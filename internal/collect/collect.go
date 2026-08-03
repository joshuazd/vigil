package collect

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

const (
	defaultGitWorkers    = 8
	defaultGitInterval   = 3 * time.Second
	defaultPRInterval    = 30 * time.Second
	defaultQueueInterval = 60 * time.Second
	defaultQueueLimit    = 20
)

type Collector struct {
	// These eight and clock are read-only once New returns. prPoller.pass reads
	// Cmd, PRInterval and clock from its own worker goroutine while Snapshot
	// runs on another, so a writer after construction is a data race - and one
	// -race would surface only if a test happened to interleave the two. A
	// test that needs a different clock or interval must set it before the
	// first Snapshot, which is the only thing that can start a pass.
	Cmd         fetch.Commander
	GitWorkers  int
	GitInterval time.Duration
	PRInterval  time.Duration

	QueueInterval   time.Duration
	QueuePRQuery    string
	QueueStoryQuery string
	QueuePRAgeDays  int
	QueueLimit      int

	// clock is nil outside tests; see now.
	clock func() time.Time

	// gitMemo holds the last git status per pane path so Snapshot can run on
	// tmux_interval without refetching git every tick. Only Snapshot's own
	// goroutine touches it: fillGit reads it before its fan-out and rewrites
	// it after the fan-out has joined. It is the last lock-free memo here, and
	// it stays that way because git is local subprocesses, not the network.
	gitMemo map[string]gitMemoEntry

	// gitStats is the last fillGit measurement, owned by the same goroutine as
	// gitMemo and for the same reason. Reset at the top of every Snapshot so a
	// Snapshot that fails before fillGit reports zero rather than the previous
	// poll's numbers - a stale measurement attached to a fresh failure is worse
	// than none.
	gitStats GitStats

	// prs owns PR data. Snapshot posts its working set and reads it; the
	// fetching happens on the poller's own worker goroutine.
	prs *prPoller

	// stories and reviews are nil when queue_enabled is false. Nil rather
	// than constructed-and-skipped: there is then no code path that can spend
	// budget by accident.
	stories *storyPoller
	reviews *reviewPoller

	// remote schedules prs and, from phase 5 on, its siblings.
	remote *remote
}

type gitMemoEntry struct {
	status    session.GitStatus
	fetchedAt time.Time
}

// dueGit carries a session through fillGit's fan-out alongside the time its
// own fetch took. Each goroutine owns exactly one of these, which is how the
// per-path timing is collected without a lock; the join in runParallel is what
// makes reading them afterwards safe.
type dueGit struct {
	s    *session.Session
	took time.Duration
}

// GitStats reports what the last fillGit cost. Total is the fan-out's wall
// time, which is the part of Snapshot that blocks publication; Slowest and
// SlowestPath name the single worst pane path, because on a monorepo one
// worktree is usually all of it.
//
// Zero when the last Snapshot found every session's git state memoized, since
// then fillGit issued no subprocesses at all.
type GitStats struct {
	Total       time.Duration
	Slowest     time.Duration
	SlowestPath string
}

// GitStats returns the last fillGit measurement. It is goroutine-owned like
// gitMemo: only the goroutine that called Snapshot may read it, and only once
// Snapshot has returned.
func (c *Collector) GitStats() GitStats { return c.gitStats }

func New(cfg *config.Config, cmd fetch.Commander) *Collector {
	workers := cfg.GetSettingInt("git_workers")
	if workers <= 0 {
		workers = defaultGitWorkers
	}
	gitInterval := cfg.GetSettingDuration("git_interval")
	if gitInterval <= 0 {
		gitInterval = defaultGitInterval
	}
	prInterval := cfg.GetSettingDuration("pr_interval")
	if prInterval <= 0 {
		prInterval = defaultPRInterval
	}
	queueInterval := cfg.GetSettingDuration("queue_interval")
	if queueInterval <= 0 {
		queueInterval = defaultQueueInterval
	}
	queueLimit := cfg.GetSettingInt("queue_limit")
	if queueLimit <= 0 {
		queueLimit = defaultQueueLimit
	}

	c := &Collector{
		Cmd:             cmd,
		GitWorkers:      workers,
		GitInterval:     gitInterval,
		PRInterval:      prInterval,
		QueueInterval:   queueInterval,
		QueuePRQuery:    cfg.GetSetting("queue_pr_query"),
		QueueStoryQuery: cfg.GetSetting("queue_story_query"),
		QueuePRAgeDays:  cfg.GetSettingInt("queue_pr_age_days"),
		QueueLimit:      queueLimit,
	}
	c.prs = newPRPoller(c)

	pollers := []poller{c.prs}
	if cfg.GetSettingBool("queue_enabled") {
		c.stories = newStoryPoller(c)
		c.reviews = newReviewPoller(c)
		pollers = append(pollers, c.stories, c.reviews)
	}
	c.remote = newRemote(pollers...)
	return c
}

func (c *Collector) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

// Start runs the remote pollers' workers. Every process that calls Snapshot
// must call this once: without it no off-box data is ever fetched. It is safe
// to call more than once and does nothing on a second call.
//
// ctx must outlive the process's use of the collector. The first call wins
// permanently, so starting with a short-lived or already-cancelled context
// disables the pollers for good and the later call this tolerates becomes a
// silent no-op.
//
// A process that never calls Snapshot may still call it. The workers are woken
// only by a nudge, so a daemon-fed client's stay blocked and spend nothing.
func (c *Collector) Start(ctx context.Context) { c.remote.start(ctx) }

// Wait joins the workers after their context is cancelled. The daemon calls it
// before Run returns, so the process does not release its flock and unlink its
// socket with a gh child still running.
func (c *Collector) Wait() { c.remote.wait() }

// RefreshRemote runs one pass of every poller on the caller's goroutine. The
// workers are a scheduler over this. It exists so a test can drive a pass
// deterministically instead of racing a goroutine; production reaches a pass
// only through Start.
func (c *Collector) RefreshRemote(ctx context.Context) { c.remote.refresh(ctx) }

// Invalidate drops the git memo and makes every remote entry due, so a caller
// that just changed state - a merge, a draft toggle, the Refresh key - does
// not have to wait out git_interval or pr_interval.
//
// Git comes back inside the next Snapshot, because fillGit is synchronous.
// Remote data comes back a tick later, when the pass this nudges has landed.
//
// The git half must only ever be called from the same goroutine as Snapshot:
// gitMemo is not guarded by a lock. The remote half is safe from anywhere.
func (c *Collector) Invalidate() {
	c.gitMemo = nil
	c.remote.invalidate()
	c.remote.nudge()
}

func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error) {
	c.gitStats = GitStats{}

	raw, err := fetch.ListSessions(ctx, c.Cmd)
	if err != nil {
		return nil, err
	}

	bells := fetch.BellFlags(ctx, c.Cmd)

	sessions := make([]*session.Session, len(raw))
	for i, r := range raw {
		sessions[i] = &session.Session{
			Name:     r.Name,
			PanePath: r.PanePath,
			Created:  r.Created,
			ID:       r.ID,
			HasBell:  bells[r.Name],
		}
	}

	c.fillGit(ctx, sessions)

	// Everything past here is local. The PR store is read as it stands and the
	// workers are nudged to refresh it; whatever they fetch is published by
	// the next Snapshot, at most one tick later. Nothing here blocks on the
	// network, which is the whole contract.
	branches := groupByBranchRoot(sessions)
	c.prs.track(branches)
	c.prs.fill(branches)
	c.remote.nudge()
	return sessions, nil
}

func runParallel[T any](items []T, workers int, do func(T)) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			do(item)
		}(item)
	}
	wg.Wait()
}

func (c *Collector) fillGit(ctx context.Context, sessions []*session.Session) {
	now := c.now()

	var due []*dueGit
	memo := make(map[string]gitMemoEntry, len(sessions))
	for _, s := range sessions {
		if prev, ok := c.gitMemo[s.PanePath]; ok && now.Sub(prev.fetchedAt) < c.GitInterval {
			s.Git = prev.status
			memo[s.PanePath] = prev
			continue
		}
		due = append(due, &dueGit{s: s})
	}

	if len(due) == 0 {
		c.gitMemo = memo
		return
	}

	start := time.Now()
	runParallel(due, c.GitWorkers, func(d *dueGit) {
		fetchStart := time.Now()
		d.s.Git = fetch.FetchGitStatus(ctx, c.Cmd, d.s.PanePath)
		d.took = time.Since(fetchStart)
	})

	stats := GitStats{Total: time.Since(start)}
	for _, d := range due {
		memo[d.s.PanePath] = gitMemoEntry{status: d.s.Git, fetchedAt: now}
		if d.took > stats.Slowest {
			stats.Slowest = d.took
			stats.SlowestPath = d.s.PanePath
		}
	}
	c.gitMemo = memo
	c.gitStats = stats
}

type branchRoot struct {
	key             string
	branch, gitRoot string
	sessions        []*session.Session
}

func groupByBranchRoot(sessions []*session.Session) []*branchRoot {
	byKey := make(map[string]*branchRoot)
	var branches []*branchRoot
	for _, s := range sessions {
		if s.Git.Branch == "" || s.Git.GitRoot == "" {
			continue
		}
		key := s.Git.Branch + "\x00" + s.Git.GitRoot
		if br, exists := byKey[key]; exists {
			br.sessions = append(br.sessions, s)
			continue
		}
		br := &branchRoot{key: key, branch: s.Git.Branch, gitRoot: s.Git.GitRoot, sessions: []*session.Session{s}}
		byKey[key] = br
		branches = append(branches, br)
	}
	return branches
}

// Queue merges the two queue stores, drops anything a live tmux session
// already covers, sorts and caps. hidden is what this call removed, which is
// the only number vigil can honestly report: the queries filter server-side
// and vigil cannot see what they dropped.
//
// Pure over the stores plus sessions. Snapshot does not call it; the daemon
// and the self-polling client each call it once per poll, and a daemon-fed
// client never calls it at all.
func (c *Collector) Queue(sessions []*session.Session) ([]session.QueueItem, int) {
	if c.stories == nil && c.reviews == nil {
		return nil, 0
	}

	var all []session.QueueItem
	if c.stories != nil {
		all = append(all, c.stories.list()...)
	}
	if c.reviews != nil {
		all = append(all, c.reviews.list()...)
	}

	items := make([]session.QueueItem, 0, len(all))
	hidden := 0
	for _, it := range all {
		if coveredBySession(it, sessions) {
			hidden++
			continue
		}
		items = append(items, it)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == session.QueueStory
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})

	if c.QueueLimit > 0 && len(items) > c.QueueLimit {
		items = items[:c.QueueLimit]
	}
	if len(items) == 0 {
		return nil, hidden
	}
	return items, hidden
}

func coveredBySession(it session.QueueItem, sessions []*session.Session) bool {
	for _, s := range sessions {
		if it.MatchesSessionName(s.Name) {
			return true
		}
		if it.Kind == session.QueueReview && s.PR != nil && strconv.Itoa(s.PR.Number) == it.ID {
			return true
		}
	}
	return false
}

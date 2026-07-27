package collect

import (
	"context"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

const (
	defaultGitWorkers  = 8
	defaultGitInterval = 3 * time.Second
	defaultPRInterval  = 30 * time.Second

	// prWorkers caps concurrent gh invocations. Each due branch costs two of
	// them against a per-hour API quota, so this stays below GitWorkers.
	prWorkers = 4
)

type Collector struct {
	Cmd         fetch.Commander
	GitWorkers  int
	GitInterval time.Duration
	PRInterval  time.Duration

	// clock is nil outside tests; see now.
	clock func() time.Time

	// gitMemo holds the last git status per pane path so Snapshot can run on
	// tmux_interval without refetching git every tick. Only Snapshot's own
	// goroutine touches it: fillGit reads it before its fan-out and rewrites
	// it after the fan-out has joined.
	gitMemo map[string]gitMemoEntry

	// prMemo holds the last PR result per branch+git-root so Snapshot can run
	// on tmux_interval without refetching PRs every tick. Only Snapshot's own
	// goroutine touches it: fillPRs reads it before its fan-out and rewrites
	// it after the fan-out has joined.
	prMemo map[string]prMemoEntry
}

type gitMemoEntry struct {
	status    session.GitStatus
	fetchedAt time.Time
}

type prMemoEntry struct {
	pr        *session.PRStatus
	fetchedAt time.Time
}

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
	return &Collector{Cmd: cmd, GitWorkers: workers, GitInterval: gitInterval, PRInterval: prInterval}
}

func (c *Collector) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error) {
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
			HasBell:  bells[r.Name],
		}
	}

	c.fillGit(ctx, sessions)
	c.fillPRs(ctx, sessions)
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

	var due []*session.Session
	memo := make(map[string]gitMemoEntry, len(sessions))
	for _, s := range sessions {
		if prev, ok := c.gitMemo[s.PanePath]; ok && now.Sub(prev.fetchedAt) < c.GitInterval {
			s.Git = prev.status
			memo[s.PanePath] = prev
			continue
		}
		due = append(due, s)
	}

	runParallel(due, c.GitWorkers, func(s *session.Session) {
		s.Git = fetch.FetchGitStatus(ctx, c.Cmd, s.PanePath)
	})

	for _, s := range due {
		memo[s.PanePath] = gitMemoEntry{status: s.Git, fetchedAt: now}
	}
	c.gitMemo = memo
}

type branchRoot struct {
	key             string
	branch, gitRoot string
	sessions        []*session.Session
	pr              *session.PRStatus
}

func (c *Collector) fillPRs(ctx context.Context, sessions []*session.Session) {
	branches := groupByBranchRoot(sessions)
	now := c.now()

	var due []*branchRoot
	memo := make(map[string]prMemoEntry, len(branches))
	for _, br := range branches {
		if prev, ok := c.prMemo[br.key]; ok && now.Sub(prev.fetchedAt) < c.PRInterval {
			br.pr = prev.pr
			memo[br.key] = prev
			continue
		}
		due = append(due, br)
	}

	runParallel(due, prWorkers, func(br *branchRoot) {
		br.pr = fetch.FetchPRStatus(ctx, c.Cmd, br.branch, br.gitRoot)
	})

	for _, br := range due {
		if br.pr == nil {
			// A failed fetch keeps the last known PR rather than blanking the
			// column and flipping the session to idle. fetchedAt still moves,
			// so a rate-limited gh is not retried every git_interval.
			if prev, ok := c.prMemo[br.key]; ok {
				br.pr = prev.pr
			}
		}
		memo[br.key] = prMemoEntry{pr: br.pr, fetchedAt: now}
	}
	c.prMemo = memo

	for _, br := range branches {
		for _, s := range br.sessions {
			s.PR = br.pr
		}
	}
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

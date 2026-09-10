package collect

import (
	"context"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// dirtyPoller owns exactly three fields - Modified, Added, Deleted - and
// fetches them off the publication path.
//
// The name is the boundary: it is not a git poller. Branch, GitRoot, Unpushed
// and RebaseAgeSecs stay synchronous inside Snapshot, because groupByBranchRoot
// feeds prPoller's working set from the first two, this poller keys its own
// working set on GitRoot, and transition.Detect reads both off the event.
// Moving them would delay PR resolution by a tick and blank the branch on the
// first poll, for no gain: they are not the latency source. If either of the
// other two ever measures slow it can join this poller without anything here
// changing shape.
//
// Keyed by git root, not by pane path: status --porcelain runs in the root, so
// two sessions inside one worktree ask the same question and one subprocess
// answers both.
//
// Structurally a sibling of prPoller, deliberately, down to the gen counter and
// the write-back prune.
type dirtyPoller struct {
	c *Collector

	// passMu makes a pass single-flight. The scheduler gives one goroutine per
	// poller, but RefreshDirty can be called from another, and two concurrent
	// passes would spend two status calls per root for one result. Held across
	// the fetch; mu is not.
	passMu sync.Mutex

	// mu guards entries, working, warmed and gen, and is held only for the map
	// work at either end of a pass.
	mu      sync.Mutex
	entries map[string]dirtyEntry
	working []string

	// warmed records which roots have had their one unflagged status. Marked
	// on attempt rather than on success: a warm that lost the index lock to
	// the user's own git leaves the root exactly as it was, which is today's
	// behaviour, whereas retrying a multi-minute call every pass would be far
	// worse than the problem. Pruned with entries, so a root that leaves and
	// returns is warmed again - cheap, since by then it is warm and the call
	// costs a fraction of a second.
	warmed map[string]bool

	// gen counts invalidations. A pass reads it alongside the working set and
	// compares again at write-back: if it moved, an invalidate landed while the
	// fetch was in flight and the fetch's answer may predate it.
	gen uint64

	// stats is the last pass's measurement. Guarded by mu rather than
	// goroutine-owned like GitStats, because the worker writes it while
	// daemon.poll reads it from Run's goroutine.
	stats DirtyStats
}

type dirtyCounts struct{ modified, added, deleted int }

type dirtyEntry struct {
	counts    dirtyCounts
	fetchedAt time.Time
	took      time.Duration
}

// staleAfter is how long this root stays fresh: git_interval normally, but
// never less than its own last fetch took.
//
// The floor is what stops a slow root pinning a worker. git_interval is a
// statement about how fresh the counts should be, and honouring it for a root
// that needs longer than the interval to answer means running status
// back-to-back forever - which is worst exactly when the machine is too loaded
// to afford it. Measured on 2026-09-10 at load average 293: status at
// ~/pr-36411 spent its whole 10s command timeout and was reissued immediately,
// indefinitely. With the floor that root costs at most half a worker.
//
// prPoller has no equivalent and needs none: at a 30s interval against
// sub-second gh calls the floor would never bind.
func (e dirtyEntry) staleAfter(interval time.Duration) time.Duration {
	if e.took > interval {
		return e.took
	}
	return interval
}

type dueRoot struct {
	root   string
	counts dirtyCounts
	took   time.Duration

	// warm asks for one unflagged status before the fetch, to let git persist
	// the untracked cache. Set for a root this poller has not warmed yet.
	warm bool
}

// warmTimeout bounds the one unflagged status per root. It is minutes rather
// than the commander's default 10s because the call it wraps is slow by
// definition - it is only issued for a root whose cache is cold, and a cold
// portal worktree measured 154s on 2026-09-10. The 10s default is what killed
// this work every time before it could finish.
const warmTimeout = 3 * time.Minute

func newDirtyPoller(c *Collector) *dirtyPoller {
	return &dirtyPoller{c: c, entries: make(map[string]dirtyEntry), warmed: make(map[string]bool)}
}

// track posts the working set: the deduped git roots the most recent Snapshot
// saw. Latest wins, as with prPoller.
//
// A session with no git root - not a repository, or git unreachable - is
// excluded and gets zero counts, which is what it gets today.
func (p *dirtyPoller) track(sessions []*session.Session) {
	seen := make(map[string]struct{}, len(sessions))
	working := make([]string, 0, len(sessions))
	for _, s := range sessions {
		root := s.Git.GitRoot
		if root == "" {
			continue
		}
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		working = append(working, root)
	}
	p.mu.Lock()
	p.working = working
	p.mu.Unlock()
}

// fill writes each session's counts from the store.
//
// There is no pending flag and that is a decision, not an omission. PRPending
// exists because transition.Detect must tell "never resolved" from "known to
// have no PR"; nothing gates on a dirty count - Session.State never reads one
// and GitStatus.IsClean has no non-test caller in the tree - so no transition,
// notify hook or auto_cleanup can be provoked by a count arriving a tick late.
//
// The accepted cost is a one-tick zero: at daemon start, and when a session
// first appears, the Git column renders unpushed and rebase age but no
// ~N +N -N until the first pass for that root lands.
func (p *dirtyPoller) fill(sessions []*session.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range sessions {
		e, ok := p.entries[s.Git.GitRoot]
		if !ok {
			continue
		}
		s.Git.Modified, s.Git.Added, s.Git.Deleted = e.counts.modified, e.counts.added, e.counts.deleted
	}
}

// invalidate makes every root due without dropping what is known, matching
// prPoller. Zeroing rather than dropping keeps the last known counts on screen
// through a forced refresh instead of blanking the column while the pass runs.
func (p *dirtyPoller) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	for k, e := range p.entries {
		e.fetchedAt = time.Time{}
		p.entries[k] = e
	}
}

func (p *dirtyPoller) pass(ctx context.Context) {
	p.passMu.Lock()
	defer p.passMu.Unlock()

	now := p.c.now()
	interval := p.c.GitInterval

	p.mu.Lock()
	startGen := p.gen
	var due []*dueRoot
	for _, root := range p.working {
		if prev, ok := p.entries[root]; ok && now.Sub(prev.fetchedAt) < prev.staleAfter(interval) {
			continue
		}
		due = append(due, &dueRoot{root: root, warm: !p.warmed[root]})
	}
	p.mu.Unlock()

	var stats DirtyStats
	if len(due) > 0 {
		// GitWorkers (8) rather than prWorkers (4): prWorkers is low because
		// each gh call spends a rate-limited quota, and a local subprocess has
		// no quota. This is not a claim that peak git concurrency stays at 8:
		// fillGit can fan out to GitWorkers on Snapshot's goroutine at the same
		// time a pass fans out to GitWorkers here, so the real peak is up to
		// 16. Measured harmless (1 vs 9 simultaneous status calls: 0.065s vs
		// 0.067s) in the design doc.
		start := time.Now()
		runParallel(due, p.c.GitWorkers, func(d *dueRoot) {
			if d.warm {
				// Its own deadline: the commander's default 10s is shorter
				// than a cold worktree's status and would kill exactly the
				// call that makes every later one fast.
				warmCtx, cancel := context.WithTimeout(ctx, warmTimeout)
				fetch.WarmDirtyCache(warmCtx, p.c.Cmd, d.root)
				cancel()
			}
			fetchStart := time.Now()
			d.counts.modified, d.counts.added, d.counts.deleted = fetch.FetchDirtyCounts(ctx, p.c.Cmd, d.root)
			d.took = time.Since(fetchStart)
		})
		stats.Total = time.Since(start)
		for _, d := range due {
			if d.took > stats.Slowest {
				stats.Slowest, stats.SlowestRoot = d.took, d.root
			}
		}
	}

	// Read after the fetch, not reused from the due scan's now. A fetch slower
	// than git_interval would otherwise be stale the moment it was written and
	// the next pass would reissue it with no gap at all, which is the loop
	// staleAfter's floor exists to bound.
	fetchedAt := p.c.now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats = stats

	// Prune to the working set as it stands now, not as it stood when the
	// fetch started: a root that vanished mid-fetch must not survive, and its
	// result must not be written back. This runs even when nothing was due, or
	// a root dropped between two passes that both found nothing else to do
	// would linger indefinitely.
	live := make(map[string]struct{}, len(p.working))
	for _, root := range p.working {
		live[root] = struct{}{}
	}
	next := make(map[string]dirtyEntry, len(p.working))
	for root, e := range p.entries {
		if _, ok := live[root]; ok {
			next[root] = e
		}
	}
	nextWarmed := make(map[string]bool, len(p.working))
	for root := range p.warmed {
		if _, ok := live[root]; ok {
			nextWarmed[root] = true
		}
	}
	for _, d := range due {
		if _, ok := live[d.root]; ok && d.warm {
			nextWarmed[d.root] = true
		}
	}
	p.warmed = nextWarmed

	invalidated := p.gen != startGen
	for _, d := range due {
		if _, ok := live[d.root]; !ok {
			continue
		}
		at := fetchedAt
		if invalidated {
			at = time.Time{}
		}
		next[d.root] = dirtyEntry{counts: d.counts, fetchedAt: at, took: d.took}
	}
	p.entries = next
}

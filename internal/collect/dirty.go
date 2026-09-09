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

	// mu guards entries, working and gen, and is held only for the map work at
	// either end of a pass.
	mu      sync.Mutex
	entries map[string]dirtyEntry
	working []string

	// gen counts invalidations. A pass reads it alongside the working set and
	// compares again at write-back: if it moved, an invalidate landed while the
	// fetch was in flight and the fetch's answer may predate it.
	gen uint64
}

type dirtyCounts struct{ modified, added, deleted int }

type dirtyEntry struct {
	counts    dirtyCounts
	fetchedAt time.Time
}

type dueRoot struct {
	root   string
	counts dirtyCounts
}

func newDirtyPoller(c *Collector) *dirtyPoller {
	return &dirtyPoller{c: c, entries: make(map[string]dirtyEntry)}
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
		if prev, ok := p.entries[root]; ok && now.Sub(prev.fetchedAt) < interval {
			continue
		}
		due = append(due, &dueRoot{root: root})
	}
	p.mu.Unlock()

	if len(due) > 0 {
		// GitWorkers (8) rather than prWorkers (4): prWorkers is low because
		// each gh call spends a rate-limited quota, and a local subprocess has
		// no quota. 8 is what fillGit already fanned out to.
		runParallel(due, p.c.GitWorkers, func(d *dueRoot) {
			d.counts.modified, d.counts.added, d.counts.deleted = fetch.FetchDirtyCounts(ctx, p.c.Cmd, d.root)
		})
	}

	p.mu.Lock()
	defer p.mu.Unlock()

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

	invalidated := p.gen != startGen
	for _, d := range due {
		if _, ok := live[d.root]; !ok {
			continue
		}
		fetchedAt := now
		if invalidated {
			fetchedAt = time.Time{}
		}
		next[d.root] = dirtyEntry{counts: d.counts, fetchedAt: fetchedAt}
	}
	p.entries = next
}

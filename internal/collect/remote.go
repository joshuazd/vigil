package collect

import (
	"context"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// prWorkers caps concurrent gh invocations. Each due branch costs two of them
// against a per-hour API quota, so this stays below GitWorkers.
const prWorkers = 4

// prPoller holds PR state per branch and git root. It is the mutex-guarded
// successor to the goroutine-owned prMemo: two goroutines reach it now, the
// worker that fetches and whichever one calls Snapshot.
//
// It reads its interval and its clock through the Collector rather than
// copying them, because Collector.PRInterval and Collector.clock are the knobs
// New and the tests already treat as the single source of truth.
type prPoller struct {
	c *Collector

	// passMu makes a pass single-flight. The scheduler gives one goroutine per
	// poller, but refresh can be called from another, and two concurrent
	// passes would spend two gh calls for one result. Held across the fetch;
	// mu is not.
	passMu sync.Mutex

	// mu guards entries, working and gen, and is held only for the map work
	// at either end of a pass.
	mu      sync.Mutex
	entries map[string]prEntry
	working []branchKey

	// gen counts invalidations. A pass reads it alongside the working set
	// and compares again at write-back: if it moved, an invalidate landed
	// while the fetch was in flight and the fetch's answer may predate it.
	gen uint64
}

type prEntry struct {
	pr        *session.PRStatus
	fetchedAt time.Time
}

type branchKey struct {
	key, branch, gitRoot string
}

type dueBranch struct {
	branchKey
	pr *session.PRStatus
}

func newPRPoller(c *Collector) *prPoller {
	return &prPoller{c: c, entries: make(map[string]prEntry)}
}

// track posts the working set. Latest wins: a pass prunes its store to
// whatever the most recent Snapshot saw, which is where the old per-Snapshot
// memo rebuild went.
func (p *prPoller) track(branches []*branchRoot) {
	working := make([]branchKey, 0, len(branches))
	for _, br := range branches {
		working = append(working, branchKey{key: br.key, branch: br.branch, gitRoot: br.gitRoot})
	}
	p.mu.Lock()
	p.working = working
	p.mu.Unlock()
}

// fill writes each session's PR from the store. A branch with no entry at all
// has never been resolved, which is a different thing from a branch known to
// have no PR, and transition.Detect treats them differently - so the
// distinction has to survive onto the session.
func (p *prPoller) fill(branches []*branchRoot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, br := range branches {
		entry, resolved := p.entries[br.key]
		for _, s := range br.sessions {
			if !resolved {
				s.PRPending = true
				continue
			}
			s.PR = entry.pr
		}
	}
}

// invalidate makes every branch due without dropping what is known. Dropping
// the entries would re-mark every branch pending, and Detect skips a pending
// session, so a forced refresh would swallow the next transition it was asked
// to go and find.
func (p *prPoller) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	for k, e := range p.entries {
		e.fetchedAt = time.Time{}
		p.entries[k] = e
	}
}

func (p *prPoller) pass(ctx context.Context) {
	p.passMu.Lock()
	defer p.passMu.Unlock()

	now := p.c.now()
	interval := p.c.PRInterval

	p.mu.Lock()
	startGen := p.gen
	var due []*dueBranch
	for _, bk := range p.working {
		if prev, ok := p.entries[bk.key]; ok && now.Sub(prev.fetchedAt) < interval {
			continue
		}
		due = append(due, &dueBranch{branchKey: bk})
	}
	p.mu.Unlock()

	if len(due) > 0 {
		runParallel(due, prWorkers, func(d *dueBranch) {
			d.pr = fetch.FetchPRStatus(ctx, p.c.Cmd, d.branch, d.gitRoot)
		})
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Prune to the working set as it stands now, not as it stood when the
	// fetch started: a branch that vanished mid-fetch must not survive, and
	// its result must not be written back. This runs even when nothing was
	// due, or a branch dropped between two passes that both found nothing
	// else to do would linger in entries indefinitely.
	live := make(map[string]struct{}, len(p.working))
	for _, bk := range p.working {
		live[bk.key] = struct{}{}
	}
	next := make(map[string]prEntry, len(p.working))
	for key, e := range p.entries {
		if _, ok := live[key]; ok {
			next[key] = e
		}
	}

	// If gen moved, an invalidate landed after this pass read the working
	// set: this fetch's answer may predate it, so its entries stay due
	// rather than being satisfied by a stale one.
	invalidated := p.gen != startGen
	for _, d := range due {
		if _, ok := live[d.key]; !ok {
			continue
		}
		pr := d.pr
		if pr == nil {
			// A failed fetch keeps the last known PR rather than blanking the
			// column and flipping the session to idle. fetchedAt still moves,
			// so a rate-limited gh is not retried on every nudge.
			if prev, ok := p.entries[d.key]; ok {
				pr = prev.pr
			}
		}
		fetchedAt := now
		if invalidated {
			fetchedAt = time.Time{}
		}
		next[d.key] = prEntry{pr: pr, fetchedAt: fetchedAt}
	}
	p.entries = next
}

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

// A poller owns one class of off-box data: its own store, its own locking, and
// its own idea of what is due. Nothing it does can block Snapshot.
//
// pass runs one fetch cycle and returns immediately when nothing is due.
// invalidate drops due-ness so the next pass refetches.
type poller interface {
	pass(ctx context.Context)
	invalidate()
}

// remote schedules pollers, one goroutine each so a slow poller cannot delay
// another.
//
// It has no ticker, and that is load-bearing rather than a simplification.
// Cadence comes from whoever calls Snapshot, which nudges at the end of every
// call. A daemon-fed client never calls Snapshot - startPoll refuses while a
// daemon is connected - so its workers block forever and it spends no gh
// budget. That is the property the daemon exists to provide: one gh budget
// regardless of how many panels are on screen. A ticker here would restore
// per-panel polling for every open panel, silently.
type remote struct {
	pollers []poller
	wakes   []chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
}

func newRemote(pollers ...poller) *remote {
	r := &remote{pollers: pollers}
	for range pollers {
		r.wakes = append(r.wakes, make(chan struct{}, 1))
	}
	return r
}

// start is idempotent rather than fatal on a second call: a client that loses
// and regains a daemon can reach it more than once, and a second set of
// workers would double the fetch rate for one collector. sync.Once, not a
// bool: that path is reachable from more than one goroutine, and an
// unsynchronized bool would race the read/write on itself.
func (r *remote) start(ctx context.Context) {
	r.once.Do(func() {
		for i, p := range r.pollers {
			p, wake := p, r.wakes[i]
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-wake:
					}
					p.pass(ctx)
				}
			}()
		}
	})
}

func (r *remote) wait() { r.wg.Wait() }

// nudge wakes every worker without blocking. The channels are cap-1, so a
// nudge that arrives while a pass is running coalesces into the one already
// queued and the worker re-checks the moment it finishes.
func (r *remote) nudge() {
	for _, wake := range r.wakes {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (r *remote) invalidate() {
	for _, p := range r.pollers {
		p.invalidate()
	}
}

// refresh runs one pass of every poller on the caller's goroutine. The workers
// are a scheduler over this, and it is the seam a test drives so it never has
// to race a goroutine. Production reaches a pass only through start.
func (r *remote) refresh(ctx context.Context) {
	for _, p := range r.pollers {
		p.pass(ctx)
	}
}

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

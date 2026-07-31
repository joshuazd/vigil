# Collector Async Remote Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Take `gh` off `Collector.Snapshot`'s critical path so publication is never behind a network call, and leave behind a seam phase 5's two new pollers plug into without touching `Snapshot`.

**Architecture:** A `poller` interface owns one class of off-box data with its own store, locking and due-ness. A `remote` scheduler runs one goroutine per poller, woken only by `Snapshot` - never by a ticker. `Snapshot` becomes local-only: tmux, bells, git, then a locked read of the PR store and a nudge. A session whose PR has never resolved carries `PRPending`, and `transition.Detector` skips it so async fill cannot fire a burst of `notify` hooks and `auto_cleanup` on daemon start.

**Tech Stack:** Go 1.x, stdlib only (`sync`, `context`, `time`). Tests are stdlib `testing` with the existing `fetch.MockCommander`.

**Design:** `docs/superpowers/specs/2026-07-31-collector-async-remote-design.md`. Read it first. Where this plan and the spec disagree, the spec is right; where this plan and the shipped code disagree, the code is right - see the phase 3 handoff's process notes on why that rule exists in this repo.

## Global Constraints

- `make test` is `go test -race ./...`. **`-race` is not optional**: this change adds shared mutable state across goroutines, so it is the primary correctness gate, not a nicety.
- `make lint` is `golangci-lint`. It must pass. `staticcheck` in particular flags nil-check-then-use.
- **No em dashes** anywhere, including comments and commit messages. Plain `-`.
- **Comments only where the code cannot say it.** Every comment this plan asks for explains a *why* that is invisible from the code. Do not add narration.
- `protocol.Version` stays **1**. `PRPending` is additive; an old panel ignores the key.
- Any test reaching `config.Load(config.ConfigPath())` or `cache.CachePath()` must `t.Setenv("HOME", t.TempDir())` first, or it reads the developer's real config and cache.
- Every test in this plan must be **watched fail first**, and fail for the stated reason. This repo has shipped plans whose tests would have passed with their subject deleted; the run-it-and-read-the-message steps are not ceremony.
- Commit after each task with the message given. Do not squash tasks together.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/collect/remote.go` | **New.** The `poller` seam, the `remote` scheduler (one goroutine per poller, no tickers), and `prPoller` - the mutex-guarded successor to `prMemo`. |
| `internal/collect/remote_test.go` | **New.** Scheduler behaviour against fake pollers: nudge wakes, no wake without a nudge, refresh is synchronous, wait joins on cancel, start is idempotent. |
| `internal/collect/collect.go` | `Snapshot` loses `fillPRs` and gains `track`/`fill`/`nudge`. `Collector` gains `prs`, `remote`, `Start`, `Wait`, `RefreshRemote`. `Invalidate` forwards to the pollers. |
| `internal/collect/collect_test.go` | Existing PR tests migrate to `Snapshot` -> `RefreshRemote` -> `Snapshot`. New tests for `PRPending` and for zero `gh` calls inside `Snapshot`. |
| `internal/session/session.go` | `PRPending bool`. |
| `internal/transition/transition.go` | `Detect` skips a session where `PRPending && PR == nil`. |
| `internal/transition/transition_test.go` | The skip, the un-mute after resolution, and the `prCache` fallback exemption. |
| `internal/daemon/daemon.go` | `Start` in `Run`, `Wait` in the shutdown arm. |
| `internal/daemon/remote_test.go` | **New.** Cold start runs no effects; `Run` starts the workers; `Run` waits for an in-flight pass; a connection is served while a pass is blocked. |
| `internal/model/model.go` | `Start` in `newModel`. |
| `internal/model/collect_cmd_test.go` | The two forced-refresh tests migrate; a new end-to-end test that `New` starts the workers. |
| `CLAUDE.md` | Conventions: the no-ticker rule, `PRPending`, the new `Collector` lifecycle. |
| `docs/superpowers/2026-07-31-collector-async-remote-handoff.md` | **New.** What landed, what was verified, what was not, landmines. |

---

## Task 1: `PRPending` and the detector skip

The detector change has to land before the collector starts producing pending sessions, or the intermediate commit fires the exact burst this whole change exists to prevent.

**Files:**
- Modify: `internal/session/session.go:118-127`
- Modify: `internal/transition/transition.go:44-65`
- Test: `internal/transition/transition_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `session.Session.PRPending bool` with json tag `pr_pending,omitempty`. Task 2b sets it; nothing else reads it except `transition.Detect`.

- [ ] **Step 1: Write the three failing tests**

Append to `internal/transition/transition_test.go`:

```go
// TestDetectDoesNotSeedAPendingSession is the whole reason PRPending exists.
// Without the skip, the first observation seeds the session at its PR-less
// state (Idle) and the second - once the async fill lands - reads as a
// transition into Done, which on the daemon runs auto_cleanup against an
// already-merged worktree on every start.
//
// Two Detect calls, not one: a single call proves nothing, because the first
// call primes and returns nothing whether or not the skip exists.
func TestDetectDoesNotSeedAPendingSession(t *testing.T) {
	d := NewDetector()

	if events := d.Detect([]*session.Session{
		{Name: "alpha", PRPending: true},
	}); len(events) != 0 {
		t.Fatalf("got %d events on the priming call, want 0", len(events))
	}

	events := d.Detect([]*session.Session{
		{Name: "alpha", PR: &session.PRStatus{Number: 1, State: "MERGED"}},
	})
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0: a pending session must not be seeded, so the observation that carries real PR data is its first sighting, not a transition", len(events))
	}
}

// TestDetectStillReportsATransitionAfterAPendingSession is the other half:
// the skip must mute the seed, not the session. A Detect that dropped pending
// sessions permanently would pass the test above and silence every later
// notify hook for that session.
func TestDetectStillReportsATransitionAfterAPendingSession(t *testing.T) {
	d := NewDetector()

	d.Detect([]*session.Session{{Name: "alpha", PRPending: true}})
	d.Detect([]*session.Session{
		{Name: "alpha", PR: &session.PRStatus{Number: 1, State: "OPEN"}},
	})

	events := d.Detect([]*session.Session{
		{Name: "alpha", PR: &session.PRStatus{Number: 1, State: "MERGED"}},
	})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].New != session.Done {
		t.Errorf("got new state %v, want Done", events[0].New)
	}
}

// TestDetectSeedsAPendingSessionThatHasAFallbackPR pins the PR == nil half of
// the condition. On a client, Model.prCache fills the last known PR for a
// branch before transitions are checked, so a pending session that already has
// data is as good as a resolved one. Skipping on PRPending alone would mute
// every transition for every branch the client has history for, for as long as
// the daemon kept republishing pending.
func TestDetectSeedsAPendingSessionThatHasAFallbackPR(t *testing.T) {
	d := NewDetector()

	d.Detect([]*session.Session{{
		Name:      "alpha",
		PRPending: true,
		PR:        &session.PRStatus{Number: 1, State: "OPEN"},
	}})

	events := d.Detect([]*session.Session{{
		Name: "alpha",
		PR:   &session.PRStatus{Number: 1, State: "MERGED"},
	}})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: a pending session with a cached PR must still be seeded", len(events))
	}
}
```

- [ ] **Step 2: Run them and confirm they fail for the right reason**

Run: `go test ./internal/transition/ -run 'TestDetect(DoesNotSeedAPending|StillReportsATransitionAfter|SeedsAPendingSessionThatHasAFallback)' -v`

Expected: a compile failure first - `unknown field PRPending in struct literal`. That is the correct first failure.

- [ ] **Step 3: Add the field**

In `internal/session/session.go`, inside `type Session struct`, after the `PR` field:

```go
	PR        *PRStatus `json:"pr,omitempty"`

	// PRPending means this session's branch has no entry in the PR store at
	// all, which is not the same as a branch known to have no PR. It exists
	// for transition.Detect: seeding a session at a PR-less state that the
	// next observation contradicts is a burst of notify hooks, and
	// auto_cleanup, on every daemon start.
	PRPending bool `json:"pr_pending,omitempty"`
```

- [ ] **Step 4: Re-run and confirm the failures are now behavioural**

Run: `go test ./internal/transition/ -run 'TestDetect(DoesNotSeedAPending|StillReportsATransitionAfter|SeedsAPendingSessionThatHasAFallback)' -v`

Expected: `TestDetectDoesNotSeedAPendingSession` FAILS with `got 1 events, want 0`. The other two PASS. That one failure is the subject of this task; read the message and confirm it is that one.

- [ ] **Step 5: Add the skip**

In `internal/transition/transition.go`, inside `Detect`'s loop, as the first statement:

```go
	for _, s := range sessions {
		// A session whose PR has never resolved is not observed at all: no
		// seed, no event, and deliberately not recorded in next, so the
		// observation that carries real data is a first sighting. PR == nil is
		// half the condition because a client fills the last known PR for the
		// branch from prCache before this runs, and a session with data is as
		// good as a resolved one.
		if s.PRPending && s.PR == nil {
			continue
		}
		state := s.State()
```

- [ ] **Step 6: Run the whole transition package**

Run: `go test -race ./internal/transition/ -v`
Expected: PASS, all tests.

- [ ] **Step 7: Run everything, to prove the field is inert so far**

Run: `make test`
Expected: PASS. Nothing sets `PRPending` yet, so no other package can have changed behaviour. If something failed, stop and find out why before continuing.

- [ ] **Step 8: Commit**

```bash
git add internal/session/session.go internal/transition/transition.go internal/transition/transition_test.go
git commit -m "feat(transition): do not seed a session whose PR has never resolved

Async PR fetching would otherwise have the first observation seed every
session at its PR-less state and the second read as a transition - a burst
of notify hooks, and auto_cleanup against already-merged worktrees, on
every daemon start.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 2a: The remote scheduler

Lands `internal/collect/remote.go` with the seam, the scheduler and `prPoller`, wired to nothing. The package compiles and the existing tests are untouched, so this task is reviewable on its own.

**Files:**
- Create: `internal/collect/remote.go`
- Create: `internal/collect/remote_test.go`

**Interfaces:**
- Consumes: `branchRoot`, `groupByBranchRoot`, `runParallel` from `collect.go` (all unexported, same package). `Collector.PRInterval` and `Collector.now()`.
- Produces, for Task 2b:
  - `type poller interface { pass(ctx context.Context); invalidate() }`
  - `func newRemote(pollers ...poller) *remote`
  - `func (r *remote) start(ctx context.Context)` - idempotent
  - `func (r *remote) wait()`
  - `func (r *remote) nudge()`
  - `func (r *remote) invalidate()`
  - `func (r *remote) refresh(ctx context.Context)`
  - `func newPRPoller(c *Collector) *prPoller`
  - `func (p *prPoller) track(branches []*branchRoot)`
  - `func (p *prPoller) fill(branches []*branchRoot)`
  - `func (p *prPoller) pass(ctx context.Context)` / `func (p *prPoller) invalidate()`

- [ ] **Step 1: Write the scheduler tests**

Create `internal/collect/remote_test.go`:

```go
package collect

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingPoller records passes and can block inside one, which is what lets a
// test observe the difference between "the worker was woken" and "the worker
// finished".
type countingPoller struct {
	mu          sync.Mutex
	passes      int
	invalidates int
	entered     chan struct{}
	release     chan struct{}
}

func newCountingPoller() *countingPoller {
	return &countingPoller{entered: make(chan struct{}, 8)}
}

func (p *countingPoller) pass(context.Context) {
	p.mu.Lock()
	p.passes++
	p.mu.Unlock()
	select {
	case p.entered <- struct{}{}:
	default:
	}
	if p.release != nil {
		<-p.release
	}
}

func (p *countingPoller) invalidate() {
	p.mu.Lock()
	p.invalidates++
	p.mu.Unlock()
}

func (p *countingPoller) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.passes
}

func (p *countingPoller) invalidateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invalidates
}

// waitForPass blocks until the poller has entered pass at least once, so a
// test never sleeps for a fixed duration hoping a goroutine got scheduled.
func waitForPass(t *testing.T, p *countingPoller) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a pass")
	}
}

func TestRemoteNudgeWakesEveryWorker(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	r := newRemote(a, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); r.wait() }()
	r.start(ctx)

	r.nudge()
	waitForPass(t, a)
	waitForPass(t, b)
}

// TestRemoteRunsNothingWithoutANudge is the load-bearing one. The workers have
// no ticker on purpose: a daemon-fed client never calls Snapshot, so it must
// never spend a gh call. A ticker here would restore per-panel polling for
// every open panel and only this test would notice.
func TestRemoteRunsNothingWithoutANudge(t *testing.T) {
	p := newCountingPoller()
	r := newRemote(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); r.wait() }()
	r.start(ctx)

	time.Sleep(200 * time.Millisecond)
	if got := p.count(); got != 0 {
		t.Errorf("got %d passes with no nudge, want 0: the remote layer must have no ticker of its own", got)
	}
}

// TestRemoteStartIsIdempotent: newModel starts the collector, and a client
// that loses and regains a daemon must not be able to double the fetch rate by
// arriving at start twice.
func TestRemoteStartIsIdempotent(t *testing.T) {
	p := newCountingPoller()
	r := newRemote(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); r.wait() }()
	r.start(ctx)
	r.start(ctx)

	r.nudge()
	waitForPass(t, p)
	time.Sleep(200 * time.Millisecond)
	if got := p.count(); got != 1 {
		t.Errorf("got %d passes for one nudge, want 1: a second start must not add a second worker", got)
	}
}

func TestRemoteRefreshRunsEveryPollerSynchronously(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	r := newRemote(a, b)

	r.refresh(context.Background())

	if got := a.count(); got != 1 {
		t.Errorf("got %d passes on poller a, want 1", got)
	}
	if got := b.count(); got != 1 {
		t.Errorf("got %d passes on poller b, want 1", got)
	}
}

func TestRemoteInvalidateReachesEveryPoller(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	r := newRemote(a, b)

	r.invalidate()

	if got := a.invalidateCount(); got != 1 {
		t.Errorf("got %d invalidates on poller a, want 1", got)
	}
	if got := b.invalidateCount(); got != 1 {
		t.Errorf("got %d invalidates on poller b, want 1", got)
	}
}

// TestRemoteWaitBlocksUntilAnInFlightPassFinishes is what the daemon's
// shutdown arm depends on. Run must not return - and so must not release its
// flock or unlink its socket - with a gh child still running.
func TestRemoteWaitBlocksUntilAnInFlightPassFinishes(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	r := newRemote(p)

	ctx, cancel := context.WithCancel(context.Background())
	r.start(ctx)
	r.nudge()
	waitForPass(t, p)

	cancel()

	returned := make(chan struct{})
	go func() {
		r.wait()
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("wait returned while a pass was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(p.release)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after the pass finished")
	}
}
```

- [ ] **Step 2: Run and confirm they fail to compile**

Run: `go test ./internal/collect/ -run TestRemote -v`
Expected: FAIL to build - `undefined: newRemote`. Correct first failure.

- [ ] **Step 3: Write `remote.go`**

Create `internal/collect/remote.go`:

```go
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
	started bool
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
// workers would double the fetch rate for one collector.
func (r *remote) start(ctx context.Context) {
	if r.started {
		return
	}
	r.started = true
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

	// mu guards entries and working, and is held only for the map work at
	// either end of a pass.
	mu      sync.Mutex
	entries map[string]prEntry
	working []branchKey
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
	var due []*dueBranch
	for _, bk := range p.working {
		if prev, ok := p.entries[bk.key]; ok && now.Sub(prev.fetchedAt) < interval {
			continue
		}
		due = append(due, &dueBranch{branchKey: bk})
	}
	p.mu.Unlock()

	if len(due) == 0 {
		return
	}

	runParallel(due, prWorkers, func(d *dueBranch) {
		d.pr = fetch.FetchPRStatus(ctx, p.c.Cmd, d.branch, d.gitRoot)
	})

	p.mu.Lock()
	defer p.mu.Unlock()

	// Prune to the working set as it stands now, not as it stood when the
	// fetch started: a branch that vanished mid-fetch must not survive, and
	// its result must not be written back.
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
		next[d.key] = prEntry{pr: pr, fetchedAt: now}
	}
	p.entries = next
}
```

- [ ] **Step 4: Delete the now-duplicated `prWorkers` const**

`prWorkers` moved to `remote.go`. Remove it from the `const` block in `internal/collect/collect.go:13-21`, leaving:

```go
const (
	defaultGitWorkers  = 8
	defaultGitInterval = 3 * time.Second
	defaultPRInterval  = 30 * time.Second
)
```

- [ ] **Step 5: Run the scheduler tests**

Run: `go test -race ./internal/collect/ -run TestRemote -v`
Expected: PASS, six tests.

- [ ] **Step 6: Run the package, which is still on the old path**

Run: `go test -race ./internal/collect/`
Expected: PASS. `remote.go` is not wired in yet, so every existing test still exercises `fillPRs`.

You will see `staticcheck` complain in the next step if `prPoller` is unused-but-constructed; it is not constructed yet, and Go does not flag unused methods or types, so the build is clean.

- [ ] **Step 7: Lint**

Run: `make lint`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/collect/remote.go internal/collect/remote_test.go internal/collect/collect.go
git commit -m "feat(collect): add the remote poller seam and scheduler

One goroutine per poller, woken only by a nudge. No ticker, deliberately:
a daemon-fed client never calls Snapshot, so it must never spend a gh call,
and a ticker here would restore per-panel polling for every open panel.

Wired to nothing yet.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 2b: Take `gh` out of `Snapshot`

**Files:**
- Modify: `internal/collect/collect.go`
- Modify: `internal/collect/collect_test.go`

**Interfaces:**
- Consumes: everything Task 2a produced.
- Produces, for Tasks 3 and 4:
  - `func (c *Collector) Start(ctx context.Context)`
  - `func (c *Collector) Wait()`
  - `func (c *Collector) RefreshRemote(ctx context.Context)`
  - `Snapshot` unchanged in signature: `func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error)`

- [ ] **Step 1: Write the new tests**

Append to `internal/collect/collect_test.go`:

```go
// TestSnapshotIssuesNoGhCalls is the entire point of the change and nothing
// else pins it. Snapshot may do local work only; every network call belongs to
// a poller running on its own goroutine.
func TestSnapshotIssuesNoGhCalls(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := countGhCalls(cmd); got != 0 {
		t.Errorf("got %d gh calls inside Snapshot, want 0: publication must never be behind a network call", got)
	}
}

// TestSnapshotMarksAnUnresolvedBranchPending and its sibling below are the
// two halves of the contract Detect reads: pending means "never resolved",
// and it must clear once the poller has an answer - including the answer
// "there is no PR".
func TestSnapshotMarksAnUnresolvedBranchPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !sessions[0].PRPending {
		t.Error("want PRPending on the first Snapshot: nothing has fetched a PR yet")
	}
	if sessions[0].PR != nil {
		t.Errorf("got PR %+v, want nil before any pass has run", sessions[0].PR)
	}
}

func TestRefreshRemoteResolvesTheBranchForTheNextSnapshot(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("PRPending should have cleared once the poller had an answer")
	}
	if sessions[0].PR == nil || sessions[0].PR.Number != 42 {
		t.Errorf("got PR %+v, want number 42", sessions[0].PR)
	}
}

// TestAResolvedBranchWithNoPRIsNotPending is the case that would otherwise
// mute a session forever: gh answering "there is no PR" is an answer, and the
// session has to become visible to Detect.
func TestAResolvedBranchWithNoPRIsNotPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", "", nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("a branch gh answered for is resolved, even when the answer is that it has no PR")
	}
	if sessions[0].PR != nil {
		t.Errorf("got PR %+v, want nil", sessions[0].PR)
	}
}

// TestASessionWithNoGitRootIsNeverPending: a session outside a repository can
// never have a PR, so marking it pending would hide it from Detect for the
// life of the process.
func TestASessionWithNoGitRootIsNeverPending(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.On("git", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("a session with no git root must not be marked pending")
	}
}

// TestPassEvictsABranchThatLeftTheWorkingSet replaces the pruning the old
// per-Snapshot memo rebuild did for free. Without it the store grows for the
// life of the daemon and a renamed branch keeps its old PR forever.
func TestPassEvictsABranchThatLeftTheWorkingSet(t *testing.T) {
	branch := "feature"
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return branch, nil
		},
	}
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	oldKey := "feature\x00/repo/alpha"
	c.prs.mu.Lock()
	_, present := c.prs.entries[oldKey]
	c.prs.mu.Unlock()
	if !present {
		t.Fatalf("fixture is broken: %q should be in the store after a pass", oldKey)
	}

	branch = "renamed"
	c.Invalidate() // drop the git memo so the new branch is read
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	c.prs.mu.Lock()
	_, stillPresent := c.prs.entries[oldKey]
	c.prs.mu.Unlock()
	if stillPresent {
		t.Errorf("%q is still in the store after leaving the working set", oldKey)
	}
}

// TestSnapshotAndRefreshRemoteAreRaceFree drives the two goroutines the design
// actually creates against each other. -race is the assertion; the loop counts
// are only there to make a window.
func TestSnapshotAndRefreshRemoteAreRaceFree(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := c.Snapshot(ctx); err != nil {
				t.Errorf("Snapshot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			c.RefreshRemote(ctx)
		}
	}()
	wg.Wait()
}
```

Add `"sync"` to the test file's imports.

- [ ] **Step 2: Run and confirm the shape of the failure**

Run: `go test ./internal/collect/ -run 'TestSnapshotIssuesNoGhCalls|TestSnapshotMarksAnUnresolvedBranchPending' -v`
Expected: build failure - `c.RefreshRemote undefined` and `c.prs undefined`. Correct first failure.

- [ ] **Step 3: Rewire `Collector`**

In `internal/collect/collect.go`, replace the `prMemo` field and `prMemoEntry` type with the poller, and delete `fillPRs`.

The struct becomes:

```go
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
	// it after the fan-out has joined. It is the last lock-free memo here, and
	// it stays that way because git is local subprocesses, not the network.
	gitMemo map[string]gitMemoEntry

	// prs owns PR data. Snapshot posts its working set and reads it; the
	// fetching happens on the poller's own worker goroutine.
	prs *prPoller

	// remote schedules prs and, from phase 5 on, its siblings.
	remote *remote
}
```

Delete `type prMemoEntry struct { ... }`.

`New` becomes:

```go
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
	c := &Collector{Cmd: cmd, GitWorkers: workers, GitInterval: gitInterval, PRInterval: prInterval}
	c.prs = newPRPoller(c)
	c.remote = newRemote(c.prs)
	return c
}
```

- [ ] **Step 4: Add the lifecycle methods and rewrite `Invalidate`**

Replace the existing `Invalidate` in `internal/collect/collect.go` with:

```go
// Start runs the remote pollers' workers. Every process that calls Snapshot
// must call this once: without it no off-box data is ever fetched. It is safe
// to call more than once and does nothing on a second call.
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
```

Add `"context"` to the imports if it is not already there (it is - `Snapshot` takes one).

- [ ] **Step 5: Rewrite `Snapshot` and delete `fillPRs`**

Replace `Snapshot`'s last three lines:

```go
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
```

Delete the whole `func (c *Collector) fillPRs(...)`. Delete the `pr *session.PRStatus` field from `type branchRoot struct` - `fill` writes to the sessions directly now.

- [ ] **Step 6: Migrate the existing PR tests**

Seven tests in `internal/collect/collect_test.go` assert PR data appearing from a single `Snapshot`. Each needs a `RefreshRemote` where the fetch used to happen. Work through them in file order:

**`TestSnapshotDeduplicatesPRFetchesByBranchAndGitRoot`** (line ~116). After the first `Snapshot`, insert the pass; the assertions then hold unchanged:

```go
	c := New(&config.Config{}, cmd)
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
```

**`TestSnapshotSkipsPRFetchWithinPRInterval`** (line ~190). The gh count is now driven by passes, not Snapshots:

```go
	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}

	c.RefreshRemote(ctx)
	if got := countGhCalls(cmd); got != 1 {
		t.Errorf("got %d gh calls after two passes, want 1 (the store should skip the refetch)", got)
	}
	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("third Snapshot: %v", err)
	}
	if second[0].PR != first[0].PR {
		t.Error("the second Snapshot should reuse the stored *PRStatus pointer")
	}
```

**`TestSnapshotRefetchesPRAfterPRInterval`** (line ~215):

```go
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	now = now.Add(c.PRInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGhCalls(cmd); got != 2 {
		t.Errorf("got %d gh calls, want 2 (the PR interval has elapsed)", got)
	}
```

**`TestSnapshotKeepsLastPRWhenFetchFails`** (line ~236):

```go
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if first[0].PR == nil {
		t.Fatal("the first pass should populate PR")
	}

	cmd.On("gh", "not json", nil)
	now = now.Add(c.PRInterval)
	c.RefreshRemote(ctx)
	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("third Snapshot: %v", err)
	}
	if second[0].PR == nil {
		t.Fatal("a failed PR fetch should keep the last known PR, got nil")
	}
	if second[0].PR != first[0].PR {
		t.Errorf("got PR %+v, want the previous pointer %+v", second[0].PR, first[0].PR)
	}
	if second[0].PRPending {
		t.Error("a failed refetch must not re-mark the branch pending: Detect would stop seeing it")
	}
```

**`TestInvalidateForcesARefetchOfGitAndPR`** (line ~335):

```go
	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGitCalls(cmd); got != 1 {
		t.Fatalf("got %d git calls on the first Snapshot, want 1", got)
	}
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}

	c.Invalidate()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGitCalls(cmd); got != 2 {
		t.Errorf("got %d git calls after Invalidate, want 2 (the memo should have been dropped)", got)
	}
	if got := countGhCalls(cmd); got != 2 {
		t.Errorf("got %d gh calls after Invalidate, want 2 (every entry should have been made due)", got)
	}
```

Then add, immediately after it, the property `Invalidate`'s comment claims and nothing else checks:

```go
// TestInvalidateDoesNotReMarkBranchesPending is why Invalidate zeroes
// fetchedAt instead of dropping the entries. A pending session is skipped by
// transition.Detect, so an Invalidate that blanked the store would have the
// forced refresh swallow the very transition it was pressed to go and find.
func TestInvalidateDoesNotReMarkBranchesPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	c.Invalidate()
	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("Invalidate must leave the branch resolved: it makes entries due, it does not forget them")
	}
	if sessions[0].PR == nil {
		t.Error("Invalidate must leave the last known PR readable until the refetch lands")
	}
}
```

**`TestSnapshotGitGatingIndependentOfPRElapsing`** (line ~390) and **`TestSnapshotPRGatingIndependentOfGitElapsing`** (line ~415): both count gh across two Snapshots. Insert `c.RefreshRemote(ctx)` immediately after each `Snapshot` call, and read the gh counts after the passes. The git assertions are unchanged.

`TestSnapshotSkipsGitFetchWithinGitInterval` and `TestSnapshotRefetchesGitAfterGitInterval` (lines ~288, ~309) touch only git counts and need no change beyond compiling.

- [ ] **Step 7: Run the collect package**

Run: `go test -race ./internal/collect/ -v`
Expected: PASS, every test. If `TestSnapshotIssuesNoGhCalls` passes but `TestSnapshotMarksAnUnresolvedBranchPending` fails, `fill` is not setting the flag. If eviction fails, check that the prune uses the *current* `p.working` rather than the snapshot taken before the fetch.

- [ ] **Step 8: Run everything and expect breakage elsewhere**

Run: `make test`
Expected: FAIL in `internal/daemon` and/or `internal/model` - those packages still assume a synchronous `Snapshot`. **Write down which tests failed.** Tasks 3 and 4 fix exactly those; a failure not on your list is a real regression, not migration.

Note: `internal/daemon/transition_test.go` should be entirely unaffected. Its `bellSwitch` fixture stubs `git` to return `""`, so no session ever has a git root, no branch is ever tracked, and nothing is ever pending. If those tests fail, `groupByBranchRoot`'s branch/root guard has been disturbed.

- [ ] **Step 9: Commit**

```bash
git add internal/collect/
git commit -m "feat(collect): take gh off Snapshot's critical path

Snapshot is local-only now: tmux, bells and git, then a locked read of the
PR store and a nudge. Whatever the workers fetch is published by the next
Snapshot, at most one tick later, instead of holding every panel's view of
data already in hand behind a network call.

Other packages are left failing; the next two commits wire them up.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Daemon lifecycle

**Files:**
- Modify: `internal/daemon/daemon.go:169-187`
- Create: `internal/daemon/remote_test.go`

**Interfaces:**
- Consumes: `Collector.Start`, `Collector.Wait`, `Collector.RefreshRemote` from Task 2b; `session.PRPending` and the `Detect` skip from Task 1.
- Produces: nothing new. `Server`'s exported surface is unchanged.

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/remote_test.go`:

```go
package daemon

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/transition"
)

// mergedPRCommander answers tmux and git for one session on one branch, with
// gh reporting a merged PR. Reaching Done is what makes the effects assertion
// below meaningful: Done is the one transition that runs auto_cleanup.
func mergedPRCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)
	return cmd
}

// TestColdStartRunsNoEffects is the regression the whole PRPending mechanism
// exists to prevent. Async PR fetching means the daemon's first poll sees no
// PR; without the skip it seeds alpha at Idle, and the poll after the fetch
// lands reads as Idle -> Done, which runs auto_cleanup against a worktree that
// was already merged before this daemon started.
//
// This drives the passes synchronously rather than starting the workers, so
// the assertion is about ordering and not about how fast a goroutine ran.
func TestColdStartRunsNoEffects(t *testing.T) {
	cmd := mergedPRCommander()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // no PR data yet: alpha is pending and must not be seeded
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx) // PR data lands: this is alpha's first real sighting
	s.pendingEffects.Wait()

	if got := effects.count(); got != 0 {
		t.Fatalf("got %d effect runs on a cold start, want 0: an already-merged session must not look like a fresh transition into Done", got)
	}
}

// TestColdStartStillReportsALaterTransition proves the test above is not
// vacuous. If the skip muted alpha permanently, both tests would be green with
// the notify hook broken.
func TestColdStartStillReportsALaterTransition(t *testing.T) {
	state := `{"number": 42, "state": "OPEN"}`
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		return state, nil
	}
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx)
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx) // seeds at Review

	state = `{"number": 42, "state": "MERGED"}`
	s.Collector.Invalidate()
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1: the skip must mute the seed, not the session", got)
	}
}

// TestRunStartsTheRemoteWorkers pins the one line that is easy to leave out
// and impossible to notice. Without Collector.Start in Run, a real daemon
// polls forever and never fetches a PR, and every collect-level test still
// passes because they drive RefreshRemote directly.
func TestRunStartsTheRemoteWorkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := mergedPRCommander()
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for cmd.CallCount("gh") == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no gh call after 3s: Run never started the remote workers")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunWaitsForAnInFlightPass: Run returning is what releases the flock and
// unlinks the socket. Doing that with a gh child still running leaves an
// orphan holding a pipe, and the next daemon start races the unlink.
func TestRunWaitsForAnInFlightPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return `{"number": 42, "state": "MERGED"}`, nil
	}
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		cancel()
		<-done
		t.Fatal("the gh stub was never reached")
	}

	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("Run returned while a remote pass was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the pass finished")
	}
}

// TestSlowRemoteDoesNotStallNewConnections is the secondary win. poll runs
// inline in Run's select loop, so before this change a slow gh call meant the
// daemon accepted no connections and handled no dispatch requests for its
// whole duration.
func TestSlowRemoteDoesNotStallNewConnections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release := make(chan struct{})
	defer close(release)
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		<-release
		return "", nil
	}
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()

	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", s.SocketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := protocol.NewDecoder(conn).Next(); err != nil {
		t.Fatalf("no snapshot while a remote pass was blocked: %v", err)
	}
}
```

`recordingEffects` already exists in this package (`transition_test.go`). There is **no** `testSocketPath` helper: the existing tests write `filepath.Join(t.TempDir(), "vigild.sock")` inline. Add one at the top of this file rather than repeating it three times:

```go
func testSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "vigild.sock")
}
```

and import `path/filepath`.

`TestRunStartsTheRemoteWorkers` polls the call count from the test goroutine while a worker goroutine appends to it. `MockCommander.Run` already appends under `m.mu` (`internal/fetch/cmd.go:169-172`), but `Calls` is exported and every existing helper reads it without the lock, so a direct read is still a data race and `-race` will say so - this is the first test in the repo to drive one `MockCommander` from two goroutines.

**Add an accessor to `MockCommander` rather than a mutex to the test**, since every future concurrent test needs the same thing:

```go
// CallCount reports how many times a command was run. Calls is appended under
// mu by a Run that may be on any goroutine, so a concurrent test cannot read
// the slice directly.
func (m *MockCommander) CallCount(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, call := range m.Calls {
		if call.Name == name {
			n++
		}
	}
	return n
}
```

The tests above already call `cmd.CallCount("gh")` directly. Do not add a package-local `countGh` wrapper.

The same applies in Task 4: `countCalls(cmd, "gh")` in `internal/model` reads `cmd.Calls` unguarded. It is only used from the test goroutine there, and `TestNewStartsTheRemoteWorkers` polls it while a worker runs - **switch that test to `cmd.CallCount("gh")`**.

- [ ] **Step 2: Run and confirm the failures**

Run: `go test ./internal/daemon/ -run 'TestColdStart|TestRunStartsTheRemoteWorkers|TestRunWaitsForAnInFlightPass|TestSlowRemoteDoesNotStallNewConnections' -v`

Expected:
- `TestColdStartRunsNoEffects` PASSES already (Task 1 landed the skip). Confirm it, and confirm it is not vacuous by checking `TestColdStartStillReportsALaterTransition` also passes.
- `TestRunStartsTheRemoteWorkers` FAILS with `no gh call after 3s`. This is the task's subject.
- `TestRunWaitsForAnInFlightPass` FAILS at `the gh stub was never reached`, for the same missing `Start`.
- `TestSlowRemoteDoesNotStallNewConnections` PASSES already, because with no `Start` nothing ever blocks. It becomes meaningful in step 4; note that and move on.

- [ ] **Step 3: Wire `Start` and `Wait` into `Run`**

In `internal/daemon/daemon.go`, immediately before `ticker := time.NewTicker(s.Interval)`:

```go
	// The collector's remote pollers fetch off the poll loop. Without this
	// they are never woken and the daemon publishes tmux and git forever
	// while never fetching a PR.
	s.Collector.Start(ctx)
```

And in the `<-ctx.Done()` arm, after `s.pendingEffects.Wait()`:

```go
			s.pendingEffects.Wait()
			// Returning here releases the flock and unlinks the socket, so it
			// must not happen while a poller still has a gh child running.
			s.Collector.Wait()
			return nil
```

- [ ] **Step 4: Re-run**

Run: `go test -race ./internal/daemon/ -run 'TestColdStart|TestRunStartsTheRemoteWorkers|TestRunWaitsForAnInFlightPass|TestSlowRemoteDoesNotStallNewConnections' -v`
Expected: all PASS.

To confirm `TestRunWaitsForAnInFlightPass` is not vacuous, temporarily delete the `s.Collector.Wait()` line and re-run it: it must fail with `Run returned while a remote pass was still in flight`. Put the line back.

- [ ] **Step 5: Run the whole daemon package**

Run: `go test -race ./internal/daemon/ -v`
Expected: PASS. In particular every test in `transition_test.go` should be untouched. If one fails, its fixture is producing a git root where it did not before.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/ internal/fetch/
git commit -m "feat(daemon): start and join the collector's remote workers

Start in Run, because nothing else wakes them and a daemon without it
polls forever while never fetching a PR. Wait in the shutdown arm, because
returning is what releases the flock and unlinks the socket.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

(Include `internal/fetch/` only if you had to add the `Calls` mutex.)

---

## Task 4: Client lifecycle

**Files:**
- Modify: `internal/model/model.go:206-248`
- Modify: `internal/model/collect_cmd_test.go`

**Interfaces:**
- Consumes: `Collector.Start`, `Collector.RefreshRemote`.
- Produces: nothing new.

- [ ] **Step 1: Migrate the two forced-refresh tests**

`TestStartPollForceReachesInvalidateEndToEnd` (line ~619) and `TestRefreshKeyReachesInvalidateEndToEnd` (line ~940) both count `gh` calls after a `Snapshot`, which is now always zero. The claim each pins - that force reaches `Invalidate` - survives; the count just moves behind a pass.

In `TestStartPollForceReachesInvalidateEndToEnd`, replace the body from `unforced := m.startPoll(false)` down:

```go
	ctx := context.Background()

	unforced := m.startPoll(false)
	if unforced == nil {
		t.Fatal("the first poll was refused")
	}
	if _, ok := unforced().(SnapshotMsg); !ok {
		t.Fatal("the first poll did not produce a SnapshotMsg")
	}
	if got := countCalls(cmd, "gh"); got != 0 {
		t.Fatalf("got %d gh calls inside a poll, want 0: Snapshot must not touch the network", got)
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}

	m.pollInFlight = false // stand in for that first poll's SnapshotMsg having landed
	forced := m.startPoll(true)
	if forced == nil {
		t.Fatal("the forced poll was refused")
	}
	if _, ok := forced().(SnapshotMsg); !ok {
		t.Fatal("the forced poll did not produce a SnapshotMsg")
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 2 {
		t.Errorf("got %d gh calls after a forced poll, want 2 (force should have made every entry due)", got)
	}
```

Apply the same shape to `TestRefreshKeyReachesInvalidateEndToEnd`: a `RefreshRemote` after the first poll asserting 1, and a `RefreshRemote` after the `r` keypress asserting 2.

- [ ] **Step 2: Write the new test**

Append to `internal/model/collect_cmd_test.go`:

```go
// TestNewStartsTheRemoteWorkers pins the client half of the line the daemon
// needed in Run. Without Collector.Start in newModel, a self-polling panel
// renders tmux and git correctly and never shows a PR column again, and every
// test in internal/collect stays green because they drive RefreshRemote
// directly.
//
// This goes through the real New rather than newTestModel: newTestModel builds
// a Model literal, so it cannot observe anything newModel does.
func TestNewStartsTheRemoteWorkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no socket there, so this client self-polls

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	m := New(&config.Config{}, cmd)
	defer m.cancel()
	if m.daemonDecoder != nil {
		t.Fatal("fixture is broken: this client found a daemon and will never self-poll")
	}

	// Init's first collectCmd is what nudges the workers.
	if msg := m.collectCmd(false)(); msg == nil {
		t.Fatal("collectCmd produced no message")
	}

	deadline := time.After(3 * time.Second)
	for cmd.CallCount("gh") == 0 {
		select {
		case <-deadline:
			t.Fatal("no gh call after 3s: New never started the remote workers")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestADaemonFedClientSpendsNoGhBudget is the whole reason the remote layer
// has no ticker. One daemon means one gh budget regardless of how many panels
// are open; a ticker in the poller would quietly restore per-panel polling and
// nothing else in the suite would notice.
func TestADaemonFedClientSpendsNoGhBudget(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.daemonConn = &fakeConn{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.collector.Start(ctx)

	if got := m.startPoll(false); got != nil {
		t.Fatal("a daemon-fed client issued a poll of its own")
	}
	time.Sleep(300 * time.Millisecond)

	if got := countCalls(cmd, "gh"); got != 0 {
		t.Errorf("got %d gh calls on a daemon-fed client, want 0", got)
	}
}
```

Ensure `collect`, `context`, `time` and `fetch` are imported in that file; most already are.

- [ ] **Step 3: Run and confirm the failure**

Run: `go test ./internal/model/ -run 'TestNewStartsTheRemoteWorkers|TestADaemonFedClientSpendsNoGhBudget|ReachesInvalidateEndToEnd' -v`
Expected: `TestNewStartsTheRemoteWorkers` FAILS with `no gh call after 3s`. The other three PASS. If `TestADaemonFedClientSpendsNoGhBudget` fails at this point, a ticker crept into `remote.start` - go back to Task 2a.

- [ ] **Step 4: Start the collector in `newModel`**

In `internal/model/model.go`, immediately after the `m := Model{...}` literal and before the cache load:

```go
	// The collector's remote pollers fetch off the poll loop. Starting them
	// unconditionally is safe: they are woken only by Snapshot, and a
	// daemon-fed client never calls it, so this client's workers block for the
	// life of the process and spend no gh budget.
	m.collector.Start(ctx)
```

- [ ] **Step 5: Re-run**

Run: `go test -race ./internal/model/ -run 'TestNewStartsTheRemoteWorkers|TestADaemonFedClientSpendsNoGhBudget|ReachesInvalidateEndToEnd' -v`
Expected: all PASS.

- [ ] **Step 6: Full suite and lint**

Run: `make test`
Expected: PASS, every package. Compare against the list you wrote down in Task 2b step 8: everything on it should now be green, and nothing new should be red.

Run: `make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/model/
git commit -m "feat(model): start the collector's remote workers

A self-polling client needs them for the same reason the daemon does.
Unconditional because the workers are woken only by Snapshot, which a
daemon-fed client never calls.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Real-machine verification, docs and handoff

The suite cannot see the thing this change is for. Verify it on the real machine before writing it up.

**Files:**
- Modify: `CLAUDE.md`
- Create: `docs/superpowers/2026-07-31-collector-async-remote-handoff.md`

- [ ] **Step 1: Install and restart the daemon**

```bash
make install
pkill -f 'vigil daemon'
```

A panel already running picks up the new binary through the re-exec check within a minute or two. The daemon does not restart itself - clients will render `daemon outdated` until you kill it, which is what the `pkill` above is for. Per the binary-refresh handoff, **panels running a build older than that feature cannot re-exec at all**; check with `pgrep -laf vigil` and respawn by hand if needed.

- [ ] **Step 2: Measure new-session latency**

Dispatch a story and time how long the new session takes to appear in the panel created inside it. The poll-latency handoff predicted 1.5 to 2 seconds after `ec70904` and never measured it. Record what you actually see, and say plainly that it is a measurement rather than arithmetic.

- [ ] **Step 3: Confirm no cold-start burst**

With at least one merged session present and `auto_cleanup` in whatever state the user's config has it, restart the daemon and watch the log:

```bash
pkill -f 'vigil daemon'
tail -f ~/.local/state/vigil/*.log 2>/dev/null || true
```

Expected: no `notify` hook fires and no cleanup runs for a session that merged before the restart. If one does, `PRPending` is not reaching the detector - check that the daemon's snapshot actually carries `pr_pending` (`nc -U` the socket and read one frame).

- [ ] **Step 4: Confirm the PR column fills**

Watch a panel for one minute. The PR column should be blank for at most a tick after a session appears, then populate and stay populated. A column that blanks and refills repeatedly means the store is being pruned against a stale working set.

- [ ] **Step 5: Update `CLAUDE.md`**

Under **Architecture**, amend the `internal/collect/` line to say that `Snapshot` is local-only and that off-box data is fetched by pollers on their own goroutines.

Under **Key Conventions**, replace the bullet beginning "**`Collector.Snapshot` is synchronous end to end**" (it is no longer true) and add:

- `Collector.Snapshot` does local work only - tmux, bells, git - then reads the PR store and nudges the remote workers. Nothing it does blocks on the network. Every process that calls it must call `Collector.Start(ctx)` once; the daemon also calls `Collector.Wait()` before `Run` returns, so it does not release its flock with a `gh` child alive.
- **The remote pollers have no ticker, and that is load-bearing.** They are woken only by `Snapshot`'s nudge. A daemon-fed client never calls `Snapshot`, so its workers block for the life of the process and spend no `gh` budget - which is what "one daemon means one `gh` rate-limit budget" actually rests on now. Adding a ticker restores per-panel polling for every open panel and only `TestRemoteRunsNothingWithoutANudge` and `TestADaemonFedClientSpendsNoGhBudget` would notice.
- `session.PRPending` means the branch has no entry in the PR store at all, which is not the same as a branch known to have no PR. `transition.Detect` skips a session where `PRPending && PR == nil`: no seed, no event. Without it, async fill turns every daemon start into a burst of `notify` hooks and an `auto_cleanup` run against every already-merged worktree. The `PR == nil` half is there because a client fills the last known PR from `prCache` first.
- `Collector.Invalidate` makes remote entries due by zeroing `fetchedAt` rather than dropping them. Dropping them would re-mark every branch pending, and a pending session is skipped by `Detect`, so a forced refresh would swallow the transition it was pressed to find.
- `Collector.RefreshRemote` runs one pass synchronously and exists so tests do not race a goroutine. Production reaches a pass only through `Start`.

Under **In-flight design work**, replace the poll-latency handoff's entry (item 1) with a line saying it is superseded by this work, and add this handoff as the new first read for phase 5.

- [ ] **Step 6: Write the handoff**

Create `docs/superpowers/2026-07-31-collector-async-remote-handoff.md` following the house shape of the others: what changed, what was verified **and what that does not prove**, what was deferred, and landmines. It must carry at minimum:

- The measured new-session latency from step 2, labelled as measured.
- That the cold-start burst was checked on a real machine with a real merged session, or that it was not.
- **The no-ticker rule**, and that it is the whole basis of the one-budget claim now.
- **The `RefreshRemote` gap**: a nudge that never reached a worker would leave every `RefreshRemote`-driven test green. Only `TestRunStartsTheRemoteWorkers` and `TestNewStartsTheRemoteWorkers` catch it.
- **The pending skip costs at most one `notify` hook per session at daemon start.** If a missing notification is reported right after a restart, this is why.
- **An old panel against a new daemon bursts toasts on daemon start**: it does not know `pr_pending`, so it seeds PR-less and reacts to the fill. Self-limiting via re-exec, except for the first install after this lands, which by construction cannot re-exec anything.
- That `ExecCommander.Run` still has the grandchild-holds-the-pipe defect. This change did not touch it and phase 5 still inherits it.
- What phase 5 now does: add a `poller` in `internal/collect/remote.go`, pass it to `newRemote` in `collect.New`, and read its store from wherever the work queue renders. It does not touch `Snapshot`.

- [ ] **Step 7: Final gate**

```bash
make test && make lint
```
Expected: both PASS.

- [ ] **Step 8: Commit**

```bash
git add CLAUDE.md docs/superpowers/
git commit -m "docs: record the collector async remote work

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 9: Merge**

Use `superpowers:finishing-a-development-branch`. The repo's pattern is a merge commit onto local `main` with a subject naming the branch and its point, e.g. `Merge collector-async-remote: gh no longer blocks publication`.

---

## Notes for the implementer

**The two failure modes this plan is most likely to produce:**

1. **A test that passes with its subject deleted.** Six briefs in the phase 3 plan had this defect. The step that says "run it and confirm it fails" is where you catch it, and the expected failure *message* is given every time. If a test passes before you have written its subject, stop and work out why - do not proceed and do not adjust the assertion to make it fail.

2. **A worker that is never woken.** Every `RefreshRemote`-driven test would stay green. The two tests that catch it are `TestRunStartsTheRemoteWorkers` and `TestNewStartsTheRemoteWorkers`, and both were written to fail first for exactly that reason. Do not weaken either into "assert Start was called".

**`fetch.MockCommander.Calls` is appended under a mutex but read without one.** Task 3 is the first test in the repo to drive one `MockCommander` from two goroutines, so it is where `-race` will say so. Fix it with a `CallCount` accessor on `MockCommander`, not with a mutex in the test.

**If a daemon test in `transition_test.go` starts failing**, the cause is almost certainly that its fixture now yields a git root, so its sessions get tracked and become pending. Check `bellSwitch`'s `cmd.On("git", "", nil)` is intact rather than adding a `RefreshRemote` to paper over it.

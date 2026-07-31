package collect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
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

// TestRemoteRunsNothingWithoutANudge is a smoke check, not a guarantee: its
// 200ms window only catches a ticker faster than 200ms. A ticker at
// pr_interval (30s) or tmux_interval (1s) - the two realistic wrong
// implementations - would sail through it undetected. The real guard against
// those is the daemon-level and client-level tests plus review; this test
// only rules out the fast, obviously-wrong case.
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
//
// The pass count for one nudge does not discriminate here: r.wakes is built
// in newRemote, not in start, so even with the guard removed two workers
// would still be selecting on the same cap-1 channel, and exactly one of them
// would receive one token. This test uses the blocking poller instead: with
// one worker, a nudge that arrives while a pass is running just queues: with
// two, the spare worker is idle and picks it up immediately.
func TestRemoteStartIsIdempotent(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	r := newRemote(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(p.release); r.wait() }()
	r.start(ctx)
	r.start(ctx)

	r.nudge()
	waitForPass(t, p)

	// The first pass is now blocked on release. With one worker this nudge
	// queues; with two, the spare picks it up and enters pass immediately.
	r.nudge()
	select {
	case <-p.entered:
		t.Fatal("a second worker entered pass while the first was still running: start ran twice")
	case <-time.After(200 * time.Millisecond):
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
	defer cancel()
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

// TestRemoteNudgeDoesNotBlockWithNoWorkerDraining pins nudge's non-blocking
// contract directly: nothing has called start, so nothing is ever reading the
// wake channels, and nudge must still return.
func TestRemoteNudgeDoesNotBlockWithNoWorkerDraining(t *testing.T) {
	p := newCountingPoller()
	r := newRemote(p)

	done := make(chan struct{})
	go func() {
		r.nudge()
		r.nudge()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nudge blocked with nothing draining the wake channel")
	}
}

// TestRemoteNudgeDuringAPassCoalescesIntoTheNext pins the comment on nudge:
// a nudge that arrives while a pass is running is not dropped, it queues, and
// the worker picks it up the moment the running pass returns.
func TestRemoteNudgeDuringAPassCoalescesIntoTheNext(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	r := newRemote(p)

	// Releasing from the defer as well as the body keeps a failed waitForPass
	// from hanging: cancel alone cannot free a worker parked on release.
	release := sync.OnceFunc(func() { close(p.release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); release(); r.wait() }()
	r.start(ctx)

	r.nudge()
	waitForPass(t, p)

	r.nudge()
	release()

	waitForPass(t, p)
	if got := p.count(); got != 2 {
		t.Errorf("got %d passes, want 2: a nudge during a pass should produce a second pass", got)
	}
}

// blockingGhCommander answers every "gh" call by blocking on release until it
// is closed, signaling entered the first time it is called so a test never
// has to sleep to know a pass has reached its fetch.
func blockingGhCommander(entered chan struct{}, release chan struct{}, output string) *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"gh": func(context.Context, string, []string) (string, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return output, nil
		},
	}
	return cmd
}

func waitForEntered(t *testing.T, entered chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fetch to start")
	}
}

func waitForDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pass to finish")
	}
}

// TestPassKeepsLastKnownPRAndMovesFetchedAtOnAFailedFetch: a failed fetch must
// not blank the PR column, but it must still count as an attempt, or a
// rate-limited gh gets retried on every nudge instead of waiting out
// PRInterval.
func TestPassKeepsLastKnownPRAndMovesFetchedAtOnAFailedFetch(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("gh", "not json", nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }
	p := newPRPoller(c)

	p.track([]*branchRoot{{key: "k", branch: "feature", gitRoot: "/repo"}})

	prev := &session.PRStatus{Number: 7}
	p.mu.Lock()
	p.entries["k"] = prEntry{pr: prev, fetchedAt: now.Add(-time.Hour)}
	p.mu.Unlock()

	now = now.Add(time.Hour)
	p.pass(context.Background())

	p.mu.Lock()
	entry := p.entries["k"]
	p.mu.Unlock()
	if entry.pr != prev {
		t.Errorf("got PR %+v, want the previous pointer %+v kept after a failed fetch", entry.pr, prev)
	}
	if !entry.fetchedAt.Equal(now) {
		t.Errorf("got fetchedAt %v, want %v: fetchedAt must still move so a failing gh is not retried every nudge", entry.fetchedAt, now)
	}
}

// TestInvalidateZeroesFetchedAtButKeepsTheEntry: invalidate must not blank a
// known PR, only make the branch due again - Detect skips a pending session,
// so dropping the entry would swallow the next transition instead of finding
// it.
func TestInvalidateZeroesFetchedAtButKeepsTheEntry(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	p := newPRPoller(c)

	pr := &session.PRStatus{Number: 9}
	p.mu.Lock()
	p.entries["k"] = prEntry{pr: pr, fetchedAt: c.now()}
	p.mu.Unlock()

	p.invalidate()

	p.mu.Lock()
	entry, ok := p.entries["k"]
	p.mu.Unlock()
	if !ok {
		t.Fatal("invalidate must not drop the entry")
	}
	if entry.pr != pr {
		t.Errorf("got PR %+v, want the same pointer %+v: invalidate must not blank a known PR", entry.pr, pr)
	}
	if !entry.fetchedAt.IsZero() {
		t.Errorf("got fetchedAt %v, want zero", entry.fetchedAt)
	}
}

// TestPassDiscardsAResultForABranchThatVanishedMidFetch: the write-back must
// prune against the working set as it stands after the fetch, not as it
// stood when the fetch started.
func TestPassDiscardsAResultForABranchThatVanishedMidFetch(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := blockingGhCommander(entered, release, `{"number": 42, "state": "MERGED"}`)

	c := New(&config.Config{}, cmd)
	p := newPRPoller(c)
	p.track([]*branchRoot{{key: "a", branch: "feature-a", gitRoot: "/repo"}})

	done := make(chan struct{})
	go func() {
		p.pass(context.Background())
		close(done)
	}()
	waitForEntered(t, entered)

	// The branch vanishes from the working set while its fetch is still in
	// flight - e.g. its session exited.
	p.track(nil)
	close(release)
	waitForDone(t, done)

	p.mu.Lock()
	_, ok := p.entries["a"]
	p.mu.Unlock()
	if ok {
		t.Error("a branch that vanished mid-fetch must not survive into the entries map")
	}
}

// TestPassDoesNotHoldMuAcrossTheFetch: mu guards the maps, not the network
// call. A concurrent track or fill must not wait out a slow gh.
func TestPassDoesNotHoldMuAcrossTheFetch(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := blockingGhCommander(entered, release, `{"number": 1, "state": "MERGED"}`)

	c := New(&config.Config{}, cmd)
	p := newPRPoller(c)
	p.track([]*branchRoot{{key: "a", branch: "feature", gitRoot: "/repo"}})

	done := make(chan struct{})
	go func() {
		p.pass(context.Background())
		close(done)
	}()
	waitForEntered(t, entered)

	trackDone := make(chan struct{})
	go func() {
		p.track([]*branchRoot{{key: "b", branch: "other", gitRoot: "/repo"}})
		close(trackDone)
	}()

	select {
	case <-trackDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("track blocked on mu while a fetch was in flight")
	}

	close(release)
	waitForDone(t, done)
}

// TestPassPrunesEvenWhenNothingWasDue: the old fillPRs rebuilt its memo every
// Snapshot, so a session that disappeared was gone from the memo on the very
// next call. A pass that returns early when nothing is due must not bring
// that regression back by leaving a vanished branch's entry in place until
// the next pass that happens to have work.
func TestPassPrunesEvenWhenNothingWasDue(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	p := newPRPoller(c)

	p.mu.Lock()
	p.entries["gone"] = prEntry{pr: &session.PRStatus{Number: 1}, fetchedAt: c.now()}
	p.mu.Unlock()
	p.track(nil)

	p.pass(context.Background())

	p.mu.Lock()
	_, ok := p.entries["gone"]
	p.mu.Unlock()
	if ok {
		t.Error("a pass with nothing due should still prune entries no longer in the working set")
	}
}

// TestPassDoesNotSatisfyAnInvalidateThatLandsDuringIt: an invalidate that
// lands mid-fetch must leave the branch due, because the answer in flight may
// predate the state change the caller pressed refresh to go and find. The
// entry is seeded already-resolved-but-due (fetchedAt zero), standing in for a
// branch made due by an earlier invalidate, so this pass has real work in
// flight when the second invalidate lands.
func TestPassDoesNotSatisfyAnInvalidateThatLandsDuringIt(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := blockingGhCommander(entered, release, `{"number": 42, "state": "MERGED"}`)

	c := New(&config.Config{}, cmd)
	p := newPRPoller(c)
	p.track([]*branchRoot{{key: "k", branch: "feature", gitRoot: "/repo"}})

	p.mu.Lock()
	p.entries["k"] = prEntry{pr: &session.PRStatus{Number: 1}}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.pass(context.Background())
		close(done)
	}()
	waitForEntered(t, entered)

	p.invalidate()
	close(release)
	waitForDone(t, done)

	p.mu.Lock()
	entry := p.entries["k"]
	p.mu.Unlock()
	if entry.pr == nil || entry.pr.Number != 42 {
		t.Errorf("got PR %+v, want the freshly fetched one even though an invalidate landed mid-fetch", entry.pr)
	}
	if !entry.fetchedAt.IsZero() {
		t.Errorf("got fetchedAt %v, want zero: an invalidate mid-pass must leave the branch due for the next pass", entry.fetchedAt)
	}
}

// TestFillMarksAnUnresolvedBranchPending: a branch with no entry at all has
// never been resolved, which transition.Detect must not treat the same as a
// branch known to have no PR.
func TestFillMarksAnUnresolvedBranchPending(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	p := newPRPoller(c)

	s := &session.Session{Name: "s"}
	br := &branchRoot{key: "k", branch: "feature", gitRoot: "/repo", sessions: []*session.Session{s}}

	p.fill([]*branchRoot{br})

	if !s.PRPending {
		t.Error("a branch with no entry at all should mark its sessions PRPending")
	}
	if s.PR != nil {
		t.Error("a pending session should not have PR populated")
	}
}

// TestFillPopulatesPRFromAResolvedEntry: the mirror case - a resolved branch
// fills PR and is not marked pending.
func TestFillPopulatesPRFromAResolvedEntry(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	p := newPRPoller(c)

	pr := &session.PRStatus{Number: 5}
	p.mu.Lock()
	p.entries["k"] = prEntry{pr: pr}
	p.mu.Unlock()

	s := &session.Session{Name: "s"}
	br := &branchRoot{key: "k", branch: "feature", gitRoot: "/repo", sessions: []*session.Session{s}}

	p.fill([]*branchRoot{br})

	if s.PR != pr {
		t.Errorf("got PR %+v, want %+v", s.PR, pr)
	}
	if s.PRPending {
		t.Error("a resolved branch should not be marked pending")
	}
}

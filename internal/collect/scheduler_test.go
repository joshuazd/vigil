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

func TestSchedulerNudgeWakesEveryWorker(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	s := newScheduler(a, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); s.wait() }()
	s.start(ctx)

	s.nudge()
	waitForPass(t, a)
	waitForPass(t, b)
}

// TestSchedulerRunsNothingWithoutANudge is a smoke check, not a guarantee: its
// 200ms window only catches a ticker faster than 200ms. A ticker at
// pr_interval (30s) or tmux_interval (1s) - the two realistic wrong
// implementations - would sail through it undetected. The real guard against
// those is the daemon-level and client-level tests plus review; this test
// only rules out the fast, obviously-wrong case.
func TestSchedulerRunsNothingWithoutANudge(t *testing.T) {
	p := newCountingPoller()
	s := newScheduler(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); s.wait() }()
	s.start(ctx)

	time.Sleep(200 * time.Millisecond)
	if got := p.count(); got != 0 {
		t.Errorf("got %d passes with no nudge, want 0: the remote layer must have no ticker of its own", got)
	}
}

// TestSchedulerStartIsIdempotent: newModel starts the collector, and a client
// that loses and regains a daemon must not be able to double the fetch rate by
// arriving at start twice.
//
// The pass count for one nudge does not discriminate here: s.wakes is built
// in newScheduler, not in start, so even with the guard removed two workers
// would still be selecting on the same cap-1 channel, and exactly one of them
// would receive one token. This test uses the blocking poller instead: with
// one worker, a nudge that arrives while a pass is running just queues: with
// two, the spare worker is idle and picks it up immediately.
func TestSchedulerStartIsIdempotent(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	s := newScheduler(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(p.release); s.wait() }()
	s.start(ctx)
	s.start(ctx)

	s.nudge()
	waitForPass(t, p)

	// The first pass is now blocked on release. With one worker this nudge
	// queues; with two, the spare picks it up and enters pass immediately.
	s.nudge()
	select {
	case <-p.entered:
		t.Fatal("a second worker entered pass while the first was still running: start ran twice")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSchedulerRefreshRunsEveryPollerSynchronously(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	s := newScheduler(a, b)

	s.refresh(context.Background())

	if got := a.count(); got != 1 {
		t.Errorf("got %d passes on poller a, want 1", got)
	}
	if got := b.count(); got != 1 {
		t.Errorf("got %d passes on poller b, want 1", got)
	}
}

func TestSchedulerInvalidateReachesEveryPoller(t *testing.T) {
	a, b := newCountingPoller(), newCountingPoller()
	s := newScheduler(a, b)

	s.invalidate()

	if got := a.invalidateCount(); got != 1 {
		t.Errorf("got %d invalidates on poller a, want 1", got)
	}
	if got := b.invalidateCount(); got != 1 {
		t.Errorf("got %d invalidates on poller b, want 1", got)
	}
}

// TestSchedulerWaitBlocksUntilAnInFlightPassFinishes is what the daemon's
// shutdown arm depends on. Run must not return - and so must not release its
// flock or unlink its socket - with a gh child still running.
func TestSchedulerWaitBlocksUntilAnInFlightPassFinishes(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	s := newScheduler(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)
	s.nudge()
	waitForPass(t, p)

	cancel()

	returned := make(chan struct{})
	go func() {
		s.wait()
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

// TestSchedulerNudgeDoesNotBlockWithNoWorkerDraining pins nudge's non-blocking
// contract directly: nothing has called start, so nothing is ever reading the
// wake channels, and nudge must still return.
func TestSchedulerNudgeDoesNotBlockWithNoWorkerDraining(t *testing.T) {
	p := newCountingPoller()
	s := newScheduler(p)

	done := make(chan struct{})
	go func() {
		s.nudge()
		s.nudge()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nudge blocked with nothing draining the wake channel")
	}
}

// TestSchedulerNudgeDuringAPassCoalescesIntoTheNext pins the comment on nudge:
// a nudge that arrives while a pass is running is not dropped, it queues, and
// the worker picks it up the moment the running pass returns.
func TestSchedulerNudgeDuringAPassCoalescesIntoTheNext(t *testing.T) {
	p := newCountingPoller()
	p.release = make(chan struct{})
	s := newScheduler(p)

	// Releasing from the defer as well as the body keeps a failed waitForPass
	// from hanging: cancel alone cannot free a worker parked on release.
	release := sync.OnceFunc(func() { close(p.release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); release(); s.wait() }()
	s.start(ctx)

	s.nudge()
	waitForPass(t, p)

	s.nudge()
	release()

	waitForPass(t, p)
	if got := p.count(); got != 2 {
		t.Errorf("got %d passes, want 2: a nudge during a pass should produce a second pass", got)
	}
}

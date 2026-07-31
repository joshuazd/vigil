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

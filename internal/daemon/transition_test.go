package daemon

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/transition"
)

type recordingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (r *recordingEffects) Run(_ context.Context, ev transition.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEffects) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// bellSwitch returns a Commander whose bell flag flips the second time tmux is
// asked, which is a state change from idle to attention.
func bellSwitch() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	var windowCalls int
	var mu sync.Mutex
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux": func(_ context.Context, _ string, args []string) (string, error) {
			if len(args) > 1 && args[1] == "-a" && args[0] == "list-panes" {
				return "1700000000|alpha|/tmp/alpha", nil
			}
			if len(args) > 0 && args[0] == "list-windows" {
				mu.Lock()
				windowCalls++
				n := windowCalls
				mu.Unlock()
				if n == 1 {
					return "alpha|0", nil
				}
				return "alpha|1", nil
			}
			return "", nil
		},
	}
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd
}

// TestPollRunsEffectsOncePerEvent is the point of moving them here: the count
// is a property of the event, not of how many clients happen to be attached.
// It also proves the fixture is capable of producing an effect at all, which
// is what makes TestEffectsDoNotScaleWithClients's "still exactly one" a
// meaningful assertion rather than a vacuous "still zero."
func TestPollRunsEffectsOncePerEvent(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx)
	if got := effects.count(); got != 0 {
		t.Fatalf("got %d effect runs on the priming poll, want 0", got)
	}
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs for one transition, want 1", got)
	}
	if ev := effects.events[0]; ev.Session != "alpha" {
		t.Errorf("got session %q, want alpha", ev.Session)
	}
}

// TestPollWithoutADetectorDoesNotPanic and TestPollWithoutEffectsDoesNotPanic
// each nil only the field they name. A test that nils both, as this used to,
// cannot tell "the guard checks Detector" from "the guard checks Effects" -
// either half of the `||` alone would still pass it. Both fixtures poll
// twice with bellSwitch so a real event reaches the dispatch loop (the
// priming poll never does, regardless of the guard).
func TestPollWithoutADetectorDoesNotPanic(t *testing.T) {
	s := &Server{
		Collector: collect.New(&config.Config{}, bellSwitch()),
		Effects:   &recordingEffects{},
	}
	s.poll(context.Background())
	s.poll(context.Background())
}

func TestPollWithoutEffectsDoesNotPanic(t *testing.T) {
	s := &Server{
		Collector: collect.New(&config.Config{}, bellSwitch()),
		Detector:  transition.NewDetector(),
	}
	s.poll(context.Background())
	s.poll(context.Background())
}

// TestNewWiresDetectorAndEffects pins New's contract: a daemon built the real
// way always has both fields set, and Effects always carries a logger. Every
// other test in this file builds a Server literal directly, so none of them
// would notice New itself failing to wire this - the gap this task exists to
// close is exactly "the daemon never runs the hooks", and that regresses
// silently if New stops setting these up.
func TestNewWiresDetectorAndEffects(t *testing.T) {
	s := New(&config.Config{}, fetch.NewMockCommander())

	if s.Detector == nil {
		t.Error("got nil Detector, want New to wire one so transitions are tracked")
	}
	if s.Effects == nil {
		t.Fatal("got nil Effects, want New to wire one so hooks and auto_cleanup fire")
	}
	runner, ok := s.Effects.(transition.Runner)
	if !ok {
		t.Fatalf("got Effects of type %T, want transition.Runner", s.Effects)
	}
	if runner.Logf == nil {
		t.Error("got nil Logf, want the daemon's logger: it is the only place a headless cleanup failure surfaces")
	}
}

// TestEffectsDoNotScaleWithClients pins the property phase 3 depends on: three
// panels attached to one daemon still produce one effect run per event.
// newBlockingConn (client_test.go) is reused here rather than wiring net.Pipe
// by hand: closing release up front makes its Write a no-op that still
// exercises addClient and the writer goroutine that poll's broadcast drives.
func TestEffectsDoNotScaleWithClients(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	for i := 0; i < 3; i++ {
		conn := newBlockingConn()
		close(conn.release)
		t.Cleanup(func() { _ = conn.Close() })
		s.addClient(conn)
	}

	ctx := context.Background()
	s.poll(ctx)
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs with three clients attached, want 1", got)
	}
	s.closeClients()
}

// blockingEffectRunner signals started, then blocks until release is closed,
// standing in for a notify hook that has not returned yet.
type blockingEffectRunner struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingEffectRunner) Run(context.Context, transition.Event) {
	close(b.started)
	<-b.release
}

// TestRunWaitsForPendingEffects pins the shutdown handshake for effect
// goroutines: Run must not return while a hook or cleanup it started is still
// in flight, the same guarantee TestRunWaitsForWriters pins for writers.
func TestRunWaitsForPendingEffects(t *testing.T) {
	cmd := bellSwitch()
	started := make(chan struct{})
	release := make(chan struct{})
	effects := &blockingEffectRunner{started: started, release: release}

	sockPath := filepath.Join(shortTempDir(t), "test.sock")
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   5 * time.Millisecond,
		SocketPath: sockPath,
		CachePath:  filepath.Join(t.TempDir(), "cache.json"),
		Detector:   transition.NewDetector(),
		Effects:    effects,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	waitForSocket(t, s.SocketPath)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("effect never started")
	}

	cancel()

	select {
	case <-done:
		t.Fatal("Run returned before its in-flight effect finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the effect finished")
	}
}

// toggleState controls the bell flag newToggleCommander reports for each
// session, read fresh on every tmux list-windows call. Unlike bellSwitch's
// fixed idle-then-attention sequence, a test can flip a specific session
// between poll calls to build an exact, multi-step transition sequence
// (including a session returning to a state it already held), which is what
// proving per-session serialization requires.
type toggleState struct {
	mu    sync.Mutex
	bells map[string]bool
}

func (t *toggleState) setBell(name string, on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bells[name] = on
}

func (t *toggleState) bellFlag(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bells[name] {
		return "1"
	}
	return "0"
}

func newToggleCommander(sessions ...string) (*fetch.MockCommander, *toggleState) {
	st := &toggleState{bells: make(map[string]bool)}
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux": func(_ context.Context, _ string, args []string) (string, error) {
			if len(args) > 1 && args[0] == "list-panes" && args[1] == "-a" {
				lines := make([]string, len(sessions))
				for i, name := range sessions {
					lines[i] = fmt.Sprintf("1700000000|%s|/tmp/%s", name, name)
				}
				return strings.Join(lines, "\n"), nil
			}
			if len(args) > 0 && args[0] == "list-windows" {
				lines := make([]string, len(sessions))
				for i, name := range sessions {
					lines[i] = name + "|" + st.bellFlag(name)
				}
				return strings.Join(lines, "\n"), nil
			}
			return "", nil
		},
	}
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd, st
}

// gatingEffects blocks the first Run call for each session named in
// gateFirst until release closes, and lets every other call - including
// later calls for a gated session - return immediately. That models "the
// notify hook (or auto_cleanup) is still running for this one transition",
// which is the exact condition finding 2 requires two DIFFERENT events for
// one session to race against.
type gatingEffects struct {
	mu        sync.Mutex
	gateFirst map[string]bool
	seen      map[string]int
	started   map[string]chan struct{}
	release   chan struct{}
	calls     []string
}

func newGatingEffects(gateFirst ...string) *gatingEffects {
	g := &gatingEffects{
		gateFirst: make(map[string]bool),
		seen:      make(map[string]int),
		started:   make(map[string]chan struct{}),
		release:   make(chan struct{}),
	}
	for _, name := range gateFirst {
		g.gateFirst[name] = true
		g.started[name] = make(chan struct{})
	}
	return g
}

func (g *gatingEffects) Run(_ context.Context, ev transition.Event) {
	g.mu.Lock()
	g.calls = append(g.calls, ev.Session)
	g.seen[ev.Session]++
	shouldGate := g.seen[ev.Session] == 1 && g.gateFirst[ev.Session]
	started := g.started[ev.Session]
	g.mu.Unlock()
	if shouldGate {
		close(started)
		<-g.release
	}
}

func (g *gatingEffects) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *gatingEffects) calledWith() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.calls))
	copy(out, g.calls)
	return out
}

// TestSecondEventForSameSessionIsSkippedWhileFirstRuns pins the fix for
// finding 2: a merged session's bell flipping (done -> attention -> done)
// yields two Done-bound events for the SAME session. Without serialization,
// the second would start a concurrent CleanupSession against the worktree
// the first is still cleaning up. The skip must also be visible in the log -
// a silently dropped destructive action is its own bug.
func TestSecondEventForSameSessionIsSkippedWhileFirstRuns(t *testing.T) {
	cmd, toggle := newToggleCommander("alpha")
	effects := newGatingEffects("alpha")
	var buf syncBuffer
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
		Log:       log.New(&buf, "", 0),
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha idle

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha idle -> attention: dispatches, blocks on release
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("effect never started")
	}

	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> idle: must be SKIPPED, not dispatched

	if got := effects.callCount(); got != 1 {
		t.Fatalf("got %d effect calls, want 1 (the second event for alpha must be skipped while the first runs)", got)
	}
	if !strings.Contains(buf.String(), "alpha") {
		t.Errorf("got log %q, want a line naming the skipped session", buf.String())
	}

	close(effects.release)
	s.pendingEffects.Wait()
}

// TestEffectForDifferentSessionIsNotSkipped is the other direction: the skip
// in poll must be keyed by session, not a blanket "an effect is running
// somewhere" gate that would starve every other session behind one slow hook.
func TestEffectForDifferentSessionIsNotSkipped(t *testing.T) {
	cmd, toggle := newToggleCommander("alpha", "beta")
	effects := newGatingEffects("alpha")
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha idle, beta idle

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha idle -> attention: dispatches, blocks on release
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("alpha effect never started")
	}

	toggle.setBell("beta", true)
	s.poll(ctx) // beta idle -> attention: a different session, must dispatch

	waitForCondition(t, 2*time.Second, func() bool { return effects.callCount() == 2 })
	if got := effects.calledWith(); len(got) != 2 || got[1] != "beta" {
		t.Fatalf("got calls %v, want [alpha beta]", got)
	}

	close(effects.release)
	s.pendingEffects.Wait()
}

// TestEventForSameSessionDispatchesAfterFirstCompletes proves the other half
// of finding 2's requirement: the skip is temporary. A permanent block on a
// session whose effect already finished would be worse than the bug this
// task fixes - it would mean a session that legitimately transitions again
// (e.g. reopens, then goes idle again) never gets its notify hook at all.
func TestEventForSameSessionDispatchesAfterFirstCompletes(t *testing.T) {
	cmd, toggle := newToggleCommander("alpha")
	effects := newGatingEffects("alpha")
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha idle

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha idle -> attention: dispatches (call 1), blocks
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("effect never started")
	}

	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> idle: skipped, call 1 still in flight

	close(effects.release) // let call 1 finish
	s.pendingEffects.Wait()

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha idle -> attention again: call 1 is done and drained,
	// so this must dispatch rather than skip forever

	waitForCondition(t, 2*time.Second, func() bool { return effects.callCount() == 2 })
	if got := effects.calledWith(); len(got) != 2 {
		t.Fatalf("got calls %v, want 2 (a later event for alpha must dispatch once the prior one finished)", got)
	}

	s.pendingEffects.Wait()
}

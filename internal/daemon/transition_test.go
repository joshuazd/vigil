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
	"github.com/jzinkduda/vigil/internal/session"
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

// newDoneToggleCommander is newToggleCommander plus a real (if fake) git
// root/branch per session and a permanently-merged PR, so each session's
// baseline state is session.Done (bell off) rather than Idle, and toggling
// the bell moves it to session.Attention and back. That is the actual shape
// of the bug finding 2 exists for: a merged session whose bell flips goes
// done -> attention -> done, producing two New == session.Done events for
// the same session. Without a real git root, collect.Collector's
// groupByBranchRoot drops the session before it ever reaches gh, and PR
// (and so State()) stays nil/idle forever.
func newDoneToggleCommander(sessions ...string) (*fetch.MockCommander, *toggleState) {
	cmd, st := newToggleCommander(sessions...)
	cmd.HandlerFuncs["git"] = func(_ context.Context, dir string, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
			return dir, nil // the pane path doubles as this fake session's git root
		}
		if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
			return "branch-" + filepath.Base(dir), nil
		}
		return "", nil
	}
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		return `{"number":1,"state":"MERGED","isDraft":false,"url":"","title":"","body":"",` +
			`"statusCheckRollup":[],"reviewDecision":"","latestReviews":[],"mergeable":"","reviewRequests":[]}`, nil
	}
	return cmd, st
}

// doneGate blocks the first New == session.Done Run call for each session
// named in gateFirst until release closes; every other call - a non-Done
// transition for that same session, a later Done for it, or any call for a
// session not named - returns immediately. This is deliberately narrower
// than the old gatingEffects it replaces: finding 1 established that only
// the Done-bound (cleanup-eligible) dispatch is meant to serialize, so the
// fixture that proves it must be able to tell Done apart from everything
// else, not just gate "the first call for this session."
type doneGate struct {
	mu        sync.Mutex
	gateFirst map[string]bool
	blocked   map[string]bool
	started   map[string]chan struct{}
	release   chan struct{}
	events    []transition.Event
}

func newDoneGate(gateFirst ...string) *doneGate {
	g := &doneGate{
		gateFirst: make(map[string]bool),
		blocked:   make(map[string]bool),
		started:   make(map[string]chan struct{}),
		release:   make(chan struct{}),
	}
	for _, name := range gateFirst {
		g.gateFirst[name] = true
		g.started[name] = make(chan struct{})
	}
	return g
}

func (g *doneGate) Run(_ context.Context, ev transition.Event) {
	g.mu.Lock()
	g.events = append(g.events, ev)
	block := ev.New == session.Done && g.gateFirst[ev.Session] && !g.blocked[ev.Session]
	if block {
		g.blocked[ev.Session] = true
	}
	started := g.started[ev.Session]
	g.mu.Unlock()
	if block {
		close(started)
		<-g.release
	}
}

func (g *doneGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.events)
}

func (g *doneGate) doneCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, ev := range g.events {
		if ev.New == session.Done {
			n++
		}
	}
	return n
}

// TestRepeatDoneForSameSessionIsSkippedWhileFirstCleanupRuns pins finding 2's
// hazard directly: a merged session's bell flipping (done -> attention ->
// done -> attention -> done) yields two New == session.Done events for the
// SAME session. Without serialization, the second would start a concurrent
// CleanupSession against the worktree the first is still cleaning up. The
// skip must also be visible in the log - a silently dropped destructive
// action is its own bug. This is also the fixture for the "gate NO events"
// mutation: without the gate, the repeat Done reaches the fixture too and
// doneCount becomes 2.
func TestRepeatDoneForSameSessionIsSkippedWhileFirstCleanupRuns(t *testing.T) {
	cmd, toggle := newDoneToggleCommander("alpha")
	effects := newDoneGate("alpha")
	var buf syncBuffer
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
		Log:       log.New(&buf, "", 0),
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha done

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha done -> attention: non-Done, dispatches
	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> done: Done-bound, dispatches and blocks
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup-eligible effect never started")
	}

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha done -> attention: non-Done, dispatches despite the
	// Done cleanup above still being in flight

	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> done AGAIN: must be SKIPPED, the prior
	// Done for this session has not finished

	waitForCondition(t, 2*time.Second, func() bool { return effects.count() == 3 })
	if got := effects.count(); got != 3 {
		t.Fatalf("got %d effect calls, want 3 (the repeat Done must be the only one skipped)", got)
	}
	if got := effects.doneCount(); got != 1 {
		t.Fatalf("got %d Done-bound dispatches, want 1 (two concurrent cleanups against one worktree is the bug this pins)", got)
	}
	if !strings.Contains(buf.String(), "alpha") {
		t.Errorf("got log %q, want a line naming the skipped session", buf.String())
	}

	close(effects.release)
	s.pendingEffects.Wait()
}

// TestDoneForDifferentSessionIsNotSkipped is the other direction: the gate
// must be keyed by session, not a blanket "a cleanup is running somewhere"
// lock that would starve every other session's cleanup behind one slow one.
func TestDoneForDifferentSessionIsNotSkipped(t *testing.T) {
	cmd, toggle := newDoneToggleCommander("alpha", "beta")
	effects := newDoneGate("alpha")
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha done, beta done

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha done -> attention: dispatches
	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> done: Done-bound, dispatches and blocks
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("alpha cleanup-eligible effect never started")
	}

	toggle.setBell("beta", true)
	s.poll(ctx) // beta done -> attention: dispatches
	toggle.setBell("beta", false)
	s.poll(ctx) // beta attention -> done: a DIFFERENT session's Done event,
	// must dispatch despite alpha's cleanup still being in flight

	waitForCondition(t, 2*time.Second, func() bool { return effects.count() == 4 })
	if got := effects.doneCount(); got != 2 {
		t.Fatalf("got %d Done-bound dispatches, want 2 (alpha's in-flight cleanup must not block beta's)", got)
	}

	close(effects.release)
	s.pendingEffects.Wait()
}

// TestDoneDispatchesAfterPriorCleanupCompletes proves the other half of
// finding 2's requirement: the skip is temporary. A permanent block on a
// session whose cleanup already finished would be worse than the bug this
// task fixes - it would mean a session that legitimately merges again after
// a rebase never gets cleaned up (or notified) at all.
func TestDoneDispatchesAfterPriorCleanupCompletes(t *testing.T) {
	cmd, toggle := newDoneToggleCommander("alpha")
	effects := newDoneGate("alpha")
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha done

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha done -> attention: dispatches
	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> done: Done-bound, dispatches (call 1) and blocks
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup-eligible effect never started")
	}

	close(effects.release) // let call 1 finish
	s.pendingEffects.Wait()

	toggle.setBell("alpha", true)
	s.poll(ctx) // alpha done -> attention: dispatches
	toggle.setBell("alpha", false)
	s.poll(ctx) // alpha attention -> done again: call 1 is done, so this must
	// dispatch (call 2) rather than skip forever

	waitForCondition(t, 2*time.Second, func() bool { return effects.doneCount() == 2 })
	if got := effects.doneCount(); got != 2 {
		t.Fatalf("got %d Done-bound dispatches, want 2 (a later Done for alpha must dispatch once the prior cleanup finished)", got)
	}

	s.pendingEffects.Wait()
}

// TestNonDoneEventsDispatchWhileACleanupIsInFlight pins finding 1's fix
// directly: gating every effect (not just the destructive one) silently
// suppressed the notify hook for every transition on a session while any one
// of its effects was running, breaking CLAUDE.md's requirement that the
// daemon-fed and self-polling paths behave identically - a self-polling
// client has no such gate and fires all three. Only the Done-bound
// (cleanup-eligible) dispatch may serialize; every other transition must
// still dispatch immediately, even with a slow cleanup in flight for the
// very same session.
func TestNonDoneEventsDispatchWhileACleanupIsInFlight(t *testing.T) {
	cmd, toggle := newDoneToggleCommander("alpha")
	effects := newDoneGate("alpha")
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // primes: alpha done

	toggle.setBell("alpha", true)
	s.poll(ctx) // event 1: done -> attention, non-Done, dispatches (the "notify hook")

	toggle.setBell("alpha", false)
	s.poll(ctx) // event 2: attention -> done, Done-bound ("the cleanup"),
	// dispatches and blocks - the slow first effect
	select {
	case <-effects.started["alpha"]:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup-eligible effect never started")
	}

	toggle.setBell("alpha", true)
	s.poll(ctx) // event 3: done -> attention, non-Done, must dispatch despite
	// event 2 still running - this is the parity assertion

	waitForCondition(t, 2*time.Second, func() bool { return effects.count() == 3 })
	if got := effects.count(); got != 3 {
		t.Fatalf("got %d hook invocations, want 3 (one per real transition; the gate must not suppress non-Done events)", got)
	}
	if got := effects.doneCount(); got != 1 {
		t.Fatalf("got %d cleanups, want 1 (only the one Done-bound transition among the three)", got)
	}

	close(effects.release)
	s.pendingEffects.Wait()
}

// TestShutdownTerminatesWithManyInFlightEffects reproduces the reviewer's
// deadlock scenario at well above the discovered threshold (257 distinct
// in-flight sessions filled the old buffered effectDone channel; every
// dispatch goroutine's send then blocked forever with nobody left to drain
// it, pendingEffects.Done() never ran, and Wait() - and so Run's shutdown,
// including against SIGTERM - hung permanently). inFlightEffects is now a
// plain mutex-guarded map with no capacity to overflow, so this must
// terminate regardless of how many sessions are in flight at once.
func TestShutdownTerminatesWithManyInFlightEffects(t *testing.T) {
	const n = 300
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("session-%d", i)
	}
	cmd, toggle := newDoneToggleCommander(names...)
	effects := newDoneGate(names...)

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

	for _, name := range names {
		toggle.setBell(name, true)
	}
	waitForCondition(t, 5*time.Second, func() bool { return effects.count() >= n })

	for _, name := range names {
		toggle.setBell(name, false)
	}
	// All n sessions are now Done-bound and blocked on release, simultaneously.
	waitForCondition(t, 5*time.Second, func() bool { return effects.count() >= 2*n })

	cancel()

	select {
	case <-done:
		t.Fatal("Run returned before its in-flight effects finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(effects.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not terminate with many in-flight effects - the deadlock this test guards against")
	}
}

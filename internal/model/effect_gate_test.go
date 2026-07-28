package model

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/transition"
)

// unresolvedSession is a third non-Done, non-zero state, used to prove a
// chain of ungated transitions all dispatch regardless of the map.
func unresolvedSession(name string) *session.Session {
	s := idleSession(name)
	s.Git = session.GitStatus{Branch: "feature/" + name, GitRoot: "/repo/" + name}
	s.PR = &session.PRStatus{Number: 7, State: "OPEN", Checks: "pass", UnresolvedComments: 2}
	return s
}

// withBell clones a session with HasBell set, so a merged (Done) session can
// be pushed to Attention and back without otherwise touching its PR data -
// session.State checks HasBell first, ahead of the merged check.
func withBell(s *session.Session) *session.Session {
	clone := *s
	clone.HasBell = true
	return &clone
}

// doneEffectCount isolates the Done-bound dispatches among everything
// effects has recorded: a bell bounce dispatches its own (non-Done, ungated)
// effect too, so asserting on the raw total would conflate "the repeat Done
// was suppressed" with "the intermediate Attention transition didn't fire,"
// which is a different property tested separately.
func doneEffectCount(effects *countingEffects) int {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	n := 0
	for _, ev := range effects.events {
		if ev.New == session.Done {
			n++
		}
	}
	return n
}

// doneEffectCountFor narrows doneEffectCount to one session, needed once a
// test has two sessions with a Done effect in flight at the same time.
func doneEffectCountFor(effects *countingEffects, name string) int {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	n := 0
	for _, ev := range effects.events {
		if ev.New == session.Done && ev.Session == name {
			n++
		}
	}
	return n
}

// TestASecondDoneEventDoesNotDispatchWhileTheFirstIsStillInFlight is the
// blocker this round exists to close: a merged session that gets a bell
// oscillates done -> attention -> done, producing two New == Done events. The
// first Done's completion (EffectDoneMsg) is deliberately never delivered
// here, so the second Done must find it still in flight and skip. Driven
// through Update with real SnapshotMsg values, not by calling
// checkStateTransitions directly, so production supplies `local`.
func TestASecondDoneEventDoesNotDispatchWhileTheFirstIsStillInFlight(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	steps := []SnapshotMsg{
		{Sessions: []*session.Session{idleSession("alpha")}, Local: true},
		{Sessions: []*session.Session{doneSession("alpha")}, Local: true},
		{Sessions: []*session.Session{withBell(doneSession("alpha"))}, Local: true},
		{Sessions: []*session.Session{doneSession("alpha")}, Local: true},
	}

	var cmds []tea.Cmd
	for _, msg := range steps {
		next, cmd := m.Update(msg)
		m = next.(Model)
		cmds = append(cmds, cmd)
	}
	drain(tea.Batch(cmds...))

	if got := doneEffectCount(effects); got != 1 {
		t.Fatalf("got %d Done-bound effect runs, want 1 (the repeat Done should have been suppressed while the first was in flight)", got)
	}
}

// TestNonDoneTransitionsDispatchWhileADoneEffectIsInFlight is the other half
// of the gate: only cleanup is destructive enough to serialize, so the notify
// path for every other transition must be unaffected by a Done effect that
// is still running for the very same session.
func TestNonDoneTransitionsDispatchWhileADoneEffectIsInFlight(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	steps := []SnapshotMsg{
		{Sessions: []*session.Session{idleSession("alpha")}, Local: true},
		{Sessions: []*session.Session{doneSession("alpha")}, Local: true},
		{Sessions: []*session.Session{withBell(doneSession("alpha"))}, Local: true},
		{Sessions: []*session.Session{blockedSession("alpha")}, Local: true},
		{Sessions: []*session.Session{unresolvedSession("alpha")}, Local: true},
	}

	var cmds []tea.Cmd
	for _, msg := range steps {
		next, cmd := m.Update(msg)
		m = next.(Model)
		cmds = append(cmds, cmd)
	}
	drain(tea.Batch(cmds...))

	if got := effects.count(); got != 4 {
		t.Fatalf("got %d effect runs, want 4 (1 gated Done dispatch plus 3 ungated non-Done dispatches, none suppressed by the in-flight Done)", got)
	}
	if got := len(m.notifications); got != 4 {
		t.Errorf("got %d notifications, want 4", got)
	}
}

// TestADoneForADifferentSessionIsNotSuppressed proves the map is keyed per
// session: alpha's in-flight cleanup must never block beta's.
func TestADoneForADifferentSessionIsNotSuppressed(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	prime := SnapshotMsg{Sessions: []*session.Session{idleSession("alpha"), idleSession("beta")}, Local: true}
	bothDone := SnapshotMsg{Sessions: []*session.Session{doneSession("alpha"), doneSession("beta")}, Local: true}

	next, cmd := m.Update(prime)
	m = next.(Model)
	drain(cmd)

	_, cmd = m.Update(bothDone)
	drain(cmd)

	if got := effects.count(); got != 2 {
		t.Fatalf("got %d effect runs, want 2 (alpha's in-flight Done must not block beta's)", got)
	}
}

// TestEffectDoneMsgOnlyClearsItsOwnSession is TestADoneForADifferentSessionIsNotSuppressed's
// counterpart: with TWO sessions mid-Done-effect, delivering EffectDoneMsg for
// one must not free the other. A regression that cleared the whole map on any
// completion would reopen the hazard for every other session with a cleanup
// still running.
func TestEffectDoneMsgOnlyClearsItsOwnSession(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	next, cmd := m.Update(SnapshotMsg{Sessions: []*session.Session{idleSession("alpha"), idleSession("beta")}, Local: true})
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{doneSession("alpha"), doneSession("beta")}, Local: true})
	m = next.(Model)
	drain(cmd) // both dispatch once and are left in flight; neither completion is delivered yet

	next, _ = m.Update(EffectDoneMsg{Session: "alpha"})
	m = next.(Model)

	next, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{withBell(doneSession("alpha")), withBell(doneSession("beta"))}, Local: true})
	m = next.(Model)
	drain(cmd)

	_, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{doneSession("alpha"), doneSession("beta")}, Local: true})
	drain(cmd)

	if got := doneEffectCountFor(effects, "alpha"); got != 2 {
		t.Errorf("got %d Done-bound effect runs for alpha (its EffectDoneMsg was delivered), want 2", got)
	}
	if got := doneEffectCountFor(effects, "beta"); got != 1 {
		t.Errorf("got %d Done-bound effect runs for beta (no EffectDoneMsg was delivered for it), want 1 (still gated)", got)
	}
}

// TestADoneDispatchesAgainAfterItsEffectDoneMsgArrives is the most important
// case: a gate that never re-dispatches is worse than the bug it fixes. This
// proves re-dispatch, not just suppression, by running the ACTUAL tea.Cmd
// checkStateTransitions dispatched for the gated Done and feeding its actual
// returned message into Update - not a fabricated EffectDoneMsg - before
// triggering a second Done for the same session. A gated dispatch that
// forgot to return EffectDoneMsg (e.g. returned nil) would otherwise pass a
// version of this test that hands Update its own hand-built completion
// message, while permanently wedging cleanup for that session in production.
func TestADoneDispatchesAgainAfterItsEffectDoneMsgArrives(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	next, cmd := m.Update(SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: true})
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{doneSession("alpha")}, Local: true})
	m = next.(Model)
	msgs := drain(cmd)

	if len(msgs) != 1 {
		t.Fatalf("got %d messages from the gated Done dispatch, want 1", len(msgs))
	}
	done, ok := msgs[0].(EffectDoneMsg)
	if !ok {
		t.Fatalf("the gated dispatch returned %T, want EffectDoneMsg", msgs[0])
	}
	if done.Session != "alpha" {
		t.Errorf("got EffectDoneMsg{Session: %q}, want alpha", done.Session)
	}

	next, _ = m.Update(done)
	m = next.(Model)

	next, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{withBell(doneSession("alpha"))}, Local: true})
	m = next.(Model)
	drain(cmd)

	_, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{doneSession("alpha")}, Local: true})
	drain(cmd)

	if got := doneEffectCount(effects); got != 2 {
		t.Fatalf("got %d Done-bound effect runs, want 2 (the second Done should dispatch once its predecessor's completion was delivered)", got)
	}
}

// TestDaemonFedClientLeavesInFlightEffectsEmpty is the last piece: a
// daemon-fed client dispatches no effects at all, so it must never
// accumulate entries in the map it never needs.
func TestDaemonFedClientLeavesInFlightEffectsEmpty(t *testing.T) {
	effects := &countingEffects{}
	m := fakeDaemonClient(effects)

	prime := SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: false}
	done := SnapshotMsg{Sessions: []*session.Session{doneSession("alpha")}, Local: false}

	next, cmd := m.Update(prime)
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(done)
	m = next.(Model)
	drain(cmd)

	if got := len(m.inFlightEffects); got != 0 {
		t.Errorf("got %d entries in inFlightEffects on a daemon-fed client, want 0", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs on a daemon-fed client, want 0", got)
	}
}

// blockingEffects lets a test hold several effect goroutines open at once,
// deterministically: every Run call blocks until release is closed, so a
// batch of them can be unblocked together instead of relying on a sleep for
// timing.
type blockingEffects struct {
	release chan struct{}
}

func (b *blockingEffects) Run(_ context.Context, _ transition.Event) {
	<-b.release
}

// leafCmds pulls the individual commands out of a tea.Batch's returned
// tea.Cmd without running them, unlike drain: a test that wants to invoke
// them itself, concurrently, needs the un-executed closures, not their
// already-computed results.
func leafCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch
	}
	return []tea.Cmd{func() tea.Msg { return msg }}
}

// TestConcurrentGatedDispatchesDoNotRaceOnTheMap drives go test -race to
// actually observe concurrent access to inFlightEffects, which nothing else
// in this file does: several sessions each get a gated Done dispatch, and
// this test runs every one of the resulting commands concurrently itself,
// exactly as Bubble Tea's own runtime would for a tea.Batch. Under the
// shipped code, nothing inside a dispatched command touches the map - the
// delete only ever happens later, serialized on the update goroutine when
// EffectDoneMsg arrives - so this passes cleanly. It exists to catch a
// regression that moved the delete into the command itself, which would be
// a genuine, silent data race that a suite with no concurrent access could
// never see, -race or not.
func TestConcurrentGatedDispatchesDoNotRaceOnTheMap(t *testing.T) {
	effects := &blockingEffects{release: make(chan struct{})}
	m := transitionModel(effects)

	names := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	idle := make([]*session.Session, len(names))
	done := make([]*session.Session, len(names))
	for i, n := range names {
		idle[i] = idleSession(n)
		done[i] = doneSession(n)
	}

	next, cmd := m.Update(SnapshotMsg{Sessions: idle, Local: true})
	m = next.(Model)
	drain(cmd)

	_, cmd = m.Update(SnapshotMsg{Sessions: done, Local: true})

	cmds := leafCmds(cmd)
	if len(cmds) != len(names) {
		t.Fatalf("got %d gated dispatch commands, want %d (one per session)", len(cmds), len(names))
	}

	var wg sync.WaitGroup
	for _, c := range cmds {
		wg.Add(1)
		go func(c tea.Cmd) {
			defer wg.Done()
			c()
		}(c)
	}
	close(effects.release)
	wg.Wait()
}

package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/session"
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

// TestADoneDispatchesAgainAfterItsEffectDoneMsgArrives is the most important
// case: a gate that never re-dispatches is worse than the bug it fixes. This
// proves re-dispatch, not just suppression, by delivering EffectDoneMsg
// through Update exactly as the real tea.Program loop would before triggering
// a second Done for the same session.
func TestADoneDispatchesAgainAfterItsEffectDoneMsgArrives(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	next, cmd := m.Update(SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: true})
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(SnapshotMsg{Sessions: []*session.Session{doneSession("alpha")}, Local: true})
	m = next.(Model)
	drain(cmd)

	next, _ = m.Update(EffectDoneMsg{Session: "alpha"})
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

package model

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/transition"
)

type countingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (c *countingEffects) Run(_ context.Context, ev transition.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *countingEffects) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// drain runs every command a batch produced, so effects dispatched as tea.Cmds
// have actually executed by the time the assertion reads the counter, and
// returns every leaf message produced along the way (BatchMsg wrappers are
// unwrapped, not returned themselves), so a test can inspect what a specific
// command actually returned instead of assuming its shape.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, drain(c)...)
		}
		return msgs
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func idleSession(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp/" + name}
}

// blockedSession transitions away from idle without landing on the zero value
// of SessionState. Attention is `= iota`, so a zero-valued Event would satisfy
// an assertion of `New == session.Attention` without any detection happening.
func blockedSession(name string) *session.Session {
	s := idleSession(name)
	s.Git = session.GitStatus{Branch: "feature/" + name, GitRoot: "/repo/" + name}
	s.PR = &session.PRStatus{Number: 7, State: "OPEN", Checks: "fail"}
	return s
}

func mergeableSession(name string) *session.Session {
	s := idleSession(name)
	s.Git = session.GitStatus{Branch: "feature/" + name, GitRoot: "/repo/" + name}
	s.PR = &session.PRStatus{Number: 7, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"}
	return s
}

func doneSession(name string) *session.Session {
	s := idleSession(name)
	s.Git = session.GitStatus{Branch: "feature/" + name, GitRoot: "/repo/" + name}
	s.PR = &session.PRStatus{Number: 7, State: "MERGED"}
	return s
}

func transitionModel(effects transition.EffectRunner) Model {
	m := newTestModel()
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "true"}}
	m.detector = transition.NewDetector()
	m.effects = effects
	return m
}

// TestDaemonFedClientsToastButDoNotRunEffects is the blocker, asserted
// directly. Two panels attached to one daemon must produce two toasts, because
// each has its own screen, and zero side effects, because the daemon owns them.
func TestDaemonFedClientsToastButDoNotRunEffects(t *testing.T) {
	effects := &countingEffects{}
	a := transitionModel(effects)
	b := transitionModel(effects)

	for _, m := range []*Model{&a, &b} {
		m.sessions = []*session.Session{idleSession("alpha")}
		m.checkStateTransitions(false)
		m.sessions = []*session.Session{blockedSession("alpha")}
		cmds := m.checkStateTransitions(false)
		drain(tea.Batch(cmds...))
	}

	if got := len(a.notifications); got != 1 {
		t.Errorf("client A: got %d notifications, want 1", got)
	}
	if got := len(b.notifications); got != 1 {
		t.Errorf("client B: got %d notifications, want 1", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs across two daemon-fed clients, want 0", got)
	}
}

// TestASelfPollingClientRunsEffectsOnce is the other half: with no daemon this
// client owns the loop, so it must run them, exactly once.
func TestASelfPollingClientRunsEffectsOnce(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1", got)
	}
	ev := effects.events[0]
	if ev.Session != "alpha" || ev.Old != session.Idle || ev.New != session.Blocked {
		t.Errorf("got %+v, want alpha idle -> blocked", ev)
	}
}

// TestTheFirstSnapshotPrimesTheDetectorWithoutToasting pins the property this
// test actually exercises: Detector.Detect's prev map starts empty, so its
// first call primes silently instead of reporting every already-nonzero
// session as a fresh transition. It is deliberately not relied on to catch a
// gutted checkStateTransitions - its siblings above do that, since a no-op
// checkStateTransitions trivially satisfies "zero notifications, zero
// effects" too.
func TestTheFirstSnapshotPrimesTheDetectorWithoutToasting(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)
	m.sessions = []*session.Session{blockedSession("alpha"), idleSession("beta")}

	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications on the priming snapshot, want 0", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs on the priming snapshot, want 0", got)
	}
}

// fakeDaemonClient builds a model wired like a daemon-fed client: a live
// decoder and a connection that only tolerates the one method handleSnapshot
// calls on it (SetReadDeadline), sharing one EffectRunner across every
// client built this way, exactly as N panels attached to one real daemon
// would share one internal/transition.Runner.
func fakeDaemonClient(effects transition.EffectRunner) Model {
	m := transitionModel(effects)
	m.daemonConn = &fakeConn{}
	m.daemonDecoder = liveDecoder()
	return m
}

// TestDaemonSnapshotsThroughUpdateToastButDoNotRunEffects drives the actual
// production call site (handleSnapshot's daemon branch, via Update) instead
// of calling checkStateTransitions directly, so a regression that hands that
// branch `local: true` fails here rather than only in a test that supplies
// the argument itself. Three daemon-fed clients sharing one EffectRunner is
// the blocker this whole task exists to close: with the bug, three panels
// would produce three hook runs and three concurrent cleanups against one
// worktree instead of zero.
func TestDaemonSnapshotsThroughUpdateToastButDoNotRunEffects(t *testing.T) {
	effects := &countingEffects{}
	clients := []Model{
		fakeDaemonClient(effects),
		fakeDaemonClient(effects),
		fakeDaemonClient(effects),
	}

	prime := SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: false}
	blocked := SnapshotMsg{Sessions: []*session.Session{blockedSession("alpha")}, Local: false}

	for i := range clients {
		next, cmd := clients[i].Update(prime)
		clients[i] = next.(Model)
		drain(cmd)

		next, cmd = clients[i].Update(blocked)
		clients[i] = next.(Model)
		drain(cmd)
	}

	for i, m := range clients {
		if got := len(m.notifications); got != 1 {
			t.Errorf("client %d: got %d notifications, want 1", i, got)
		}
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs across %d daemon-fed clients sharing one runner, want 0", got, len(clients))
	}
}

// TestLocalSnapshotsThroughUpdateRunEffectsOnce is the other real-call-site
// half: a self-polling client's own SnapshotMsg{Local: true} must reach
// Update's handleSnapshot Local branch and run the effect exactly once, with
// the event carrying the fields Runner.Run requires to act (a later task
// wires the real Runner here; a fixture that produced an event with an empty
// Session, PanePath or GitRoot would silently stop exercising cleanup then).
func TestLocalSnapshotsThroughUpdateRunEffectsOnce(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	prime := SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: true}
	blocked := SnapshotMsg{Sessions: []*session.Session{blockedSession("alpha")}, Local: true}

	next, cmd := m.Update(prime)
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(blocked)
	m = next.(Model)
	drain(cmd)

	if got := len(m.notifications); got != 1 {
		t.Errorf("got %d notifications, want 1", got)
	}
	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1", got)
	}
	ev := effects.events[0]
	if ev.Session != "alpha" || ev.Old != session.Idle || ev.New != session.Blocked {
		t.Errorf("got %+v, want alpha idle -> blocked", ev)
	}
	if ev.Session == "" || ev.PanePath == "" || ev.GitRoot == "" {
		t.Errorf("got %+v, want Session, PanePath and GitRoot all non-empty: Runner.Run refuses to clean up otherwise", ev)
	}
}

// TestTransitionNotificationSeverityMatchesTargetState pins notifSeverity's
// mapping, which nothing previously asserted: a Blocked transition must read
// as an error, not a same-looking warning or info toast.
func TestTransitionNotificationSeverityMatchesTargetState(t *testing.T) {
	cases := []struct {
		name    string
		session func(string) *session.Session
		want    string
	}{
		{"blocked", blockedSession, "error"},
		{"mergeable", mergeableSession, "info"},
		{"done", doneSession, "info"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := transitionModel(&countingEffects{})
			m.sessions = []*session.Session{idleSession("alpha")}
			m.checkStateTransitions(false)
			m.sessions = []*session.Session{tc.session("alpha")}
			m.checkStateTransitions(false)

			if len(m.notifications) != 1 {
				t.Fatalf("got %d notifications, want 1", len(m.notifications))
			}
			if got := m.notifications[0].Severity; got != tc.want {
				t.Errorf("got severity %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNotificationsDisabledSuppressesToasts pins the notifications_enabled
// guard around addNotification: disabling it (the setting defaults to
// "true", so this must set it explicitly) must silence toasts entirely,
// not just skip the hook.
func TestNotificationsDisabledSuppressesToasts(t *testing.T) {
	m := transitionModel(&countingEffects{})
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "false"}}
	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(false)
	m.sessions = []*session.Session{blockedSession("alpha")}
	m.checkStateTransitions(false)

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications with notifications_enabled=false, want 0", got)
	}
}

// TestCheckStateTransitionsAutoFocusesTheMostUrgentSession pins the
// auto-focus block: with auto_focus on and outside tmux, the cursor must
// move to the first session in a state that needs attention. lastManualNav's
// zero value already clears the cooldown check trivially (time.Since of the
// zero time is decades), so this needs no time manipulation to reach it.
func TestCheckStateTransitionsAutoFocusesTheMostUrgentSession(t *testing.T) {
	m := transitionModel(&countingEffects{})
	m.cfg = &config.Config{Settings: map[string]any{
		"notifications_enabled": "true",
		"auto_focus":            "true",
	}}
	m.sessions = []*session.Session{idleSession("alpha"), blockedSession("beta")}
	m.cursor = 0

	m.checkStateTransitions(false)

	if m.cursor != 1 {
		t.Errorf("got cursor %d, want 1 (auto-focus on the blocked session)", m.cursor)
	}
}

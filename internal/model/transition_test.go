package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/session"
)

// drain runs every command a batch produced and returns every leaf message
// produced along the way (BatchMsg wrappers are unwrapped, not returned
// themselves), so a test can inspect what a specific command actually returned
// instead of assuming its shape.
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

func transitionModel() Model {
	m := newTestModel()
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "true"}}
	return m
}

// TestEveryClientToastsItsOwnTransitions is the per-client half of asserted
// ownership: two panels watching one session both toast, because each has its
// own screen and its own detector. What they must not do - run the hook or the
// cleanup - is asserted in effect_ownership_test.go, against the Commander.
func TestEveryClientToastsItsOwnTransitions(t *testing.T) {
	a := transitionModel()
	b := transitionModel()

	for _, m := range []*Model{&a, &b} {
		m.sessions = []*session.Session{idleSession("alpha")}
		m.checkStateTransitions()
		m.sessions = []*session.Session{blockedSession("alpha")}
		m.checkStateTransitions()
	}

	if got := len(a.notifications); got != 1 {
		t.Errorf("client A: got %d notifications, want 1", got)
	}
	if got := len(b.notifications); got != 1 {
		t.Errorf("client B: got %d notifications, want 1", got)
	}
}

// TestTheFirstSnapshotPrimesTheDetectorWithoutToasting pins the property this
// test actually exercises: Detector.Detect's prev map starts empty, so its
// first call primes silently instead of reporting every already-nonzero
// session as a fresh transition.
func TestTheFirstSnapshotPrimesTheDetectorWithoutToasting(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{blockedSession("alpha"), idleSession("beta")}

	m.checkStateTransitions()

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications on the priming snapshot, want 0", got)
	}
}

// TestNotificationsDisabledSuppressesToasts pins the notifications_enabled
// guard around addNotification: disabling it (the setting defaults to "true",
// so this must set it explicitly) silences transition toasts entirely. Since
// asserted ownership there is nothing else for the flag to reach - the hook it
// also used to gate is the daemon's, and the daemon reads the flag itself.
func TestNotificationsDisabledSuppressesToasts(t *testing.T) {
	m := transitionModel()
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "false"}}

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions()
	m.sessions = []*session.Session{blockedSession("alpha")}
	m.checkStateTransitions()

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications with notifications_enabled=false, want 0", got)
	}
}

// fakeDaemonClient builds a model wired like a daemon-fed client: a live
// decoder and a connection that only tolerates the one method handleSnapshot
// calls on it (SetReadDeadline).
func fakeDaemonClient() Model {
	m := transitionModel()
	m.daemonConn = &fakeConn{}
	m.daemonDecoder = liveDecoder()
	return m
}

// TestDaemonSnapshotsThroughUpdateToast drives the actual production call site
// (handleSnapshot's daemon branch, via Update) rather than calling
// checkStateTransitions directly.
func TestDaemonSnapshotsThroughUpdateToast(t *testing.T) {
	m := fakeDaemonClient()

	prime := SnapshotMsg{Sessions: []*session.Session{idleSession("alpha")}, Local: false}
	blocked := SnapshotMsg{Sessions: []*session.Session{blockedSession("alpha")}, Local: false}

	next, cmd := m.Update(prime)
	m = next.(Model)
	drain(cmd)

	next, cmd = m.Update(blocked)
	m = next.(Model)
	drain(cmd)

	if got := len(m.notifications); got != 1 {
		t.Errorf("got %d notifications, want 1", got)
	}
}

// TestLocalSnapshotsThroughUpdateToast is the other real-call-site half: a
// self-polling client's own SnapshotMsg{Local: true} must reach
// handleSnapshot's Local branch and toast there too, so falling back to
// self-polling never costs the user their notifications.
func TestLocalSnapshotsThroughUpdateToast(t *testing.T) {
	m := transitionModel()

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
}

// TestTransitionNotificationSeverityMatchesTargetState pins notifSeverity's
// mapping: a Blocked transition must read as an error, not a same-looking
// warning or info toast.
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
			m := transitionModel()
			m.sessions = []*session.Session{idleSession("alpha")}
			m.checkStateTransitions()
			m.sessions = []*session.Session{tc.session("alpha")}
			m.checkStateTransitions()

			if len(m.notifications) != 1 {
				t.Fatalf("got %d notifications, want 1", len(m.notifications))
			}
			if got := m.notifications[0].Severity; got != tc.want {
				t.Errorf("got severity %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCheckStateTransitionsAutoFocusesTheMostUrgentSession pins the
// auto-focus block: with auto_focus on and outside tmux, the cursor must
// move to the first session in a state that needs attention. lastManualNav's
// zero value already clears the cooldown check trivially (time.Since of the
// zero time is decades), so this needs no time manipulation to reach it.
func TestCheckStateTransitionsAutoFocusesTheMostUrgentSession(t *testing.T) {
	m := transitionModel()
	m.cfg = &config.Config{Settings: map[string]any{
		"notifications_enabled": "true",
		"auto_focus":            "true",
	}}
	m.sessions = []*session.Session{idleSession("alpha"), blockedSession("beta")}
	m.cursor = 0

	m.checkStateTransitions()

	if m.cursor != 1 {
		t.Errorf("got cursor %d, want 1 (auto-focus on the blocked session)", m.cursor)
	}
}

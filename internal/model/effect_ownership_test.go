package model

import (
	"net"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/daemon"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
)

// effectCalls returns every subprocess call a transition effect would have
// made. There is no EffectRunner on a Model to inject a double into any more -
// that seam existed only so a client could own effects - so "this client ran
// no effects" has to be asserted against the one thing a client cannot fake:
// the Commander every subprocess in this codebase goes through.
//
// transition.Runner's whole surface is here. The notify and cleanup hooks
// shell out through config.RunHook (`sh -c`), fetch.AttachedSessions is the
// list-sessions call with the session_attached format, and action's builtin
// cleanup kills the session and removes the worktree. A regression that
// reached for transition.Runner directly, with no seam at all, still lands in
// this list.
func effectCalls(t *testing.T, m Model) []fetch.MockCall {
	t.Helper()
	mock, ok := m.cmd.(*fetch.MockCommander)
	if !ok {
		t.Fatalf("test model is not using a MockCommander, got %T", m.cmd)
	}
	var found []fetch.MockCall
	for _, c := range mock.Calls {
		switch {
		case c.Name == "sh":
			found = append(found, c)
		case c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "worktree":
			found = append(found, c)
		case c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session":
			found = append(found, c)
		case c.Name == "tmux" && len(c.Args) > 1 && c.Args[0] == "list-sessions" &&
			c.Args[len(c.Args)-1] == "#{session_name}|#{session_attached}":
			found = append(found, c)
		}
	}
	return found
}

// effectOwnershipModel is a client configured so that both effects WOULD fire
// if anything on this side still ran them: notify enabled (its hook has a
// default, so no hook needs configuring) and auto_cleanup asked for, since it
// is off by default. Everything reaches the one Commander newTestModel builds,
// which is what effectCalls then reads - a fixture that let the effects and the
// model reach two different Commanders would pass every assertion here with a
// client happily running hooks.
func effectOwnershipModel() Model {
	m := newTestModel()
	m.cfg = &config.Config{Settings: map[string]any{
		"notifications_enabled": "true",
		"auto_cleanup":          "true",
	}}
	return m
}

// transitionSteps drives one snapshot per step through Update and drains
// whatever it scheduled, so anything dispatched as a tea.Cmd has actually run
// by the time the assertions read the Commander.
func transitionSteps(m Model, local bool, sessions ...[]*session.Session) Model {
	for _, s := range sessions {
		next, cmd := m.Update(SnapshotMsg{Sessions: s, Epoch: m.epoch, Local: local})
		m = next.(Model)
		drain(cmd)
	}
	return m
}

// withBell clones a session with HasBell set, so a merged (Done) session can
// be pushed to Attention and back without otherwise touching its PR data -
// session.State checks HasBell first, ahead of the merged check.
func withBell(s *session.Session) *session.Session {
	clone := *s
	clone.HasBell = true
	return &clone
}

// idleThenBlockedThenDone is a priming snapshot plus two real transitions: one
// ordinary (which would fire the notify hook) and one Done (which would fire
// notify and auto_cleanup both).
func idleThenBlockedThenDone(name string) [][]*session.Session {
	return [][]*session.Session{
		{idleSession(name)},
		{blockedSession(name)},
		{doneSession(name)},
	}
}

// TestASelfPollingClientRunsNoEffects is the invariant the spawn grace used to
// approximate with a timer. A client with no daemon still detects transitions
// and still toasts them - that is per-client, one screen each - but the notify
// hook and auto_cleanup belong to the daemon and to nothing else, so a
// self-polling client runs zero of them no matter how long it has been
// self-polling.
//
// The toast assertion is load-bearing: without it a gutted
// checkStateTransitions would satisfy "no effects" trivially.
func TestASelfPollingClientRunsNoEffects(t *testing.T) {
	m := effectOwnershipModel()
	if m.daemonConn != nil {
		t.Fatal("the fixture is supposed to be self-polling")
	}

	steps := idleThenBlockedThenDone("alpha")
	m = transitionSteps(m, true, steps...)

	if got := effectCalls(t, m); len(got) != 0 {
		t.Errorf("a self-polling client ran %d transition effects, want 0: %+v", len(got), got)
	}
	if got := len(m.notifications); got != 2 {
		t.Errorf("got %d toasts, want 2 (idle->blocked and blocked->done): the per-client half must survive", got)
	}
}

// TestAClientThatLostItsDaemonRunsNoEffects is the successor to the re-arm
// case the spawn grace could not cover. handleDaemonLost starts self-polling
// immediately, and the daemon it lost may still be alive - a read timeout on a
// slow first snapshot loses nothing but the connection. Under inferred
// ownership this client started running effects the daemon was also running,
// and a Done event meant two CleanupSession calls against one worktree. Under
// asserted ownership there is nothing to race: the client never runs them.
func TestAClientThatLostItsDaemonRunsNoEffects(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })

	m := effectOwnershipModel()
	m.daemonConn = client
	m.daemonDecoder = protocol.NewDecoder(client)

	next, _ := m.Update(DaemonLostMsg{Epoch: m.epoch})
	m = next.(Model)
	if m.daemonConn != nil {
		t.Fatal("handleDaemonLost left the connection installed; this test needs the self-polling state")
	}

	steps := idleThenBlockedThenDone("alpha")
	m = transitionSteps(m, true, steps...)

	if got := effectCalls(t, m); len(got) != 0 {
		t.Errorf("a client that lost its daemon ran %d transition effects, want 0: %+v", len(got), got)
	}
	if got := len(m.notifications); got < 2 {
		t.Errorf("got %d toasts, want at least 2 transition toasts alongside the daemon-lost warning", got)
	}
}

// TestADaemonFedClientRunsNoEffects is the case that was already true before
// asserted ownership and must stay true after it: N panels on one daemon toast
// N times and run nothing.
func TestADaemonFedClientRunsNoEffects(t *testing.T) {
	m := effectOwnershipModel()
	m.daemonConn = &fakeConn{}
	m.daemonDecoder = liveDecoder()

	steps := idleThenBlockedThenDone("alpha")
	m = transitionSteps(m, false, steps...)

	if got := effectCalls(t, m); len(got) != 0 {
		t.Errorf("a daemon-fed client ran %d transition effects, want 0: %+v", len(got), got)
	}
	if got := len(m.notifications); got != 2 {
		t.Errorf("got %d toasts, want 2", got)
	}
}

// TestNewSpawnsTheDaemon is the other half of asserted ownership, and the
// reason it is not simply a deletion: with effects daemon-only, a user running
// the dashboard with panel_auto = false and no daemon would never see the
// notify hook fire again. Both modes spawn now.
func TestNewSpawnsTheDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = daemon.Spawn })

	dir := shortTempDir(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	m := New(&config.Config{}, fetch.NewMockCommander())
	if m.daemonDecoder != nil {
		t.Fatal("New dialed a real daemon; this test needs the no-daemon branch")
	}
	if spawned != 1 {
		t.Errorf("the dashboard spawned %d daemons, want 1: nothing else would ever run the notify hook", spawned)
	}
}

// TestADashboardRespawnsARateLimitedDaemon is TestPanelRespawnsARateLimitedDaemon
// for the dashboard: the failed-probe respawn was panel-only when only panels
// spawned at startup, and a dashboard that lost its daemon would otherwise
// self-poll silently forever with no hooks. The cooldown still applies to it.
func TestADashboardRespawnsARateLimitedDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = daemon.Spawn })

	m := newTestModel()
	m.epoch = 1
	m.lastSpawn = time.Now().Add(-time.Hour)

	got, _ := m.Update(DaemonProbeResultMsg{Epoch: 1})
	if spawned != 1 {
		t.Fatalf("spawned %d daemons after a failed probe on a dashboard, want 1", spawned)
	}
	next := got.(Model)
	if _, _ = next.Update(DaemonProbeResultMsg{Epoch: next.epoch}); spawned != 1 {
		t.Errorf("spawned %d, want the second attempt rate-limited", spawned)
	}
}

// TestNoDoneEffectIsRunTwiceForOneBellBounce is what is left of the
// client-side inFlightEffects gate, asserted where the gate now lives:
// nowhere on this side. A merged session that gets a bell re-enters Done, so
// the client sees two Done events and must still run zero effects for them -
// the daemon's own per-session serialization (internal/daemon) is the only
// thing that has to care about the repeat.
func TestNoDoneEffectIsRunTwiceForOneBellBounce(t *testing.T) {
	m := effectOwnershipModel()

	m = transitionSteps(m, true,
		[]*session.Session{idleSession("alpha")},
		[]*session.Session{doneSession("alpha")},
		[]*session.Session{withBell(doneSession("alpha"))},
		[]*session.Session{doneSession("alpha")},
	)

	if got := effectCalls(t, m); len(got) != 0 {
		t.Errorf("a bell bounce on a merged session ran %d effects on the client, want 0: %+v", len(got), got)
	}
	if got := len(m.notifications); got != 3 {
		t.Errorf("got %d toasts, want 3 (done, attention, done): toasts are ungated on purpose", got)
	}
}

package model

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// TestProbeReconnectsAndRetiresSelfPolling is the whole feature: a client that
// fell back must climb back onto the daemon, and the self-poll loops it
// started must stop when it does.
func TestProbeReconnectsAndRetiresSelfPolling(t *testing.T) {
	m := newTestModel()
	m.epoch = 3
	before := m.epoch

	conn := serveOneSnapshot(t, &protocol.Snapshot{Version: protocol.Version})
	got, cmd := m.Update(DaemonProbeResultMsg{
		Epoch:   before,
		Conn:    conn,
		Decoder: protocol.NewDecoder(conn),
	})
	next := got.(Model)

	if next.daemonConn == nil || next.daemonDecoder == nil {
		t.Fatal("probe result did not install the connection")
	}
	if next.epoch == before {
		t.Error("reconnect did not bump the epoch, so self-poll ticks survive it")
	}
	if next.daemonReady {
		t.Error("daemonReady must stay false until the first snapshot arrives")
	}
	if cmd == nil {
		t.Error("reconnect scheduled nothing: no listen, no render tick")
	}
}

func TestStaleProbeResultClosesTheConnection(t *testing.T) {
	m := newTestModel()
	m.epoch = 4

	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })

	got, _ := m.Update(DaemonProbeResultMsg{
		Epoch:   3,
		Conn:    client,
		Decoder: protocol.NewDecoder(client),
	})
	if got.(Model).daemonConn != nil {
		t.Fatal("a probe result from a retired epoch was installed")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the discarded connection was left open")
	}
}

func TestProbeResultIgnoredWhileConnected(t *testing.T) {
	m := newTestModel()
	m.epoch = 1
	m.daemonConn = &fakeConn{}

	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })

	got, _ := m.Update(DaemonProbeResultMsg{
		Epoch:   1,
		Conn:    client,
		Decoder: protocol.NewDecoder(client),
	})
	if got.(Model).daemonConn != m.daemonConn {
		t.Error("a second connection replaced a live one")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the surplus connection was left open")
	}
}

func TestFailedProbeReschedulesItself(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	_, cmd := m.Update(DaemonProbeResultMsg{Epoch: 2})
	if cmd == nil {
		t.Fatal("a failed probe stopped probing")
	}
}

func TestStaleProbeTickDoesNotProbe(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	if _, cmd := m.Update(ProbeTickMsg{Epoch: 1}); cmd != nil {
		t.Error("a probe tick from a retired epoch kept probing")
	}
}

func TestProbeTickStopsOnceConnected(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	m.daemonConn = &fakeConn{}
	if _, cmd := m.Update(ProbeTickMsg{Epoch: 2}); cmd != nil {
		t.Error("still probing while connected")
	}
}

func TestDialDaemonCmdReportsFailureWithoutAConn(t *testing.T) {
	cmd := dialDaemonCmd(filepath.Join(shortTempDir(t), "nope.sock"), 5)
	msg, ok := cmd().(DaemonProbeResultMsg)
	if !ok {
		t.Fatalf("got %T, want DaemonProbeResultMsg", msg)
	}
	if msg.Conn != nil {
		t.Error("a failed dial reported a connection")
	}
	if msg.Epoch != 5 {
		t.Errorf("got epoch %d, want 5", msg.Epoch)
	}
}

// --- health ---

func TestHealthEmptyWhenFresh(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}
	m.daemonReady = true
	m.lastSnapshot = time.Now()
	if got := m.daemonHealth(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestHealthReportsStaleDaemon covers the freeze this task exists to make
// visible: connected, ready, and silent for far longer than a poll interval.
func TestHealthReportsStaleDaemon(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}
	m.daemonReady = true
	m.lastSnapshot = time.Now().Add(-42 * time.Second)
	if got := m.daemonHealth(); got != "daemon stale 42s" {
		t.Errorf("got %q, want %q", got, "daemon stale 42s")
	}
}

func TestHealthEmptyWhenSelfPollingInTheTUI(t *testing.T) {
	m := newTestModel()
	if got := m.daemonHealth(); got != "" {
		t.Errorf("got %q, want empty: the TUI treats self-polling as normal", got)
	}
}

func TestStaleAfterFloorsAtFiveSeconds(t *testing.T) {
	t.Setenv("VIGIL_TMUX_INTERVAL", "1") // pinned: env beats config in GetSetting
	m := newTestModel()
	if got := m.staleAfter(); got != 5*time.Second {
		t.Errorf("got %s, want 5s", got)
	}
}

func TestStaleAfterTracksTmuxInterval(t *testing.T) {
	t.Setenv("VIGIL_TMUX_INTERVAL", "10")
	m := newTestModel()
	if got := m.staleAfter(); got != 30*time.Second {
		t.Errorf("got %s, want 30s (3x tmux_interval)", got)
	}
}

func TestSnapshotStampsLastSnapshot(t *testing.T) {
	m := newTestModel()
	m.epoch = 1
	got, _ := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 1})
	if got.(Model).lastSnapshot.IsZero() {
		t.Error("a snapshot did not stamp lastSnapshot, so staleness can never be detected")
	}
}

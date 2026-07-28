package model

import (
	"net"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/protocol"
)

// liveDecoder gives a test a non-nil daemonDecoder without a real daemon on
// the other end. The render tick's guard is `stale-epoch || decoder == nil`;
// with a nil decoder the second disjunct alone forces the nil-cmd outcome
// regardless of epoch, so any test of the epoch half needs a live decoder to
// isolate it.
func liveDecoder() *protocol.Decoder {
	return protocol.NewDecoder(strings.NewReader(""))
}

// TestStaleTicksDoNotReschedule covers the daemon path's render heartbeat. A
// message born in an earlier epoch must die rather than reschedule itself,
// or a mode switch leaves two independent pollers running for the life of
// the process.
//
// A stale-epoch local SnapshotMsg is deliberately NOT part of this table: it
// is the one case that must produce a command anyway, to restart a self-poll
// loop a stale straggler was the last thing holding open. See
// TestStaleLocalSnapshotRestartsTheLoopWhenSelfPolling and
// TestStaleLocalSnapshotDoesNothingWhenDaemonConnected.
func TestStaleTicksDoNotReschedule(t *testing.T) {
	cases := []struct {
		name  string
		msg   tea.Msg
		setup func(*Model)
	}{
		// RenderTickMsg is also gated on daemonDecoder == nil, so this case
		// must set a live decoder: otherwise a nil decoder alone would force
		// cmd == nil and the epoch check would never be exercised.
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 0}, func(m *Model) {
			m.daemonDecoder = liveDecoder()
		}},
		// CollectTickMsg's epoch check is not redundant with startPoll's own
		// guards: losing it would double the poll chain on every reconnect,
		// the exact class of bug the epoch mechanism exists to prevent.
		{"collect tick", CollectTickMsg{Epoch: 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 1
			if tc.setup != nil {
				tc.setup(&m)
			}
			if _, cmd := m.Update(tc.msg); cmd != nil {
				t.Error("a stale tick produced a command")
			}
		})
	}
}

// TestStaleLocalSnapshotRestartsTheLoopWhenSelfPolling pins the fix for the
// wedge a stale straggler would otherwise leave: pollInFlight tracks a
// running goroutine, not a generation, so it is always cleared when that
// goroutine's result lands, even from a retired epoch. If this client is
// self-polling in the current generation, that straggler landing is the only
// thing that can ever flip pollInFlight back to false and unblock startPoll,
// so this is where the loop has to restart.
func TestStaleLocalSnapshotRestartsTheLoopWhenSelfPolling(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.epoch = 1
	m.pollInFlight = true

	next, got := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 0, Local: true})
	if got == nil {
		t.Fatal("a stale local snapshot restarted nothing while self-polling; the fallback is wedged")
	}
	if next.(Model).pollInFlight != true {
		t.Error("restarting the loop should mark a new poll in flight")
	}
	msg, ok := got().(SnapshotMsg)
	if !ok || !msg.Local || msg.Epoch != 1 {
		t.Fatalf("got %+v, want a fresh local poll for the current epoch (1)", msg)
	}
}

// TestStaleLocalSnapshotDoesNothingWhenDaemonConnected is the other half:
// once a daemon has taken over, a straggler from the self-poll loop it
// replaced must not restart anything - startPoll's own daemon check makes
// this a no-op rather than a special case here.
func TestStaleLocalSnapshotDoesNothingWhenDaemonConnected(t *testing.T) {
	m := newTestModel()
	m.epoch = 1
	m.pollInFlight = true
	m.daemonConn = &fakeConn{}

	_, got := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 0, Local: true})
	if got != nil {
		t.Error("a stale local snapshot restarted self-polling after a daemon took over")
	}
}

func TestCurrentTicksReschedule(t *testing.T) {
	cases := []struct {
		name  string
		msg   tea.Msg
		setup func(*Model)
	}{
		{"local snapshot", SnapshotMsg{Sessions: fixtureSessions(), Epoch: 7, Local: true}, nil},
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 7}, func(m *Model) {
			m.daemonDecoder = liveDecoder()
		}},
		{"collect tick", CollectTickMsg{Epoch: 7}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 7
			if tc.setup != nil {
				tc.setup(&m)
			}
			if _, cmd := m.Update(tc.msg); cmd == nil {
				t.Error("a current tick produced no command")
			}
		})
	}
}

// TestStaleSnapshotIsIgnored covers a stale-epoch daemon (non-local)
// snapshot. It sets a live decoder so the epoch check is what produces the
// outcome, not the separate nil-connection guard the daemon branch also has:
// without a live connection here, both guards would return the same result
// for the same reason handleSnapshot has each, and the test could not tell
// which one it was actually pinning.
func TestStaleSnapshotIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	m.sessions = nil
	m.daemonDecoder = liveDecoder()
	got, _ := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 1})
	if len(got.(Model).sessions) != 0 {
		t.Error("a snapshot from a closed connection was applied")
	}
}

// TestStaleDaemonLostIsIgnored covers the case a real reconnect produces: a
// stale-epoch DaemonLostMsg arriving while a *live* daemon connection is
// still set. handleDaemonLost's pre-existing nil-conn guard already returns
// (m, nil) with no notification when daemonConn/daemonDecoder are both nil,
// so a fixture that leaves them nil can't tell the epoch check apart from
// that guard. Using net.Pipe for a real, non-nil connection means the only
// thing that can produce the asserted outcome (no command, no notification,
// connection left untouched) is the epoch check.
func TestStaleDaemonLostIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2

	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	m.daemonConn = conn
	m.daemonDecoder = protocol.NewDecoder(conn)

	got, cmd := m.Update(DaemonLostMsg{Epoch: 1})
	if cmd != nil {
		t.Error("a stale daemon-lost restarted the fallback poll loops")
	}
	m2 := got.(Model)
	if len(m2.notifications) != 0 {
		t.Error("a stale daemon-lost notified the user")
	}
	if m2.daemonConn != conn {
		t.Error("a stale daemon-lost closed or replaced the live daemon connection")
	}
	if m2.daemonDecoder == nil {
		t.Error("a stale daemon-lost cleared the live daemon decoder")
	}
}

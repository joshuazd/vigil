package model

import (
	"net"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// staleTickCases covers every self-rescheduling tick. A tick born in an
// earlier epoch must die rather than reschedule itself, or a mode switch
// leaves two independent tickers running for the life of the process.
func TestStaleTicksDoNotReschedule(t *testing.T) {
	cases := []struct {
		name  string
		msg   tea.Msg
		setup func(*Model)
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 0}, nil},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 0}, nil},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 0}, nil},
		// RenderTickMsg is also gated on daemonDecoder == nil, so this case
		// must set a live decoder: otherwise a nil decoder alone would force
		// cmd == nil and the epoch check would never be exercised.
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 0}, func(m *Model) {
			m.daemonDecoder = liveDecoder()
		}},
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

func TestCurrentTicksReschedule(t *testing.T) {
	cases := []struct {
		name  string
		msg   tea.Msg
		setup func(*Model)
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 7}, nil},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 7}, nil},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 7}, nil},
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 7}, func(m *Model) {
			m.daemonDecoder = liveDecoder()
		}},
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

func TestStaleSnapshotIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	m.sessions = nil
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

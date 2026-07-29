package model

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// setProbeInterval shortens the reconnect probe so a test can wait for a tick
// to fire rather than for the real 2s.
func setProbeInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := daemonProbeInterval
	daemonProbeInterval = d
	t.Cleanup(func() { daemonProbeInterval = orig })
}

// probeScheduled reports whether cmd is, or wraps, the reconnect probe.
// tea.Batch collapses its arguments into one opaque command whose only
// observable output is a tea.BatchMsg listing the commands it wrapped, so the
// only way to identify a probe among them is to run them all and look at what
// comes back. They run concurrently because the batch can also carry a poll.
//
// cmd is invoked exactly once here, and each command it might unwrap to is
// invoked exactly once more. tea.Batch's own compaction (see
// bubbletea's compactCmds) means a batch of exactly one non-nil command is
// never actually wrapped - Batch just returns that command directly - so a
// naive "peek at cmd(), then also run whatever cmds ended up holding" risks
// invoking that single command twice. For a one-shot tea.Tick (which every
// caller of this helper passes, directly or inside a batch), a second
// invocation reads from an already-drained timer channel and blocks forever,
// which would silently misreport "no probe scheduled" after the deadline
// below rather than correctly finding the one command that was scheduled.
func probeScheduled(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		_, isProbe := msg.(ProbeTickMsg)
		return isProbe
	}

	msgs := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		if c == nil {
			continue
		}
		go func(c tea.Cmd) { msgs <- c() }(c)
	}

	deadline := time.After(2 * time.Second)
	for range batch {
		select {
		case msg := <-msgs:
			if _, ok := msg.(ProbeTickMsg); ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
	return false
}

// assertPipeClosed proves the model closed a connection it decided not to
// keep, by writing to the other end of a net.Pipe.
//
// The deadline is load-bearing, not defensive. A net.Pipe is unbuffered, so if
// the close is ever dropped from production this write has no reader and
// blocks forever: the test would hang the package until the go test timeout
// killed it with no message, instead of failing. Asserting io.ErrClosedPipe
// rather than "some error" is what keeps the timeout from being mistaken for
// the closed connection the test is looking for.
func assertPipeClosed(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	// Deliberately unchecked: on an already-closed pipe - the passing case -
	// this fails with the very error the write below is looking for.
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("%s: write returned %v, want io.ErrClosedPipe", msg, err)
	}
}

// TestDaemonLostStartsProbing is the headline of the reconnect work: before
// it, falling back to self-polling was one-way and permanent, so one daemon
// restart left every panel polling gh on its own for the life of the process.
// TestFailedProbeReschedulesItself pins that the probe chain keeps going once
// it has started; this pins that losing the daemon starts it at all.
func TestDaemonLostStartsProbing(t *testing.T) {
	setProbeInterval(t, 10*time.Millisecond)

	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })

	m := newTestModel()
	m.epoch = 2
	m.daemonConn = client
	m.daemonDecoder = protocol.NewDecoder(client)

	_, cmd := m.Update(DaemonLostMsg{Epoch: 2})
	if !probeScheduled(t, cmd) {
		t.Fatal("losing the daemon scheduled no probe: the fallback is permanent")
	}
}

// TestInitStartsProbingWhenThereIsNoDaemon covers the other entry into
// self-polling: a client that never reached a daemon at startup must still
// keep trying, or a panel opened before the daemon comes up polls forever.
func TestInitStartsProbingWhenThereIsNoDaemon(t *testing.T) {
	setProbeInterval(t, 10*time.Millisecond)

	m := newTestModel()
	if m.daemonDecoder != nil {
		t.Fatal("newTestModel is supposed to start with no daemon")
	}
	if !probeScheduled(t, m.Init()) {
		t.Fatal("Init scheduled no probe with no daemon reachable")
	}
}

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
	assertPipeClosed(t, client, "the discarded connection was left open")
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
	assertPipeClosed(t, client, "the surplus connection was left open")
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

// TestHealthEmptyWhileConnectingNotYetReady covers a gap the other health
// tests leave open: a freshly (re)connected daemonConn with daemonReady still
// false and lastSnapshot at its zero value. Without the !m.daemonReady guard,
// time.Since(zero value) is decades, which is far past any staleAfter
// threshold, so daemonHealth would report the connecting daemon as stale
// before it ever gets a chance to send a first snapshot.
func TestHealthEmptyWhileConnectingNotYetReady(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}
	if got := m.daemonHealth(); got != "" {
		t.Errorf("got %q, want empty while waiting for the first snapshot", got)
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
	m.daemonDecoder = liveDecoder()
	got, _ := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 1})
	if got.(Model).lastSnapshot.IsZero() {
		t.Error("a snapshot did not stamp lastSnapshot, so staleness can never be detected")
	}
}

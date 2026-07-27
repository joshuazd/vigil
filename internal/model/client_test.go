package model

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
)

func newTestModel() Model {
	return Model{
		gitCache:   make(map[string]session.GitStatus),
		prCache:    make(map[string]*session.PRStatus),
		prevStates: make(map[string]session.SessionState),
		selected:   make(map[string]bool),
		cfg:        &config.Config{},
		cmd:        fetch.NewMockCommander(),
		ctx:        context.Background(),
	}
}

// fakeConn is a net.Conn that only implements SetReadDeadline, recording
// every call. It embeds a nil net.Conn, so any other method panics if
// exercised; handleSnapshot must never call anything but SetReadDeadline.
type fakeConn struct {
	net.Conn
	deadlines []time.Time
}

func (f *fakeConn) SetReadDeadline(t time.Time) error {
	f.deadlines = append(f.deadlines, t)
	return nil
}

// shortTempDir mirrors internal/daemon's helper of the same name: unix
// socket paths are capped at 104 bytes (sockaddr_un.sun_path) on
// macOS/BSD, and t.TempDir() embeds the full test name plus a counter,
// which routinely blows past that, so the socket lives under a short,
// fixed directory instead.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vigil-model-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveOneSnapshot(t *testing.T, snap *protocol.Snapshot) net.Conn {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "test.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if snap != nil {
			_ = protocol.Encode(conn, snap)
			time.Sleep(300 * time.Millisecond)
		}
	}()

	conn, err := dialDaemon(path)
	if err != nil {
		t.Fatalf("dialDaemon: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestDialDaemonFailsWhenAbsent(t *testing.T) {
	if _, err := dialDaemon(filepath.Join(shortTempDir(t), "nope.sock")); err == nil {
		t.Fatal("want an error dialing a nonexistent socket")
	}
}

func TestListenDaemonEmitsSnapshotMsg(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version: protocol.Version,
		Sessions: []*session.Session{
			{Name: "alpha", Git: session.GitStatus{Branch: "main", Modified: 3}},
		},
	})

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg", msg)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if snap.Sessions[0].Git.Modified != 3 {
		t.Errorf("got %d modified, want 3: git state must survive the client",
			snap.Sessions[0].Git.Modified)
	}
}

func TestListenDaemonResolvesPerClientFlags(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version: protocol.Version,
		Sessions: []*session.Session{
			{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"},
		},
	})

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cmd.OnArgs("tmux display-message -p #{client_last_session}", "gamma", nil)

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	snap := msg.(SnapshotMsg)

	byName := map[string]*session.Session{}
	for _, s := range snap.Sessions {
		byName[s.Name] = s
	}
	if !byName["beta"].IsCurrent {
		t.Error("beta should be marked current")
	}
	if byName["alpha"].IsCurrent {
		t.Error("alpha should not be marked current")
	}
	if !byName["gamma"].IsLast {
		t.Error("gamma should be marked last")
	}
}

func TestListenDaemonClearsLastWhenSessionGone(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version:  protocol.Version,
		Sessions: []*session.Session{{Name: "alpha"}},
	})

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.OnArgs("tmux display-message -p #{client_last_session}", "vanished", nil)

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	snap := msg.(SnapshotMsg)
	if snap.Sessions[0].IsLast {
		t.Error("no session should be marked last when the last session is gone")
	}
}

func TestListenDaemonFallsBackToKnownCurrent(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version:  protocol.Version,
		Sessions: []*session.Session{{Name: "alpha"}},
	})

	// MockCommander returns "" for unregistered commands, standing in for
	// running outside tmux where display-message yields nothing.
	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "alpha")()
	snap := msg.(SnapshotMsg)
	if !snap.Sessions[0].IsCurrent {
		t.Error("should fall back to the current session detected at startup")
	}
}

func TestListenDaemonEmitsDaemonLostOnClose(t *testing.T) {
	conn := serveOneSnapshot(t, nil)

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	if _, ok := msg.(DaemonLostMsg); !ok {
		t.Fatalf("got %T, want DaemonLostMsg", msg)
	}
}

// TestHandleSnapshotClearsFirstSnapshotDeadline verifies the other half of
// the timeout fix: once a snapshot actually arrives, handleSnapshot must
// clear the read deadline New() set, or the connection would time out again
// during the next idle gap between poll cycles and a healthy daemon would
// be dropped. It must only do this once (on the first snapshot).
func TestHandleSnapshotClearsFirstSnapshotDeadline(t *testing.T) {
	conn := &fakeConn{}
	m := newTestModel()
	m.daemonConn = conn

	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{{Name: "alpha"}}})
	m2 := next.(Model)

	if !m2.daemonReady {
		t.Error("daemonReady should be true after the first snapshot")
	}
	if len(conn.deadlines) != 1 {
		t.Fatalf("want exactly one SetReadDeadline call, got %d", len(conn.deadlines))
	}
	if !conn.deadlines[0].IsZero() {
		t.Errorf("want deadline cleared to the zero time, got %v", conn.deadlines[0])
	}

	next, _ = m2.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{{Name: "alpha"}}})
	m3 := next.(Model)
	if len(conn.deadlines) != 1 {
		t.Errorf("deadline should only be touched once, got %d calls", len(conn.deadlines))
	}
	_ = m3
}

// TestHandleSnapshotNilConnDoesNotPanic covers the nil-guard: daemonConn can
// be nil (e.g. in tests, or hypothetically if wiring ever changes), and
// handleSnapshot must not dereference it unconditionally.
func TestHandleSnapshotNilConnDoesNotPanic(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{{Name: "alpha"}}})
	m2 := next.(Model)
	if !m2.daemonReady {
		t.Error("daemonReady should be true after the first snapshot even with a nil conn")
	}
}

// TestHandleDaemonLostClosesConnection verifies the daemon connection is
// closed and cleared, so a lost daemon cannot leak the socket fd and
// listenDaemonCmd is not reissued after Update returns.
func TestHandleDaemonLostClosesConnection(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version:  protocol.Version,
		Sessions: []*session.Session{{Name: "alpha"}},
	})
	m := newTestModel()
	m.daemonConn = conn
	m.daemonDecoder = protocol.NewDecoder(conn)
	m.daemonReady = true

	next, _ := m.handleDaemonLost()
	m2 := next.(Model)

	if m2.daemonConn != nil {
		t.Error("daemonConn should be nil after the daemon is lost")
	}
	if m2.daemonDecoder != nil {
		t.Error("daemonDecoder should be nil after the daemon is lost")
	}
	if m2.daemonReady {
		t.Error("daemonReady should be false after the daemon is lost")
	}
	if err := conn.Close(); err == nil {
		t.Error("connection should already be closed by handleDaemonLost")
	}
}

// TestListenDaemonEmitsDaemonLostOnReadTimeout covers the fix for a daemon
// whose first poll failed: it has nothing to send, so a client that dials
// successfully would otherwise block in Next() forever. New sets a read
// deadline for the first snapshot; this test sets that deadline directly on
// the client connection (rather than waiting out the real 5s constant) to
// verify decoder.Next() surfaces a timeout error that listenDaemonCmd maps
// to DaemonLostMsg, so the caller falls back to self-polling.
func TestListenDaemonEmitsDaemonLostOnReadTimeout(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "test.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()

	conn, err := dialDaemon(path)
	if err != nil {
		t.Fatalf("dialDaemon: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	select {
	case serverConn := <-accepted:
		// Accepted but deliberately sends nothing.
		t.Cleanup(func() { _ = serverConn.Close() })
	case <-time.After(2 * time.Second):
		t.Fatal("server never accepted the connection")
	}

	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	if _, ok := msg.(DaemonLostMsg); !ok {
		t.Fatalf("got %T, want DaemonLostMsg", msg)
	}
}

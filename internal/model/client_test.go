package model

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/cache"
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
		openURL:    func(string) error { return nil },
		ctx:        context.Background(),
	}
}

func fixtureSessions() []*session.Session {
	return []*session.Session{
		{Name: "alpha", Git: session.GitStatus{Branch: "feature/a", Modified: 2}},
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
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "", 0)()

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

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "", 0)()
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

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "", 0)()
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
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "alpha", 0)()
	snap := msg.(SnapshotMsg)
	if !snap.Sessions[0].IsCurrent {
		t.Error("should fall back to the current session detected at startup")
	}
}

func TestListenDaemonEmitsDaemonLostOnClose(t *testing.T) {
	conn := serveOneSnapshot(t, nil)

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "", 0)()
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

	_, _ = m2.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{{Name: "alpha"}}})
	if len(conn.deadlines) != 1 {
		t.Errorf("deadline should only be touched once, got %d calls", len(conn.deadlines))
	}
}

// TestHandleSnapshotNilConnDoesNotPanic covers the nil-guard: daemonConn can
// be nil (e.g. in tests, or hypothetically if wiring ever changes), and
// handleSnapshot must not dereference it unconditionally. The daemonReady
// assertion here is redundant with TestHandleSnapshotClearsFirstSnapshotDeadline;
// this test's only real value is that it does not panic with a nil conn.
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

	next, _ := m.handleDaemonLost(DaemonLostMsg{Epoch: m.epoch})
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

// TestHandleDaemonLostIsIdempotent guards against a future call site adding
// a second in-flight listenDaemonCmd: handleDaemonLost must not restart the
// fallback poll loops (or add a second warning) if the daemon connection is
// already gone.
func TestHandleDaemonLostIsIdempotent(t *testing.T) {
	m := newTestModel()

	next, cmd := m.handleDaemonLost(DaemonLostMsg{Epoch: m.epoch})
	m2 := next.(Model)

	if len(m2.notifications) != 0 {
		t.Errorf("want no notification when there was no daemon connection to lose, got %d", len(m2.notifications))
	}
	if cmd != nil {
		t.Error("want a nil command when there was no daemon connection to lose")
	}
}

// TestRenderTickStopsWhenDaemonGone pins the render tick's termination:
// without a live daemon decoder it must not reschedule itself, or it would
// run forever as a second 1s ticker alongside self-polling's own
// tmuxTickCmd after a fallback. It also checks the opposite direction -
// with a live decoder it must keep rescheduling - so the test would fail
// if the guard were ever inverted, not just if it were deleted.
func TestRenderTickStopsWhenDaemonGone(t *testing.T) {
	m := newTestModel()

	if _, cmd := m.Update(RenderTickMsg{Time: time.Now(), Epoch: m.epoch}); cmd != nil {
		t.Error("render tick should not reschedule once the daemon is gone (nil daemonDecoder)")
	}

	m.daemonDecoder = protocol.NewDecoder(strings.NewReader(""))
	if _, cmd := m.Update(RenderTickMsg{Time: time.Now(), Epoch: m.epoch}); cmd == nil {
		t.Error("render tick should keep rescheduling while a daemon is connected")
	}
}

// TestHandleSnapshotWarmsCachesAndClearsInitialLoad is the test that guards
// the actual point of this task: a daemon snapshot's git/PR state must
// survive into the model's caches unmerged and unmutated, and the
// self-polling-only initialLoad flag must still clear via the shared
// checkStateTransitions() call.
func TestHandleSnapshotWarmsCachesAndClearsInitialLoad(t *testing.T) {
	m := newTestModel()
	m.initialLoad = true

	pr := &session.PRStatus{Number: 7}
	sess := &session.Session{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "feature/x"},
		PR:   pr,
	}

	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{sess}})
	m2 := next.(Model)

	if got, ok := m2.gitCache["alpha"]; !ok || got.Branch != "feature/x" {
		t.Errorf("gitCache[alpha] = %+v, ok=%v, want Branch=feature/x", got, ok)
	}
	if got := m2.prCache["feature/x"]; got != pr {
		t.Errorf("prCache[feature/x] = %v, want %v (same pointer)", got, pr)
	}
	if m2.initialLoad {
		t.Error("initialLoad should be cleared after the first snapshot, same as the self-polling handlers")
	}
	if !m2.initialPRDone {
		t.Error("initialPRDone should be set when the snapshot actually carries PR data")
	}
	if len(m2.sessions) != 1 || m2.sessions[0] != sess {
		t.Error("handleSnapshot should hold onto the daemon's session pointer directly, not merge or copy it")
	}
}

// TestHandleSnapshotLeavesInitialPRDoneFalseWithoutPRData covers the fix for
// blank PR columns after a fallback: if the daemon's own PR fetch failed or
// is still pending, a snapshot can carry sessions with no PR data at all.
// Marking initialPRDone in that case would suppress handleGitUpdated's
// eager first PR fetch after falling back to self-polling, leaving PR
// columns blank for a full pr_interval (30s by default) instead of one.
func TestHandleSnapshotLeavesInitialPRDoneFalseWithoutPRData(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{
		{Name: "alpha", Git: session.GitStatus{Branch: "feature/x"}},
	}})
	m2 := next.(Model)
	if m2.initialPRDone {
		t.Error("initialPRDone should stay false when no session in the snapshot carries PR data")
	}
}

// TestHandleSnapshotFallsBackToLastKnownPR covers a daemon whose gh call
// failed after having succeeded: the snapshot carries a nil PR, and taking it
// verbatim would blank the PR column and flip the session to idle, firing a
// notification and the notify hook on a session that did not change.
func TestHandleSnapshotFallsBackToLastKnownPR(t *testing.T) {
	m := newTestModel()
	pr := &session.PRStatus{Number: 7, State: "OPEN"}
	m.prCache["feature/x"] = pr

	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: []*session.Session{
		{Name: "alpha", Git: session.GitStatus{Branch: "feature/x"}},
	}})
	m2 := next.(Model)

	if m2.sessions[0].PR != pr {
		t.Errorf("got PR %v, want the last known %v from prCache", m2.sessions[0].PR, pr)
	}
	if m2.initialPRDone {
		t.Error("initialPRDone should be judged on the snapshot as received, before the cache backfill")
	}
}

// TestNewLoadsCacheOutsidePopupMode pins the fix for a blank first paint in
// the standalone window: the cache load runs for every mode, synchronously,
// so a daemon that has not completed a successful poll yet does not leave the
// table empty. It must stay synchronous rather than emit a TmuxUpdatedMsg,
// which would re-merge stale cached data over live sessions.
func TestNewLoadsCacheOutsidePopupMode(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("TMUX", "")

	pr := &session.PRStatus{Number: 7, State: "OPEN"}
	cached := []*session.Session{{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "feature/x", GitRoot: "/repo/alpha"},
		PR:   pr,
	}}
	if err := cache.Save(cache.CachePath(), cached); err != nil {
		t.Fatalf("cache.Save: %v", err)
	}

	m := New(&config.Config{}, fetch.NewMockCommander())
	if len(m.sessions) != 1 || m.sessions[0].Name != "alpha" {
		t.Fatalf("got sessions %+v, want the cached alpha session", m.sessions)
	}
	if m.sessions[0].Git.Branch != "feature/x" {
		t.Errorf("got branch %q, want feature/x", m.sessions[0].Git.Branch)
	}
	if got, ok := m.gitCache["alpha"]; !ok || got.Branch != "feature/x" {
		t.Errorf("gitCache[alpha] = %+v, ok=%v, want the cached git state", got, ok)
	}
	if m.prCache["feature/x"] == nil {
		t.Error("prCache should be warmed from the cache load")
	}
}

// TestNewArmsFirstSnapshotReadDeadline proves New actually sets the read
// deadline on the freshly dialed connection, rather than merely proving
// listenDaemonCmd maps some error to DaemonLostMsg (already covered by
// TestListenDaemonEmitsDaemonLostOnClose). It points protocol.SocketPath at
// a throwaway directory via XDG_RUNTIME_DIR, listens there without ever
// accepting or writing anything (standing in for a daemon whose first poll
// failed), and shortens the package var so the test doesn't wait out the
// real 5s. If New's SetReadDeadline call is removed, decoder.Next() blocks
// forever and this test fails on the 2s bound below instead of hanging the
// suite (the leaked goroutine dies when t.Cleanup closes the listener at
// the end of the test, or otherwise when the test binary exits).
func TestNewArmsFirstSnapshotReadDeadline(t *testing.T) {
	dir := shortTempDir(t)
	sockDir := filepath.Join(dir, "vigil")
	if err := os.Mkdir(sockDir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	// New loads the session cache, so keep it off the developer's real one.
	t.Setenv("HOME", dir)

	sockPath := filepath.Join(sockDir, "vigild.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	orig := firstSnapshotTimeout
	firstSnapshotTimeout = 100 * time.Millisecond
	t.Cleanup(func() { firstSnapshotTimeout = orig })

	m := New(&config.Config{}, fetch.NewMockCommander())
	if m.daemonDecoder == nil {
		t.Fatal("New did not dial the daemon; want it to have connected to the listener above")
	}

	done := make(chan tea.Msg, 1)
	go func() {
		done <- listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch)()
	}()

	select {
	case msg := <-done:
		if _, ok := msg.(DaemonLostMsg); !ok {
			t.Fatalf("got %T, want DaemonLostMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listenDaemonCmd did not return within 2s: New's first-snapshot read deadline is not armed")
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
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "", 0)()
	if _, ok := msg.(DaemonLostMsg); !ok {
		t.Fatalf("got %T, want DaemonLostMsg", msg)
	}
}

func TestAnnotateClientFlagsMarksCurrentAndLast(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cmd.OnArgs("tmux list-sessions -F #{session_name}|#{session_last_attached}", "", nil)

	sessions := []*session.Session{{Name: "alpha"}, {Name: "beta"}}
	annotateClientFlags(context.Background(), cmd, sessions, "")

	if sessions[0].IsCurrent {
		t.Error("alpha should not be current")
	}
	if !sessions[1].IsCurrent {
		t.Error("beta should be current")
	}
}

// TestAnnotateClientFlagsBlanksAStaleLast pins the guard that a last-session
// name tmux still remembers, but which is no longer in the snapshot, does not
// mark anything. The fixture names a live current session so the assertion
// cannot pass just because every flag came back false.
func TestAnnotateClientFlagsBlanksAStaleLast(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.OnArgs("tmux display-message -p #{client_last_session}", "gone", nil)

	sessions := []*session.Session{{Name: "alpha"}}
	annotateClientFlags(context.Background(), cmd, sessions, "")

	if !sessions[0].IsCurrent {
		t.Fatal("alpha should be current, so a false IsLast below means something")
	}
	if sessions[0].IsLast {
		t.Error("alpha was marked last, but tmux named a session that is not in the snapshot")
	}
}

func TestAnnotateClientFlagsFallsBackWhenTmuxIsSilent(t *testing.T) {
	cmd := fetch.NewMockCommander()
	sessions := []*session.Session{{Name: "alpha"}}
	annotateClientFlags(context.Background(), cmd, sessions, "alpha")

	if !sessions[0].IsCurrent {
		t.Error("with no answer from tmux, the fallback current name should win")
	}
}

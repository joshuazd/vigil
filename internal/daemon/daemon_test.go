package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

// maxSockPath is the largest usable length for a unix socket path
// (sockaddr_un.sun_path is 104 bytes on macOS/BSD, including the null
// terminator). t.TempDir() embeds the full test function name plus a
// counter, which routinely exceeds this on macOS, so the socket lives
// under a short, fixed directory instead.
const maxSockPath = 103

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vigil-daemon-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testServer(t *testing.T) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	sockDir := shortTempDir(t)
	sockPath := filepath.Join(sockDir, "test.sock")
	if len(sockPath) > maxSockPath {
		t.Fatalf("socket path %q is %d bytes, want <= %d (sun_path limit)", sockPath, len(sockPath), maxSockPath)
	}
	cacheDir := t.TempDir()
	return &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   50 * time.Millisecond,
		SocketPath: sockPath,
		CachePath:  filepath.Join(cacheDir, "cache.json"),
	}
}

func TestServerSendsSnapshotOnConnect(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	snap, err := protocol.NewDecoder(conn).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if snap.Version != protocol.Version {
		t.Errorf("got version %d, want %d", snap.Version, protocol.Version)
	}
}

// TestServerBroadcastsToMultipleClients pins fan-out: all connected clients
// receive the same broadcasted snapshot, not just the one-shot connect-time
// send that accept does for every new connection regardless of poll.
func TestServerBroadcastsToMultipleClients(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	var decoders [3]*protocol.Decoder
	for i := 0; i < 3; i++ {
		conn, err := net.Dial("unix", s.SocketPath)
		if err != nil {
			t.Fatalf("Dial %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline %d: %v", i, err)
		}
		decoders[i] = protocol.NewDecoder(conn)
		if _, err := decoders[i].Next(); err != nil {
			t.Fatalf("client %d connect-time frame: %v", i, err)
		}
	}

	var timestamps [3]int64
	for i, d := range decoders {
		snap, err := d.Next()
		if err != nil {
			t.Fatalf("client %d broadcast frame: %v", i, err)
		}
		timestamps[i] = snap.Timestamp
	}

	if timestamps[0] != timestamps[1] || timestamps[1] != timestamps[2] {
		t.Fatalf("clients got different timestamps %v, want identical (same broadcast)", timestamps)
	}
}

func TestServerKeepsPushingOnInterval(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	d := protocol.NewDecoder(conn)
	for i := 0; i < 3; i++ {
		if _, err := d.Next(); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
}

func TestServerRefusesWhenAlreadyRunning(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	second := testServer(t)
	second.SocketPath = s.SocketPath
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	if err := second.Run(secondCtx); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("got %v, want ErrAlreadyRunning", err)
	}
}

func TestServerReplacesStaleSocket(t *testing.T) {
	s := testServer(t)
	if err := writeStaleSocketFile(s.SocketPath); err != nil {
		t.Fatalf("writeStaleSocketFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial after stale cleanup: %v", err)
	}
	_ = conn.Close()
}

func TestServerRejectsNonSocketAtPath(t *testing.T) {
	s := testServer(t)
	if err := os.WriteFile(s.SocketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Run(ctx)
	if err == nil {
		t.Fatal("got nil error, want rejection of a non-socket file at SocketPath")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got ErrAlreadyRunning, want a descriptive non-socket rejection error: %v", err)
	}
	if _, statErr := os.Stat(s.SocketPath); statErr != nil {
		t.Fatalf("file at %s was removed, want it left alone: %v", s.SocketPath, statErr)
	}
}

func TestServerRemovesSocketOnShutdown(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	waitForSocket(t, s.SocketPath)

	cancel()
	<-done

	if _, err := net.Dial("unix", s.SocketPath); err == nil {
		t.Error("socket still accepting connections after shutdown")
	}
}

// syncBuffer lets a test read a log.Logger's output while the daemon's poll
// goroutine is concurrently writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// flakyCommander fails the tmux list-panes call on demand, so poll's error
// path can be exercised. Access to fail/calls is mutex-guarded because it is
// read and written from the daemon's poll goroutine and the test goroutine.
type flakyCommander struct {
	mu    sync.Mutex
	fail  bool
	calls int
}

func (f *flakyCommander) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *flakyCommander) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakyCommander) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	full := name + " " + strings.Join(args, " ")
	if strings.Contains(full, "list-panes") {
		f.mu.Lock()
		f.calls++
		fail := f.fail
		f.mu.Unlock()
		if fail {
			return "", errors.New("tmux: no server running")
		}
		return "1700000000|alpha|/tmp/alpha", nil
	}
	if strings.Contains(full, "list-windows") {
		return "alpha|0", nil
	}
	return "", nil
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestServerLogsPollFailureTransitions confirms poll logs on the transition
// into failure and the transition back to healthy, not on every failing
// tick, and that it does not nil-panic when Log is set (as it always is via
// New, and as this test exercises directly on a hand-built Server).
func TestServerLogsPollFailureTransitions(t *testing.T) {
	cmd := &flakyCommander{fail: true}
	var buf syncBuffer
	sockPath := filepath.Join(shortTempDir(t), "test.sock")
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: sockPath,
		CachePath:  filepath.Join(t.TempDir(), "cache.json"),
		Log:        log.New(&buf, "", 0),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	waitForCondition(t, 2*time.Second, func() bool { return cmd.callCount() >= 2 })

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "poll failed") {
		t.Fatalf("got log lines %q after repeated failures, want exactly one poll failed line", buf.String())
	}

	callsBeforeRecovery := cmd.callCount()
	cmd.setFail(false)
	waitForCondition(t, 2*time.Second, func() bool { return cmd.callCount() > callsBeforeRecovery })
	waitForCondition(t, 2*time.Second, func() bool { return strings.Contains(buf.String(), "poll recovered") })

	lines = strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got log lines %q after recovery, want exactly one failure line and one recovery line", buf.String())
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became available", path)
}

func writeStaleSocketFile(path string) error {
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Close without unlinking, reproducing what a SIGKILLed daemon leaves
	// on disk. Close() removes the socket file by default, which would
	// make this helper a no-op and the test vacuous.
	ul := l.(*net.UnixListener)
	ul.SetUnlinkOnClose(false)
	return ul.Close()
}

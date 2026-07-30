package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// startServer runs s.Run in the background and registers a t.Cleanup that
// cancels it and waits for it to actually return before the test's TempDir
// cleanup can run. Without that wait, a live poll can still be mid-flight
// (in particular inside cache.Save, which writes into CachePath via a temp
// file) when TempDir starts deleting its directory, which intermittently
// fails with "directory not empty". It waits for the socket to become
// available before returning, since every caller needs that regardless.
func startServer(t *testing.T, s *Server) (ctx context.Context, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	waitForSocket(t, s.SocketPath)
	return ctx, stop
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

func TestNewDefaultsIntervalToTmuxInterval(t *testing.T) {
	t.Setenv("VIGIL_TMUX_INTERVAL", "0")
	s := New(&config.Config{}, fetch.NewMockCommander())
	if s.Interval != 1*time.Second {
		t.Errorf("got interval %s, want 1s", s.Interval)
	}
}

func TestNewDefaultsCollectorGitInterval(t *testing.T) {
	t.Setenv("VIGIL_GIT_INTERVAL", "0")
	s := New(&config.Config{}, fetch.NewMockCommander())
	if s.Collector.GitInterval != 3*time.Second {
		t.Errorf("got collector git interval %s, want 3s", s.Collector.GitInterval)
	}
}

// TestServerSendsSnapshotOnConnect pins the connect-time send specifically.
// The interval is long enough that no broadcast can arrive within the read
// deadline below, so the frame this test reads can only have come from
// accept's one-shot send. The wait for s.latest is what makes that
// deterministic: the socket is listenable before the first poll finishes, and
// a client that connects before then has nothing to be sent.
func TestServerSendsSnapshotOnConnect(t *testing.T) {
	s := testServer(t)
	s.Interval = 10 * time.Second
	startServer(t, s)
	waitForCondition(t, 2*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.latest != nil
	})

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
	startServer(t, s)

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
	startServer(t, s)

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
	startServer(t, s)

	second := testServer(t)
	second.SocketPath = s.SocketPath
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	if err := second.Run(secondCtx); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("got %v, want ErrAlreadyRunning", err)
	}
}

// TestListenErrorMapsAddrInUse covers the EADDRINUSE mapping Run relies on
// when a second daemon binds between this one's clearStaleSocket check and its
// own bind. That race cannot be staged through Run itself (a live socket is
// rejected earlier, by clearStaleSocket), so the mapping is exercised on the
// real bind error a busy path produces.
func TestListenErrorMapsAddrInUse(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "test.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	_, err = net.Listen("unix", path)
	if err == nil {
		t.Fatal("second Listen on a busy path succeeded, want a bind error")
	}
	if mapped := listenError(err); !errors.Is(mapped, ErrAlreadyRunning) {
		t.Errorf("listenError(%v) = %v, want ErrAlreadyRunning", err, mapped)
	}
}

func TestServerReplacesStaleSocket(t *testing.T) {
	s := testServer(t)
	if err := writeStaleSocketFile(s.SocketPath); err != nil {
		t.Fatalf("writeStaleSocketFile: %v", err)
	}

	startServer(t, s)

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

// TestServerLeavesNoSocketFileOnShutdown asserts the socket path is free
// again after shutdown, not merely that the listener stopped accepting: a
// leftover socket file is what makes the next daemon's clearStaleSocket do
// stale-socket recovery instead of a clean bind.
func TestServerLeavesNoSocketFileOnShutdown(t *testing.T) {
	s := testServer(t)
	_, stop := startServer(t, s)
	stop()

	if _, err := os.Stat(s.SocketPath); !os.IsNotExist(err) {
		t.Errorf("socket file still at %s after shutdown (stat err %v)", s.SocketPath, err)
	}
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

	startServer(t, s)

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

// dialWhenReady waits for the socket to accept connections (reusing
// waitForSocket's polling rather than inventing a second one) and returns a
// live connection for the caller to use, unlike waitForSocket itself, which
// only probes readiness and closes immediately.
func dialWhenReady(t *testing.T, path string) net.Conn {
	t.Helper()
	waitForSocket(t, path)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return conn
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

// testConfig is a bare config for tests that only need a Collector to run
// without erroring - it carries no hooks or settings, so it must not be used
// where dispatch behavior (hooks, dispatch_timeout) matters; testJobsConfig
// is for that.
func testConfig() *config.Config {
	return &config.Config{}
}

// Task 5's deliverable is that a request reaches the job queue, so this asserts
// against the queue directly rather than over the wire. The end-to-end version -
// the job appearing in a broadcast Snapshot - belongs to Task 6, which is what
// populates Snapshot.Jobs; asserting it here would make this task's acceptance
// depend on the next task's deliverable.
func TestASubmittedRequestReachesTheJobQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sockDir := shortTempDir(t)
	stream := newBlockingStream()
	srv := &Server{
		Collector:  collect.New(testConfig(), fetch.NewMockCommander()),
		Interval:   10 * time.Millisecond,
		SocketPath: filepath.Join(sockDir, "vigild.sock"),
		Log:        log.New(io.Discard, "", 0),
		requests:   make(chan *protocol.Request, queueDepth),
	}
	srv.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), srv.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	conn := dialWhenReady(t, srv.SocketPath)
	defer func() { _ = conn.Close() }()

	if err := protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch,
		ID: "job-1", Input: "sc-12345",
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if findJob(srv.jobs.snapshot(), "job-1") != nil {
			close(stream.release)
			cancel()
			<-runDone
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(stream.release)
	cancel()
	<-runDone
	t.Fatal("the request never reached the job queue")
}

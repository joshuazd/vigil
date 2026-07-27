package daemon

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	dir := t.TempDir()
	return &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   50 * time.Millisecond,
		SocketPath: filepath.Join(dir, "test.sock"),
		CachePath:  filepath.Join(dir, "cache.json"),
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

func TestServerBroadcastsToMultipleClients(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	for i := 0; i < 3; i++ {
		conn, err := net.Dial("unix", s.SocketPath)
		if err != nil {
			t.Fatalf("Dial %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		if _, err := protocol.NewDecoder(conn).Next(); err != nil {
			t.Fatalf("client %d Next: %v", i, err)
		}
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
	if err := second.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
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

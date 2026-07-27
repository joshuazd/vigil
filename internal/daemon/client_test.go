package daemon

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// blockingConn is a net.Conn whose Write blocks until release is closed,
// standing in for a client that has stopped reading. It embeds a nil net.Conn
// so any method the code under test should not be calling panics loudly.
type blockingConn struct {
	net.Conn
	release chan struct{}
	mu      sync.Mutex
	writes  int
	closed  bool
	failing bool
}

func newBlockingConn() *blockingConn {
	return &blockingConn{release: make(chan struct{})}
}

func (c *blockingConn) Write(p []byte) (int, error) {
	<-c.release
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if c.failing {
		return 0, errors.New("connection wedged")
	}
	return len(p), nil
}

func (c *blockingConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *blockingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *blockingConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *blockingConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func snap(ts int64) *protocol.Snapshot {
	return &protocol.Snapshot{Version: protocol.Version, Timestamp: ts}
}

// TestQueueNeverBlocks is the whole point of the type: the poll loop hands a
// snapshot to a wedged client and keeps going.
func TestQueueNeverBlocks(t *testing.T) {
	c := newClient(newBlockingConn())
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.queue(snap(int64(i)))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queue blocked")
	}
}

// TestQueueKeepsOnlyTheNewestSnapshot pins latest-wins. A slow client that
// catches up must see current state, not a backlog of stale frames.
func TestQueueKeepsOnlyTheNewestSnapshot(t *testing.T) {
	c := newClient(newBlockingConn())
	c.queue(snap(1))
	c.queue(snap(2))
	c.queue(snap(3))

	if got := len(c.ch); got != 1 {
		t.Fatalf("got %d queued snapshots, want 1", got)
	}
	if got := (<-c.ch).Timestamp; got != 3 {
		t.Errorf("got timestamp %d, want 3 (the newest)", got)
	}
}

// TestPollDoesNotWaitForASlowClient fails if sends happen inline on the poll
// loop, which is how it works before this task.
func TestPollDoesNotWaitForASlowClient(t *testing.T) {
	s := testServer(t)
	conn := newBlockingConn()
	s.addClient(conn)
	t.Cleanup(func() { close(conn.release); s.closeClients() })

	polled := make(chan struct{})
	go func() {
		s.poll(context.Background())
		s.poll(context.Background())
		close(polled)
	}()

	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("poll blocked behind a client that is not reading")
	}
}

// TestAcceptDoesNotBlockOnConnectTimeSend covers the other half: the
// connect-time send used to run on the accept goroutine, so one wedged client
// also stopped new panels from connecting.
func TestAcceptDoesNotBlockOnConnectTimeSend(t *testing.T) {
	s := testServer(t)
	s.Interval = 10 * time.Second
	startServer(t, s)
	waitForCondition(t, 2*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.latest != nil
	})

	// A client that connects and never reads. Its snapshot fits in the socket
	// buffer, so this alone would not wedge the daemon; what it proves is that
	// a second client still gets served promptly.
	wedged, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = wedged.Close() }()

	second, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := protocol.NewDecoder(second).Next(); err != nil {
		t.Fatalf("second client got no snapshot: %v", err)
	}
}

// TestBroadcastPrunesFailedClients keeps the client list from growing without
// bound as panels come and go.
func TestBroadcastPrunesFailedClients(t *testing.T) {
	s := testServer(t)
	conn := newBlockingConn()
	conn.failing = true
	s.addClient(conn)
	close(conn.release)

	// s.latest is nil (poll never ran), so addClient queued nothing; drive a
	// broadcast to give the writer something to fail on.
	waitForCondition(t, 2*time.Second, func() bool {
		s.broadcast(snap(1))
		return conn.writeCount() > 0
	})
	waitForCondition(t, 2*time.Second, func() bool {
		s.broadcast(snap(1))
		return len(s.clients) == 0
	})
}

// TestRunWaitsForWriters pins the shutdown handshake. writeLoop is what closes
// the connection, so an open conn after Run returns means Run walked away from
// its goroutines.
//
// addClient runs before Run starts, deliberately: s.clients is owned by Run's
// goroutine, so touching it from the test goroutine while Run polls is the
// very race this design exists to remove. Same reason
// TestPollDoesNotWaitForASlowClient and TestBroadcastPrunesFailedClients
// never start Run at all - they drive poll and broadcast directly, on one
// goroutine.
func TestRunWaitsForWriters(t *testing.T) {
	s := testServer(t)
	conn := newBlockingConn()
	close(conn.release)
	s.addClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	waitForSocket(t, s.SocketPath)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	if !conn.isClosed() {
		t.Error("Run returned before its writer goroutine closed the connection")
	}
}

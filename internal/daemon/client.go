package daemon

import (
	"net"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// writeTimeout bounds one frame write to one client. A client slower than
// this is not a client worth keeping: it will get the next snapshot when it
// reconnects.
const writeTimeout = 5 * time.Second

// client owns one connection's writes. Exactly one goroutine (writeLoop) ever
// writes to the connection, and the queue holds at most one pending snapshot,
// so a client that reads slowly gets the newest state rather than a backlog
// and can never stall the poll loop that queues for it.
type client struct {
	conn net.Conn
	ch   chan *protocol.Snapshot
	done chan struct{}
}

func newClient(conn net.Conn) *client {
	return &client{
		conn: conn,
		ch:   make(chan *protocol.Snapshot, 1),
		done: make(chan struct{}),
	}
}

// queue hands snap to the writer without ever blocking, discarding an
// already-pending snapshot in favor of the newer one.
//
// Safe only because Run's goroutine is the sole caller: the drain-then-send
// below would race against a second queuer. stop must likewise not overlap
// with queue, and does not, because Run only stops clients on the way out.
func (c *client) queue(snap *protocol.Snapshot) {
	select {
	case c.ch <- snap:
		return
	default:
	}
	select {
	case <-c.ch:
	default:
	}
	select {
	case c.ch <- snap:
	default:
	}
}

// gone reports whether the writer has exited, so Run can prune the client.
func (c *client) gone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// stop ends the writer once it has drained whatever is already queued.
func (c *client) stop() { close(c.ch) }

func (c *client) writeLoop(logf func(string, ...any)) {
	defer close(c.done)
	defer func() { _ = c.conn.Close() }()
	for snap := range c.ch {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := protocol.Encode(c.conn, snap); err != nil {
			logf("dropping client: %v", err)
			return
		}
	}
}

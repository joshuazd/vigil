package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// writeTimeout bounds one frame write to one client. A client slower than
// this is not a client worth keeping: it will get the next snapshot when it
// reconnects.
const writeTimeout = 5 * time.Second

// client owns one connection's writes and, since dispatch, reads. Exactly one
// goroutine (writeLoop) ever writes to the connection or closes it, and the
// queue holds at most one pending snapshot, so a client that reads slowly
// gets the newest state rather than a backlog and can never stall the poll
// loop that queues for it. A second goroutine (readLoop) reads Request frames
// from the same connection but never closes it, so a reader at EOF cannot
// pull the socket out from under a writer mid-Encode.
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

// readLoop consumes Request frames from this client until the connection ends.
// It never closes the connection: writeLoop is the sole closer, so a reader at
// EOF cannot pull the socket out from under a writer mid-Encode.
//
// A malformed frame does not end the loop: the line was already off the wire,
// so the connection is still good, and silently dropping the client's ability
// to dispatch - while it keeps receiving snapshots and looks perfectly healthy
// - is the same failure shape jobs.submit's own refusal-registers-a-reason
// comment exists to avoid. Only a bare decode error (the transport itself is
// gone) ends the loop.
func (c *client) readLoop(ctx context.Context, requests chan<- *protocol.Request, logf func(string, ...any)) {
	dec := protocol.NewRequestDecoder(c.conn)
	for {
		req, err := dec.Next()
		if err != nil {
			if errors.Is(err, protocol.ErrMalformedRequest) {
				logf("dropping malformed request frame: %v", err)
				continue
			}
			return
		}
		select {
		case requests <- req:
		case <-ctx.Done():
			return
		case <-c.done:
			return
		}
	}
}

func (c *client) writeLoop(logf func(string, ...any)) {
	defer close(c.done)
	defer func() { _ = c.conn.Close() }()
	for snap := range c.ch {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := protocol.Encode(c.conn, snap); err != nil {
			// A peer that closed the socket is routine - a TUI quitting, a
			// panel toggled off - and the daemon stays silent when healthy.
			// A deadline expiring means a client held the connection open
			// and stopped reading, which is the failure worth reporting.
			if errors.Is(err, os.ErrDeadlineExceeded) {
				logf("dropping unresponsive client after %s", writeTimeout)
			}
			return
		}
	}
}

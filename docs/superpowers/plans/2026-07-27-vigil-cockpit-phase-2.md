# Vigil Cockpit Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `vigil --panel` plus a tmux toggle binding so any session can show a live session list beside its Claude pane, and make the daemon safe for many clients first.

**Architecture:** Three daemon/client robustness fixes land before any panel code: an flock'd lock file for start-time mutual exclusion, a per-client writer goroutine so no client can stall the poll loop, and an epoch-guarded reconnect probe with a staleness indicator. Then the pure view layer gains a width-responsive table layout, and panel mode is the existing `Model` with `panelMode = true`: no detail panel, no footer, a compact status bar, and an on-demand daemon spawn. tmux decides panel placement (a shell toggle script measures the client and splits); vigil only renders to fit.

**Tech Stack:** Go 1.22+, Bubble Tea, lipgloss, unix sockets, `syscall.Flock`. Shell side: bash, tmux 3.6a, bats-core with an argv-recording `tmux` stub.

## Global Constraints

- Two repositories. Tasks 1-7 are in `~/vigil` (branch off `main`). Tasks 8-9 are in `~/dotfiles` (branch off `master`). Never mix them in one commit.
- ANSI colors only. No hardcoded hex anywhere in `internal/view`.
- No global mutable state. Config and caches are passed explicitly.
- `internal/view` stays pure: no subprocesses, no I/O, no time reads in render paths beyond what is already there.
- Both data paths (daemon and self-polling) must keep rendering identical git/PR data, sort order, and notifications. The only permitted new difference is the daemon-health indicator, which is empty in the TUI when self-polling and reads `no daemon` only in panel mode.
- The TUI's appearance at width >= 104 must not change at all. The session-name column stays capped at 52 columns.
- `make test` and `make lint` must pass at the end of every task in `~/vigil`. `make test` and `make lint` in `~/dotfiles/scripts/scripts` for tasks 8-9.
- Do not remove the per-key memos in `internal/collect`. PR state is gated on `pr_interval` (default 30s) for a measured reason: ungated it is ~4x over GitHub's 5,000/hour GraphQL limit.
- Commit after every task. Conventional commit prefixes (`feat`, `fix`, `test`, `docs`, `refactor`).

## One deviation from the spec's testing section

The spec asks for "golden renders at several widths and heights" for the panel. This plan asserts invariants instead - total width never exceeds the pane, line count equals the pane height, named columns present or absent at each threshold, and the git column pinned at offset 63 when the layout is full. Golden files across nine widths would be nine blobs of escape sequences that no reviewer can read and that any style tweak invalidates wholesale, while the invariants say what actually matters and say why they broke when they break. The regression pin the golden files were for is kept explicitly: `TestLayoutMatchesTodayAtFullWidth` plus `TestTableKeepsNameColumnPinnedAtFullWidth`.

## Out of scope, deliberately

- **Collapsing the TUI's self-polling onto `internal/collect`.** Real duplication, worth doing, but it touches notification and state-transition logic that the panel work also touches. Doing both at once makes review impossible. Phase 3 or its own branch.
- **Lazy review-thread fetching** (halving the daemon's `gh` cost). The panel adds clients, not pollers, so the budget does not move in phase 2. Revisit with phase 5's work queue.
- **`pr_interval = 60`.** A config edit, not code. Mention it to the user; do not change the default.
- **Panel by default for new sessions.** That is phase 3.

---

## File Structure

**`~/vigil`**

| File | Responsibility |
|---|---|
| `internal/daemon/lock.go` (new) | flock'd lock file: start-time mutual exclusion, released by the kernel on death |
| `internal/daemon/client.go` (new) | One connected client: bounded latest-wins queue plus a single writer goroutine |
| `internal/daemon/daemon.go` (modify) | `Run` becomes the sole owner of the client list; accept hands connections over a channel |
| `internal/model/messages.go` (modify) | Tick and daemon messages carry an epoch; two new probe messages |
| `internal/model/client.go` (modify) | Dial, probe, spawn: the whole client side of the daemon relationship |
| `internal/model/model.go` (modify) | Epoch-guarded tick handling, reconnect, `daemonHealth`, panel construction and view |
| `internal/view/layout.go` (new) | `TableLayout`, `LayoutForWidth`, ANSI-aware truncation |
| `internal/view/table.go` (modify) | Render rows against a layout instead of fixed column constants |
| `internal/view/statusbar.go` (modify) | Segment-budgeted status bar with a health segment |
| `main.go` (modify) | `--panel` subcommand |
| `CLAUDE.md` (modify) | Architecture, conventions, panel mode |

**`~/dotfiles`**

| File | Responsibility |
|---|---|
| `scripts/scripts/vigil-panel` (new) | Toggle: measure the client, split, mark the pane, or kill an existing panel |
| `scripts/scripts/tests/vigil_panel.bats` (new) | End-to-end tests of that script against the tmux stub |
| `scripts/scripts/tests/stubs/tmux` (modify) | Canned responses for `list-panes`, `show-options`, `split-window -P` |
| `scripts/scripts/lib/tmux.sh` (modify) | Pane targeting by `pane_id` instead of positional `.1` / `.2` |
| `scripts/scripts/Makefile` (modify) | `vigil-panel` into `SHELL_SCRIPTS` for shellcheck |
| `tmux/.tmux.conf` (modify) | `prefix p` / `prefix C-p` toggle binding |

---

## Task 1: Daemon start-time mutual exclusion

**Why:** With a stale socket file present, two daemons can both decide it is stale, both unlink it, and both bind. The first is orphaned yet still polls and still writes the shared cache. Dormant today because nothing autostarts the daemon; task 6 makes `vigil --panel` spawn one on demand from N panels, which is exactly that race run concurrently.

**Files:**
- Create: `internal/daemon/lock.go`
- Modify: `internal/daemon/daemon.go:56-90` (`Run`)
- Test: `internal/daemon/lock_test.go` (new)

**Interfaces:**
- Consumes: `Server.SocketPath`, `ErrAlreadyRunning` (both existing).
- Produces: `func (s *Server) acquireLock() (release func(), err error)` and `func (s *Server) lockPath() string`, used only by `Run`.

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/lock_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// holdLock takes the same flock the daemon takes, from this process but on a
// separate fd, standing in for a second daemon that is already running. flock
// is per-fd, so this genuinely blocks acquireLock.
func holdLock(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Flock: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
}

// TestRunRefusesWhenLockHeld is the case EADDRINUSE mapping cannot cover:
// there is no socket file at all, so without the lock Run would bind happily
// and two daemons would poll side by side.
func TestRunRefusesWhenLockHeld(t *testing.T) {
	s := testServer(t)
	holdLock(t, s.lockPath())

	if _, err := os.Stat(s.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("want no socket file before Run, got err %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Run(ctx)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
}

// TestRunLocksBeforeTouchingTheSocket pins the ordering. If the lock is taken
// after clearStaleSocket, the losing daemon deletes the winner's socket file
// on its way out, which is worse than the race it was meant to fix.
func TestRunLocksBeforeTouchingTheSocket(t *testing.T) {
	s := testServer(t)
	holdLock(t, s.lockPath())
	if err := writeStaleSocketFile(s.SocketPath); err != nil {
		t.Fatalf("writeStaleSocketFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
	if _, err := os.Stat(s.SocketPath); err != nil {
		t.Fatalf("Run removed the socket file it did not own: %v", err)
	}
}

// TestRunReleasesLockOnShutdown proves the release actually happens: a second
// Run on the same paths must succeed after the first returns.
func TestRunReleasesLockOnShutdown(t *testing.T) {
	s := testServer(t)
	_, stop := startServer(t, s)
	stop()

	second := testServer(t)
	second.SocketPath = s.SocketPath
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx) }()
	waitForSocket(t, second.SocketPath)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

// TestLockFileSurvivesShutdown pins that the lock file itself is not removed.
// Unlinking it lets a starting daemon create a fresh inode and lock that,
// while the running daemon still holds a lock on the old one: two daemons,
// each holding a lock, neither aware of the other.
func TestLockFileSurvivesShutdown(t *testing.T) {
	s := testServer(t)
	_, stop := startServer(t, s)
	stop()
	if _, err := os.Stat(s.lockPath()); err != nil {
		t.Fatalf("lock file gone after shutdown: %v", err)
	}
}

func TestLockPathSitsBesideTheSocket(t *testing.T) {
	s := &Server{SocketPath: filepath.Join("/tmp", "vigild.sock")}
	if got, want := s.lockPath(), "/tmp/vigild.sock.lock"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/daemon/ -run 'Lock' -v`
Expected: FAIL to compile with `s.lockPath undefined`.

- [ ] **Step 3: Write the implementation**

Create `internal/daemon/lock.go`:

```go
package daemon

import (
	"errors"
	"os"
	"syscall"
)

// acquireLock takes an exclusive non-blocking flock on a lock file beside the
// socket. Held across the stale-socket removal and the bind, it makes those
// two steps atomic with respect to another starting daemon: without it, two
// daemons can both find the socket stale, both unlink it, and both bind,
// leaving the first orphaned but still polling and still writing the cache.
//
// flock lives on the open file description, so the kernel drops it when the
// process dies however violently. There is nothing to clean up after a
// SIGKILL, which is the property a pidfile would not give us.
func (s *Server) acquireLock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	// Closing the fd releases the lock. The file itself is deliberately left
	// in place: unlinking it would let the next daemon create a fresh inode
	// and lock that while this one still holds a lock on the old one.
	return func() { _ = f.Close() }, nil
}

func (s *Server) lockPath() string {
	return s.SocketPath + ".lock"
}
```

Modify `Run` in `internal/daemon/daemon.go` so the lock is taken immediately after the directory exists and before anything looks at the socket:

```go
func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return err
	}
	release, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer release()

	if err := s.clearStaleSocket(); err != nil {
		return err
	}
	// ... rest unchanged
```

Leave `clearStaleSocket` and `listenError` alone. The dial probe still matters during an upgrade, when a pre-lock daemon may be live, and `listenError` still gives the friendly message if anything slips between the check and the bind.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -race -v`
Expected: PASS, including the pre-existing socket tests.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/lock.go internal/daemon/lock_test.go internal/daemon/daemon.go
git commit -m "fix(daemon): serialize startup on an flock'd lock file"
```

---

## Task 2: Single-writer broadcast

**Why:** The daemon sends to clients sequentially from the poll loop with a 5s write deadline, and the connect-time send runs on the accept goroutine. One client that connects and never reads therefore stalls both polling and new connections. Correct at one client; a panel per session is not one client.

**Files:**
- Create: `internal/daemon/client.go`
- Modify: `internal/daemon/daemon.go` (`Server` struct, `Run`, `accept`, `poll`, `send`, `drop`, `closeClients`)
- Test: `internal/daemon/client_test.go` (new)

**Interfaces:**
- Consumes: `protocol.Encode`, `protocol.Snapshot`, `Server.logf` (all existing).
- Produces, all package-private:
  - `type client struct { conn net.Conn; ch chan *protocol.Snapshot; done chan struct{} }`
  - `func newClient(conn net.Conn) *client`
  - `func (c *client) queue(snap *protocol.Snapshot)` - never blocks, latest wins
  - `func (c *client) gone() bool`
  - `func (c *client) stop()`
  - `func (c *client) writeLoop(logf func(string, ...any))`
  - `func (s *Server) addClient(conn net.Conn)` and `func (s *Server) broadcast(snap *protocol.Snapshot)`
- Removes: `Server.send`, `Server.drop`, and the `clients map[net.Conn]struct{}` field.

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/client_test.go`:

```go
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

	waitForCondition(t, 2*time.Second, func() bool { return conn.writeCount() > 0 })
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/daemon/ -race -run 'Queue|Poll|Accept|Broadcast|Writers' -v`
Expected: FAIL to compile with `undefined: newClient`, `s.addClient undefined`, `s.broadcast undefined`.

- [ ] **Step 3: Write the implementation**

Create `internal/daemon/client.go`:

```go
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
```

Rewrite the server side of `internal/daemon/daemon.go`. The struct:

```go
type Server struct {
	Collector  *collect.Collector
	Interval   time.Duration
	SocketPath string
	CachePath  string
	Log        *log.Logger

	// mu guards latest only. clients is owned by Run's goroutine: poll,
	// addClient and broadcast all run there and nothing else touches it.
	mu     sync.Mutex
	latest *protocol.Snapshot

	clients []*client
	writers sync.WaitGroup

	// pollFailing is only read and written from poll, which Run only ever
	// calls from its own goroutine, so it needs no mutex.
	pollFailing bool
}
```

`Run`, with the lock from task 1 kept in place:

```go
func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return err
	}
	release, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer release()

	if err := s.clearStaleSocket(); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return listenError(err)
	}
	defer func() { _ = os.Remove(s.SocketPath) }()

	// Accept only hands connections over; Run does every send, so a client
	// that never reads cannot block new connections.
	incoming := make(chan net.Conn)
	var accepted sync.WaitGroup
	accepted.Add(1)
	go func() {
		defer accepted.Done()
		s.accept(ctx, listener, incoming)
	}()

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			// Closing the listener is what unblocks accept out of Accept.
			_ = listener.Close()
			accepted.Wait()
			s.closeClients()
			return nil
		case conn := <-incoming:
			s.addClient(conn)
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}
```

`accept` gains the channel and a ctx-guarded handoff:

```go
func (s *Server) accept(ctx context.Context, listener net.Listener, incoming chan<- net.Conn) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case incoming <- conn:
		case <-ctx.Done():
			_ = conn.Close()
			return
		}
	}
}
```

`addClient`, `broadcast`, `closeClients`, replacing `send` and `drop`:

```go
// addClient registers a connection and sends it the latest snapshot, if there
// is one. A client that connects before the first successful poll gets
// nothing until the next one, and falls back to self-polling if that takes
// too long.
func (s *Server) addClient(conn net.Conn) {
	c := newClient(conn)
	s.clients = append(s.clients, c)
	s.writers.Add(1)
	go func() {
		defer s.writers.Done()
		c.writeLoop(s.logf)
	}()

	s.mu.Lock()
	latest := s.latest
	s.mu.Unlock()
	if latest != nil {
		c.queue(latest)
	}
}

// broadcast queues snap for every live client and prunes the dead ones.
func (s *Server) broadcast(snap *protocol.Snapshot) {
	live := s.clients[:0]
	for _, c := range s.clients {
		if c.gone() {
			continue
		}
		c.queue(snap)
		live = append(live, c)
	}
	for i := len(live); i < len(s.clients); i++ {
		s.clients[i] = nil
	}
	s.clients = live
}

func (s *Server) closeClients() {
	for _, c := range s.clients {
		c.stop()
	}
	s.clients = nil
	s.writers.Wait()
}
```

`poll` loses its inline send loop:

```go
	s.mu.Lock()
	s.latest = snap
	s.mu.Unlock()

	s.broadcast(snap)

	if s.CachePath != "" {
		_ = cache.Save(s.CachePath, sessions)
	}
```

Delete the `s.clients = make(map[net.Conn]struct{})` line from `Run`, and delete `send` and `drop`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -race -count=3 -v`
Expected: PASS. `-count=3` because the pre-existing broadcast and interval tests are timing-sensitive and this task moves when sends happen.

- [ ] **Step 5: Fix the pre-existing tests that reach into `s.clients`**

Run: `grep -n 's.clients' internal/daemon/*_test.go`
Any test asserting on the old map must be rewritten against the slice. Do not delete an assertion to make it pass.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/
git commit -m "fix(daemon): give every client its own writer so one cannot stall the poll loop"
```

---

## Task 3: Epoch-guarded ticks

**Why:** Bubble Tea ticks are self-rescheduling messages and cannot be cancelled. Task 4 needs to switch between self-polling and daemon mode more than once, and without a generation guard each switch leaves the previous mode's tickers running forever. This task is a pure refactor with no behavior change; task 4 is what uses it.

**Files:**
- Modify: `internal/model/messages.go:9-22` (tick message types), `internal/model/messages.go:75-81` (`SnapshotMsg`, `DaemonLostMsg`)
- Modify: `internal/model/model.go` (`Model` struct, `Init`, `Update`, `handleSnapshot`, `handleDaemonLost`, the four tick cmds)
- Modify: `internal/model/client.go` (`listenDaemonCmd` signature)
- Modify: `internal/model/client_test.go:290-306` (`TestRenderTickStopsWhenDaemonGone`)
- Test: `internal/model/epoch_test.go` (new)

**Interfaces:**
- Produces:
  - `type TmuxTickMsg struct { Time time.Time; Epoch int }`, same shape for `GitTickMsg`, `PRTickMsg`, `RenderTickMsg`
  - `SnapshotMsg{ Sessions []*session.Session; Epoch int }`, `DaemonLostMsg{ Epoch int }`
  - `Model.epoch int` - bumped on every switch between daemon and self-polling
  - tick cmds take an epoch: `tmuxTickCmd(interval time.Duration, epoch int) tea.Cmd`, same for `gitTickCmd`, `prTickCmd`, `renderTickCmd`
  - `listenDaemonCmd(decoder *protocol.Decoder, ctx context.Context, cmd fetch.Commander, fallbackCurrent string, epoch int) tea.Cmd`

- [ ] **Step 1: Write the failing tests**

Create `internal/model/epoch_test.go`:

```go
package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// staleTickCases covers every self-rescheduling tick. A tick born in an
// earlier epoch must die rather than reschedule itself, or a mode switch
// leaves two independent tickers running for the life of the process.
func TestStaleTicksDoNotReschedule(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 0}},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 0}},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 0}},
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 1
			if _, cmd := m.Update(tc.msg); cmd != nil {
				t.Error("a stale tick produced a command")
			}
		})
	}
}

func TestCurrentTicksReschedule(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 7}},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 7}},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 7
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

func TestStaleDaemonLostIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	got, cmd := m.Update(DaemonLostMsg{Epoch: 1})
	if cmd != nil {
		t.Error("a stale daemon-lost restarted the fallback poll loops")
	}
	if len(got.(Model).notifications) != 0 {
		t.Error("a stale daemon-lost notified the user")
	}
}
```

Add the fixture helper to `internal/model/client_test.go` if it is not already there:

```go
func fixtureSessions() []*session.Session {
	return []*session.Session{
		{Name: "alpha", Git: session.GitStatus{Branch: "feature/a", Modified: 2}},
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/ -run 'Epoch|StaleTick|CurrentTick|StaleSnapshot|StaleDaemonLost' -v`
Expected: FAIL to compile - `TmuxTickMsg` is a `time.Time`, not a struct, and `Model.epoch` does not exist.

- [ ] **Step 3: Write the implementation**

`internal/model/messages.go` - every self-rescheduling tick and both daemon messages carry the epoch they were born in:

```go
// TmuxTickMsg triggers a tmux metadata polling cycle. Epoch is the polling
// generation it was scheduled in: Bubble Tea ticks cannot be cancelled, so a
// tick from a superseded generation is dropped instead of rescheduling
// itself. Without that, every switch between daemon and self-polling would
// leave the previous mode's tickers running for the life of the process.
type TmuxTickMsg struct {
	Time  time.Time
	Epoch int
}

// GitTickMsg triggers a git polling cycle.
type GitTickMsg struct {
	Time  time.Time
	Epoch int
}

// PRTickMsg triggers a PR polling cycle.
type PRTickMsg struct {
	Time  time.Time
	Epoch int
}

// RenderTickMsg triggers a repaint with no fetch work. The daemon path uses
// it to get the same 1s render cadence self-polling gets for free from
// TmuxTickMsg, so time-based rendering (like notification expiry) behaves
// the same whether or not a daemon is connected.
type RenderTickMsg struct {
	Time  time.Time
	Epoch int
}
```

```go
// SnapshotMsg carries a full session snapshot received from the daemon, with
// per-client flags already resolved.
type SnapshotMsg struct {
	Sessions []*session.Session
	Epoch    int
}

// DaemonLostMsg reports that the daemon stream ended, so the TUI should
// resume self-polling.
type DaemonLostMsg struct {
	Epoch int
}
```

`internal/model/model.go` - add the field with the other mode state:

```go
	// epoch identifies the current polling generation. Every switch between
	// daemon snapshots and self-polling bumps it, which retires the previous
	// generation's ticks and any snapshot or loss still in flight from it.
	epoch int
```

The tick cmds:

```go
func tmuxTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return TmuxTickMsg{Time: t, Epoch: epoch}
	})
}

func gitTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return GitTickMsg{Time: t, Epoch: epoch}
	})
}

func prTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return PRTickMsg{Time: t, Epoch: epoch}
	})
}

// renderTickCmd triggers a repaint with no fetch work, so the daemon path
// gets the same render cadence tmuxTickCmd gives self-polling.
func renderTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return RenderTickMsg{Time: t, Epoch: epoch}
	})
}
```

`Update` guards each one. Replace the four existing cases:

```go
	case TmuxTickMsg:
		if msg.Epoch != m.epoch {
			return m, nil
		}
		return m, tea.Batch(m.fetchTmuxCmd(), tmuxTickCmd(1*time.Second, m.epoch))

	case GitTickMsg:
		if msg.Epoch != m.epoch {
			return m, nil
		}
		return m, tea.Batch(m.fetchGitCmd(), gitTickCmd(m.cfg.GetSettingDuration("git_interval"), m.epoch))

	case PRTickMsg:
		if msg.Epoch != m.epoch {
			return m, nil
		}
		return m, tea.Batch(m.fetchPRsCmd(), prTickCmd(m.cfg.GetSettingDuration("pr_interval"), m.epoch))

	case RenderTickMsg:
		// Render-only heartbeat for the daemon path: Bubble Tea always calls
		// View() after Update, so this does nothing but keep the screen
		// repainting at the 1s cadence self-polling gets from tmuxTickCmd,
		// which is what makes notification expiry behave the same on both
		// paths. The epoch check retires it when the daemon goes away.
		if msg.Epoch != m.epoch || m.daemonDecoder == nil {
			return m, nil
		}
		return m, renderTickCmd(1*time.Second, m.epoch)
```

`handleSnapshot` and `handleDaemonLost` gain a stale check at the top:

```go
func (m Model) handleSnapshot(msg SnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.epoch {
		// In flight when the connection was retired. Applying it would
		// overwrite self-polled state with data from a dead daemon.
		return m, nil
	}
	// ... existing body
```

```go
func (m Model) handleDaemonLost(msg DaemonLostMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.epoch {
		return m, nil
	}
	// ... existing body
```

Update the `DaemonLostMsg` case in `Update` to `return m.handleDaemonLost(msg)`, and the guard inside `handleDaemonLost` that checked for a nil conn stays as it is.

Everything in `handleDaemonLost` that schedules the fallback loops now bumps the epoch first:

```go
	m.epoch++
	m.daemonReady = false
	m.addNotification("daemon lost, polling directly", "warning")
	return m, tea.Batch(
		m.fetchTmuxCmd(),
		m.fetchGitCmd(),
		tmuxTickCmd(1*time.Second, m.epoch),
		gitTickCmd(m.cfg.GetSettingDuration("git_interval"), m.epoch),
		prTickCmd(m.cfg.GetSettingDuration("pr_interval"), m.epoch),
	)
```

`listenDaemonCmd` in `internal/model/client.go` takes and stamps the epoch:

```go
func listenDaemonCmd(
	decoder *protocol.Decoder,
	ctx context.Context,
	cmd fetch.Commander,
	fallbackCurrent string,
	epoch int,
) tea.Cmd {
	return func() tea.Msg {
		snap, err := decoder.Next()
		if err != nil {
			return DaemonLostMsg{Epoch: epoch}
		}
		// ... unchanged body
		return SnapshotMsg{Sessions: snap.Sessions, Epoch: epoch}
	}
}
```

Update the two call sites (`Init` and `handleSnapshot`) to pass `m.epoch`, and `Init`'s `renderTickCmd(1*time.Second)` to `renderTickCmd(1*time.Second, m.epoch)`.

- [ ] **Step 4: Fix the existing test that constructs a tick**

`internal/model/client_test.go:290` (`TestRenderTickStopsWhenDaemonGone`) constructs `RenderTickMsg(time.Now())`. Change both call sites to `RenderTickMsg{Time: time.Now(), Epoch: m.epoch}`. Keep what the test asserts intact - it pins that the render tick stops when the daemon is gone, which is still true and still separate from the epoch guard.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/model/ -race -v`
Expected: PASS, all of it.

- [ ] **Step 6: Commit**

```bash
git add internal/model/
git commit -m "refactor(model): stamp polling ticks with a generation epoch"
```

---

## Task 4: Reconnect probe and daemon health

**Why:** The client's first-snapshot read deadline is cleared once the first snapshot arrives, so a daemon that is alive but silent freezes the TUI on stale data with no indicator. Fallback is one-way and permanent: nothing ever reconnects. With one client that is tolerable; with a panel per session it means one daemon restart permanently multiplies the `gh` budget by the number of panels.

**Files:**
- Modify: `internal/model/messages.go` (two new messages)
- Modify: `internal/model/client.go` (probe cmd)
- Modify: `internal/model/model.go` (`Model` struct, `New`, `Init`, `Update`, `handleSnapshot`, `handleDaemonLost`, `View`)
- Modify: `internal/view/statusbar.go` (health segment, segment budget)
- Test: `internal/model/reconnect_test.go` (new), `internal/view/statusbar_test.go` (new)

**Interfaces:**
- Consumes: `Model.epoch`, epoch-stamped ticks (task 3); `dialDaemon`, `firstSnapshotTimeout`, `listenDaemonCmd` (existing).
- Produces:
  - `type ProbeTickMsg struct { Epoch int }`
  - `type DaemonProbeResultMsg struct { Epoch int; Conn net.Conn; Decoder *protocol.Decoder }` - `Conn == nil` means the dial failed
  - `func probeTickCmd(epoch int) tea.Cmd`
  - `func dialDaemonCmd(path string, epoch int) tea.Cmd`
  - `func (m Model) daemonHealth() string` - `""`, `"no daemon"`, or `"daemon stale Ns"`
  - `func (m Model) staleAfter() time.Duration`
  - `Model.lastSnapshot time.Time`
  - `view.RenderStatusBar(sessions, filterState, sortMode, width int, health string) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/model/reconnect_test.go`:

```go
package model

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

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
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the discarded connection was left open")
	}
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
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the surplus connection was left open")
	}
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
	got, _ := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 1})
	if got.(Model).lastSnapshot.IsZero() {
		t.Error("a snapshot did not stamp lastSnapshot, so staleness can never be detected")
	}
}
```

Create `internal/view/statusbar_test.go`:

```go
package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func TestStatusBarShowsHealth(t *testing.T) {
	out := RenderStatusBar(nil, nil, session.SortCreated, 80, "daemon stale 9s")
	if !strings.Contains(out, "daemon stale 9s") {
		t.Errorf("health missing from %q", out)
	}
}

// TestStatusBarOmitsEmptyHealth covers both shapes an unguarded empty health
// segment would take: a doubled separator when another segment follows it, and
// a dangling one when nothing does. A session fixture is required for the first
// - with no sessions there is no following segment, and the doubled separator
// can never appear whether the guard exists or not.
func TestStatusBarOmitsEmptyHealth(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
	}
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 80, ""))
	if strings.Contains(out, "·  ·") {
		t.Errorf("empty health left a doubled separator in %q", out)
	}

	bare := strings.TrimRight(StripANSI(RenderStatusBar(nil, nil, session.SortCreated, 80, "")), " ")
	if strings.HasSuffix(bare, "·") {
		t.Errorf("empty health left a dangling separator in %q", bare)
	}
}

// TestStatusBarNeverExceedsItsWidth is the panel's requirement: lipgloss
// Width pads but does not truncate, so a status bar wider than the pane wraps
// and pushes every table row down by one.
func TestStatusBarNeverExceedsItsWidth(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
		{Name: "SC-2 two", Git: session.GitStatus{Branch: "b"}},
		{Name: "SC-3 three", Git: session.GitStatus{Branch: "c"}},
	}
	for _, width := range []int{20, 30, 40, 60, 80, 120} {
		out := RenderStatusBar(sessions, nil, session.SortAlpha, width, "no daemon")
		if strings.Contains(out, "\n") {
			t.Fatalf("width %d wrapped: %q", width, out)
		}
		if got := visibleLen(out); got != width {
			t.Errorf("width %d: rendered %d visible columns", width, got)
		}
	}
}

// TestStatusBarKeepsHealthOverStateCounts pins the priority at the real
// landscape panel width: 40 columns leaves room for the identity, the session
// count and the health segment, but not the state counts after them. Health is
// the segment worth that last slot.
//
// The arithmetic, so this test is maintainable: StatusBarStyle has
// Padding(0, 1), so the content budget is 38. "vigil" is 5, " · 1 sessions" is
// 13, " · no daemon" is 12, totalling 30. The next segment, " · 1 idle", costs
// 9 and would reach 39, so it is dropped.
func TestStatusBarKeepsHealthOverStateCounts(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
	}
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 40, "no daemon"))
	if !strings.Contains(out, "no daemon") {
		t.Errorf("health was dropped: %q", out)
	}
	if strings.Contains(out, "idle") {
		t.Errorf("a state count was kept ahead of health: %q", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/ ./internal/view/ -run 'Probe|Health|StaleAfter|LastSnapshot|StatusBar' -v`
Expected: FAIL to compile - `ProbeTickMsg`, `dialDaemonCmd`, `daemonHealth`, and the 5-argument `RenderStatusBar` do not exist.

- [ ] **Step 3: Write the implementation**

`internal/model/messages.go`:

```go
// ProbeTickMsg schedules the next attempt to reach the daemon. It only fires
// while self-polling: reaching a daemon that came up is what keeps one poller
// serving many clients instead of N clients each spending the gh budget.
type ProbeTickMsg struct {
	Epoch int
}

// DaemonProbeResultMsg reports one dial attempt. A nil Conn means the dial
// failed and probing should continue.
type DaemonProbeResultMsg struct {
	Epoch   int
	Conn    net.Conn
	Decoder *protocol.Decoder
}
```

That needs `net` and `internal/protocol` imports in `messages.go`.

`internal/model/client.go`:

```go
// daemonProbeInterval is how often a self-polling client tries the daemon
// socket again. Fallback is a supported mode, so this is not urgent; it just
// has to be short enough that a daemon restart does not leave a panel
// self-polling for minutes.
const daemonProbeInterval = 2 * time.Second

func probeTickCmd(epoch int) tea.Cmd {
	return tea.Tick(daemonProbeInterval, func(time.Time) tea.Msg {
		return ProbeTickMsg{Epoch: epoch}
	})
}

// dialDaemonCmd dials off the UI goroutine, where the 300ms connect timeout
// is allowed to block.
func dialDaemonCmd(path string, epoch int) tea.Cmd {
	return func() tea.Msg {
		conn, err := dialDaemon(path)
		if err != nil {
			return DaemonProbeResultMsg{Epoch: epoch}
		}
		return DaemonProbeResultMsg{
			Epoch:   epoch,
			Conn:    conn,
			Decoder: protocol.NewDecoder(conn),
		}
	}
}
```

`internal/model/model.go` - the field:

```go
	// lastSnapshot is when the most recent daemon snapshot was applied. A
	// daemon that is connected but silent is invisible without it.
	lastSnapshot time.Time
```

Health:

```go
// daemonHealth describes the state of the data source, for the status bar.
// Empty means nothing worth saying: either the daemon is feeding us or the
// TUI is self-polling, which is a supported mode and already announced by a
// notification when it starts. A panel says so out loud, because N panels
// self-polling is the one arrangement that actually costs something.
func (m Model) daemonHealth() string {
	if m.daemonConn == nil {
		if m.panelMode {
			return "no daemon"
		}
		return ""
	}
	if !m.daemonReady {
		return ""
	}
	if age := time.Since(m.lastSnapshot); age > m.staleAfter() {
		return fmt.Sprintf("daemon stale %ds", int(age.Seconds()))
	}
	return ""
}

// staleAfter is how long a connected daemon may stay silent before the status
// bar says so: three poll cycles, never less than 5s.
func (m Model) staleAfter() time.Duration {
	d := 3 * m.cfg.GetSettingDuration("tmux_interval")
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
```

`panelMode` does not exist until task 6. Add the field now, unused, with a comment pointing at task 6, so this task compiles and its tests pass:

```go
	// panelMode renders the compact per-session panel instead of the full
	// dashboard. Set by NewPanel.
	panelMode bool
```

`Update` gains two cases:

```go
	case ProbeTickMsg:
		if msg.Epoch != m.epoch || m.daemonConn != nil {
			return m, nil
		}
		return m, dialDaemonCmd(protocol.SocketPath(), m.epoch)

	case DaemonProbeResultMsg:
		return m.handleProbeResult(msg)
```

```go
// handleProbeResult installs a reconnected daemon, or keeps probing. Bumping
// the epoch is what retires the self-poll loops that were running while the
// daemon was away.
func (m Model) handleProbeResult(msg DaemonProbeResultMsg) (tea.Model, tea.Cmd) {
	if msg.Conn == nil {
		if msg.Epoch != m.epoch || m.daemonConn != nil {
			return m, nil
		}
		return m, probeTickCmd(m.epoch)
	}
	if msg.Epoch != m.epoch || m.daemonConn != nil {
		// Retired generation, or a connection we no longer need. Dropping it
		// on the floor would leak an fd and a daemon-side writer goroutine.
		_ = msg.Conn.Close()
		return m, nil
	}

	// Bound the wait for the first snapshot exactly as New does: a daemon
	// whose poll is failing has nothing to send, and handleSnapshot clears
	// the deadline once something arrives.
	_ = msg.Conn.SetReadDeadline(time.Now().Add(firstSnapshotTimeout))
	m.epoch++
	m.daemonConn = msg.Conn
	m.daemonDecoder = msg.Decoder
	m.daemonReady = false
	m.addNotification("daemon back, streaming snapshots", "info")

	return m, tea.Batch(
		listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch),
		renderTickCmd(1*time.Second, m.epoch),
	)
}
```

`handleSnapshot` stamps the clock, right after the `daemonReady` block:

```go
	m.lastSnapshot = time.Now()
```

`handleDaemonLost` starts probing alongside the fallback loops:

```go
	return m, tea.Batch(
		m.fetchTmuxCmd(),
		m.fetchGitCmd(),
		tmuxTickCmd(1*time.Second, m.epoch),
		gitTickCmd(m.cfg.GetSettingDuration("git_interval"), m.epoch),
		prTickCmd(m.cfg.GetSettingDuration("pr_interval"), m.epoch),
		probeTickCmd(m.epoch),
	)
```

`Init`'s self-polling branch does the same, so a client that started with no daemon at all also finds one when it appears:

```go
	cmds = append(cmds,
		m.fetchTmuxCmd(),
		m.fetchGitCmd(),
		tmuxTickCmd(1*time.Second, m.epoch),
		gitTickCmd(m.cfg.GetSettingDuration("git_interval"), m.epoch),
		prTickCmd(m.cfg.GetSettingDuration("pr_interval"), m.epoch),
		probeTickCmd(m.epoch),
	)
```

`internal/view/statusbar.go` - rebuild it as a segment budget, so it can never exceed its width and the health segment outranks the state counts:

```go
// RenderStatusBar renders the top status bar. Segments are appended in
// priority order and any that does not fit the width is skipped, so a 40
// column panel gets the identity, the count and the health rather than a
// wrapped line. health is empty in the common case.
func RenderStatusBar(sessions []*session.Session, filterState *session.SessionState, sortMode session.SortMode, width int, health string) string {
	counts := make(map[session.SessionState]int)
	for _, s := range sessions {
		counts[s.State()]++
	}

	p := PlainOnBar()

	var b strings.Builder
	// budget is what StatusBarStyle leaves for content: it renders with
	// Padding(0, 1), and lipgloss counts padding inside Width.
	budget := width - 2
	used := 0

	// addSegment appends a separator and a segment together, and only if both
	// fit. Together, because a segment dropped on its own leaves a dangling
	// " · ". Only if it fits, because the alternative is truncating a rendered
	// string mid escape sequence.
	//
	// visibleLen rather than len: the separator is a multi-byte "·" and state
	// names may not stay ASCII forever.
	addSegment := func(text, rendered string) {
		const sep = " · "
		cost := visibleLen(sep) + visibleLen(text)
		if used+cost > budget {
			return
		}
		used += cost
		b.WriteString(p.Render(sep))
		b.WriteString(rendered)
	}

	if visibleLen("vigil") <= budget {
		used += visibleLen("vigil")
		b.WriteString(BoldOnBar().Render("vigil"))
	}

	countText := fmt.Sprintf("%d sessions", len(sessions))
	addSegment(countText, p.Render(countText))

	// Health outranks the state counts: in a 40 column panel it is the
	// segment worth the space.
	if health != "" {
		addSegment(health, OnBar(BrightYellow).Render(health))
	}

	for _, state := range session.AllStates() {
		n := counts[state]
		if n == 0 {
			continue
		}
		text := fmt.Sprintf("%d %s", n, state)
		if state == session.Done || state == session.Idle {
			addSegment(text, FaintOnBar().Render(text))
		} else {
			addSegment(text, OnBar(StateColor[state]).Render(text))
		}
	}

	if filterState != nil {
		text := fmt.Sprintf("filter: %s", *filterState)
		addSegment(text, OnBar(StateColor[*filterState]).Render(text))
	}

	if sortMode != session.SortCreated {
		text := fmt.Sprintf("sort: %s", sortMode)
		addSegment(text, FaintOnBar().Render(text))
	}

	return StatusBarStyle.Width(width).Render(b.String())
}
```

This changes the dashboard's status bar text by one detail: the session count used to read ` · 12 sessions` after `vigil` with the separator baked into the same string, and now goes through `addSegment`. The rendered result is identical.

Finally, update `View`'s call: `view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width, m.daemonHealth())`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/model/ ./internal/view/ -race -v`
Expected: PASS.

- [ ] **Step 5: Verify no double-poll after a reconnect cycle**

This is the failure mode the epoch exists to prevent, and it is invisible to a unit test of a single message. Confirm it by inspection, in this order:
1. `Init` (self-poll branch) → `probeTickCmd(m.epoch)` scheduled once.
2. `handleProbeResult` with a live conn → `m.epoch++` before `listenDaemonCmd` and `renderTickCmd`.
3. The next `TmuxTickMsg`, born in the old epoch, hits the guard in `Update` and returns `m, nil`.
Write down which line does each in the commit message.

- [ ] **Step 6: Commit**

```bash
git add internal/model/ internal/view/statusbar.go internal/view/statusbar_test.go
git commit -m "feat(model): reconnect to the daemon and surface a stale one"
```

---

## Task 5: Width-responsive table layout

**Why:** A 40-column panel cannot render a 104-column table. lipgloss `Width` pads but does not truncate, so an over-wide row wraps and every subsequent row shifts. The table must drop columns as width shrinks - and must render byte-identically to today at width >= 104, which is the only width the TUI has ever run at.

**Files:**
- Create: `internal/view/layout.go`
- Modify: `internal/view/table.go:11-77` (column constants, `RenderTable`, `renderRow`)
- Test: `internal/view/layout_test.go` (new), `internal/view/table_test.go` (new)

**Interfaces:**
- Produces:
  - `type TableLayout struct { Indicator bool; Index bool; State bool; Name int; Git int; PR int }` - the name is never dropped; everything else can be
  - `func LayoutForWidth(width int) TableLayout`, defined for `width >= 1`
  - The tiers, which the tests below encode. `fixed` is every column but the name, separators included:

    | Applies when | Columns | fixed | Name |
    |---|---|---|---|
    | width >= 60 | indicator, index, state, name, git, pr | 52 | `clamp(width-52, 8, 52)` |
    | width >= 41 | indicator, index, state, name, pr | 33 | `clamp(width-33, 8, 52)` |
    | width >= 28 | indicator, state, name, pr(12) | 20 | `clamp(width-20, 8, 52)` |
    | width >= 15 | indicator, state, name | 7 | `clamp(width-7, 8, 52)` |
    | width >= 4 | state, name | 3 | `clamp(width-3, 1, 52)` |
    | width < 4 | name | 0 | `clamp(width, 1, 52)` |
  - `func (l TableLayout) Total() int`
  - `func TruncateVisible(s string, width int) string` - ANSI-aware, resets style if it cuts
  - `func truncateName(name string, width int) string` - rune-based with an ellipsis
  - `func VisibleWidth(s string) int` and `func StripANSI(s string) string` - exported, because `internal/model`'s panel tests must assert that a rendered panel fits its pane and must not reimplement "how many columns is this on screen"
- `RenderTable`'s signature does not change. Callers are unaffected.

- [ ] **Step 1: Write the failing tests**

Create `internal/view/layout_test.go`:

```go
package view

import (
	"strings"
	"testing"
)

// TestLayoutMatchesTodayAtFullWidth is the regression pin for the TUI: at the
// width the dashboard has always run at, nothing about the geometry moves.
func TestLayoutMatchesTodayAtFullWidth(t *testing.T) {
	for _, width := range []int{104, 140, 200} {
		l := LayoutForWidth(width)
		if !l.Indicator || !l.Index {
			t.Errorf("width %d dropped a column", width)
		}
		if l.Name != 52 {
			t.Errorf("width %d: name column is %d, want 52 (unchanged from today)", width, l.Name)
		}
		if l.Git != 18 || l.PR != 22 {
			t.Errorf("width %d: git/pr are %d/%d, want 18/22", width, l.Git, l.PR)
		}
	}
}

func TestLayoutShrinksNameBeforeDroppingColumns(t *testing.T) {
	l := LayoutForWidth(80)
	if l.Git == 0 || l.PR == 0 {
		t.Fatal("width 80 dropped a column instead of shrinking the name")
	}
	if l.Name != 28 {
		t.Errorf("got name %d, want 28 (80 - 52)", l.Name)
	}
}

func TestLayoutDropsGitBeforePR(t *testing.T) {
	l := LayoutForWidth(50)
	if l.Git != 0 {
		t.Error("git survived at width 50")
	}
	if l.PR == 0 {
		t.Error("PR was dropped before git")
	}
}

// TestLayoutDropsIndexAndShrinksPRWhenNarrow uses 40, the default width of
// the landscape panel, so this is the layout the feature is actually judged on.
func TestLayoutDropsIndexAndShrinksPRWhenNarrow(t *testing.T) {
	l := LayoutForWidth(40)
	if l.Index {
		t.Error("the index column survived at width 40")
	}
	if l.PR != 12 {
		t.Errorf("got PR %d, want the compact 12", l.PR)
	}
	if l.Name != 20 {
		t.Errorf("got name %d, want 20 (40 - 20)", l.Name)
	}
}

func TestLayoutDropsPRWhenVeryNarrow(t *testing.T) {
	l := LayoutForWidth(20)
	if l.PR != 0 {
		t.Error("PR survived at width 20")
	}
	if !l.Indicator || !l.State {
		t.Error("the indicator or state dot was dropped before PR")
	}
	if l.Name != 13 {
		t.Errorf("got name %d, want 13 (20 - 7)", l.Name)
	}
}

func TestLayoutKeepsANameAtAnyWidth(t *testing.T) {
	for _, width := range []int{1, 4, 8, 11, 12, 26, 43, 62} {
		l := LayoutForWidth(width)
		if l.Name < 1 {
			t.Errorf("width %d: name column collapsed to %d", width, l.Name)
		}
	}
}

// TestLayoutDropsTheStateDotOnlyAsALastResort: under four columns there is
// nothing to spend on a dot and a separator.
func TestLayoutDropsTheStateDotOnlyAsALastResort(t *testing.T) {
	if l := LayoutForWidth(4); !l.State {
		t.Error("the state dot was dropped at width 4, where it still fits")
	}
	if l := LayoutForWidth(3); l.State {
		t.Error("the state dot survived at width 3, where it cannot fit")
	}
}

// TestLayoutNeverExceedsItsWidth is the invariant the panel depends on. A
// total wider than the pane wraps, and one wrapped row shifts every row under
// it for as long as the panel is open.
func TestLayoutNeverExceedsItsWidth(t *testing.T) {
	for width := 1; width <= 220; width++ {
		if got := LayoutForWidth(width).Total(); got > width {
			t.Fatalf("width %d: layout totals %d", width, got)
		}
	}
}

// --- truncation ---

func TestTruncateVisibleKeepsShortStrings(t *testing.T) {
	if got := TruncateVisible("abc", 10); got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}

func TestTruncateVisibleCountsVisibleColumnsOnly(t *testing.T) {
	styled := "\x1b[32m#1234\x1b[0m ✓"
	got := TruncateVisible(styled, 5)
	if visibleLen(got) != 5 {
		t.Errorf("got %d visible columns from %q, want 5", visibleLen(got), got)
	}
}

func TestTruncateVisibleResetsStyleWhenItCuts(t *testing.T) {
	got := TruncateVisible("\x1b[32m#1234 ✓", 3)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("%q ends mid-style: the color bleeds into the rest of the row", got)
	}
}

func TestTruncateVisibleZeroWidth(t *testing.T) {
	if got := TruncateVisible("\x1b[32mabc", 0); visibleLen(got) != 0 {
		t.Errorf("got %q, want no visible output", got)
	}
}

func TestTruncateNameAddsAnEllipsis(t *testing.T) {
	got := truncateName("SC-190583 a very long story title indeed", 12)
	if visibleLen(got) != 12 {
		t.Errorf("got %d columns from %q, want 12", visibleLen(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("%q does not show it was cut", got)
	}
}

func TestTruncateNameLeavesShortNames(t *testing.T) {
	if got := truncateName("SC-1 short", 40); got != "SC-1 short" {
		t.Errorf("got %q, want it untouched", got)
	}
}
```

Create `internal/view/table_test.go`:

```go
package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func tableFixture() []*session.Session {
	return []*session.Session{
		{
			Name:    "SC-190583 Emit Datadog metrics for investigation",
			Git:     session.GitStatus{Branch: "feature/metrics", Modified: 3, Unpushed: 1},
			PR:      &session.PRStatus{Number: 4521, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"},
			HasBell: true,
		},
		{
			Name: "SC-2 short one",
			Git:  session.GitStatus{Branch: "feature/short"},
		},
	}
}

// TestTableNeverExceedsItsWidth is the panel's hard requirement.
func TestTableNeverExceedsItsWidth(t *testing.T) {
	for _, width := range []int{12, 20, 26, 30, 40, 43, 50, 62, 80, 104, 200} {
		out := RenderTable(tableFixture(), 0, map[string]bool{}, 86400, width, 2, "")
		for i, line := range strings.Split(out, "\n") {
			if got := visibleLen(line); got > width {
				t.Errorf("width %d line %d: %d visible columns\n%q", width, i, got, line)
			}
		}
	}
}

// TestTableKeepsNameColumnPinnedAtFullWidth pins the TUI's appearance: the
// name column stays 52 wide, so the git column starts at the same offset at
// 104 columns as at 200. Stretching the name to fill the pane would fling git
// and PR out to the right edge of a wide terminal.
//
// The cursor is put on row 1 so row 0, the one being measured, carries no
// cursor styling to unpick. Row 0 is the fixture session that has git data.
func TestTableKeepsNameColumnPinnedAtFullWidth(t *testing.T) {
	const wantOffset = 63 // 3 + 1 + 2 + 1 + 2 + 1 + 52 + 1
	for _, width := range []int{104, 200} {
		out := RenderTable(tableFixture(), 1, map[string]bool{}, 86400, width, 2, "")
		row := StripANSI(strings.Split(out, "\n")[0])
		if gitAt := strings.Index(row, "~3"); gitAt != wantOffset {
			t.Errorf("width %d: git column starts at %d, want %d", width, gitAt, wantOffset)
		}
	}
}

func TestTableDropsGitInAPanelWidthRow(t *testing.T) {
	out := RenderTable(tableFixture(), 1, map[string]bool{}, 86400, 40, 2, "")
	if strings.Contains(StripANSI(out), "~3") {
		t.Error("git data rendered at width 40, where the git column is dropped")
	}
	if !strings.Contains(StripANSI(out), "#4521") {
		t.Error("the PR number was dropped at width 40, where it should survive")
	}
}

func TestTableRendersCursorRowWithinWidth(t *testing.T) {
	for _, width := range []int{20, 40, 104} {
		out := RenderTable(tableFixture(), 0, map[string]bool{}, 86400, width, 2, "")
		cursorRow := strings.Split(out, "\n")[0]
		if got := visibleLen(cursorRow); got > width {
			t.Errorf("width %d: cursor row is %d visible columns", width, got)
		}
	}
}
```

`StripANSI` already exists in `notification.go` (task 4 added it there). Use it; do not redeclare it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/view/ -run 'Layout|Truncate|Table' -v`
Expected: FAIL to compile - `LayoutForWidth`, `TruncateVisible`, `truncateName` are undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/view/layout.go`:

```go
package view

import "strings"

// Column widths. colName is a cap, not a fixed width: the name column shrinks
// with the pane but never stretches past this, so the dashboard at 200 columns
// looks exactly as it does at 104 rather than flinging the git and PR columns
// out to the right edge.
const (
	colIndicator = 3
	colIndex     = 2
	colState     = 2
	colName      = 52
	colGit       = 18
	colPR        = 22

	// colPRCompact is what survives when a panel is too narrow for the full
	// PR column. ColorizePR renders the number and check state first, so
	// truncating to this keeps what matters and drops the review icons.
	colPRCompact = 12

	// nameMin is the narrowest name column worth rendering. Below it, drop a
	// column instead: "SC-1902…" plus a PR number beats four characters of
	// name plus everything else.
	nameMin = 8
	sep     = 1
)

// TableLayout is which columns fit a given width and how wide each is. Only
// the name column is never dropped.
type TableLayout struct {
	Indicator bool
	Index     bool
	State     bool
	Name      int
	Git       int
	PR        int
}

// Total is the exact number of columns a row will occupy. Rows wider than the
// pane wrap, and one wrapped row shifts every row below it for as long as the
// panel is open, so tests assert this against the requested width at every
// width from 1 up.
func (l TableLayout) Total() int {
	total := l.Name
	if l.Indicator {
		total += colIndicator + sep
	}
	if l.Index {
		total += colIndex + sep
	}
	if l.State {
		total += colState + sep
	}
	if l.Git > 0 {
		total += sep + l.Git
	}
	if l.PR > 0 {
		total += sep + l.PR
	}
	return total
}

// LayoutForWidth picks the widest layout that fits, for width >= 1. Columns
// drop in reverse order of what a glance needs: git first, then the quick-jump
// index (with the PR column going compact at the same time), then the PR, then
// the indicator, and the state dot only when there is nothing left to give.
//
// Each tier's floor is fixed+nameMin, which is what keeps Total() <= width: a
// tier is only chosen when its own fixed cost plus the smallest useful name
// already fits.
func LayoutForWidth(width int) TableLayout {
	// fixed is every column but the name, including every separator: one
	// between each pair of columns, so the name contributes a separator on
	// both sides when a column follows it.
	const (
		fullFixed    = colIndicator + sep + colIndex + sep + colState + sep + sep + colGit + sep + colPR // 52
		noGitFixed   = colIndicator + sep + colIndex + sep + colState + sep + sep + colPR                // 33
		compactFixed = colIndicator + sep + colState + sep + sep + colPRCompact                          // 20
		noPRFixed    = colIndicator + sep + colState + sep                                               // 7
		bareFixed    = colState + sep                                                                    // 3
	)

	switch {
	case width >= fullFixed+nameMin: // 60
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-fullFixed, nameMin, colName),
			Git:  colGit, PR: colPR,
		}
	case width >= noGitFixed+nameMin: // 41
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-noGitFixed, nameMin, colName),
			PR:   colPR,
		}
	case width >= compactFixed+nameMin: // 28
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-compactFixed, nameMin, colName),
			PR:   colPRCompact,
		}
	case width >= noPRFixed+nameMin: // 15
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-noPRFixed, nameMin, colName),
		}
	case width >= bareFixed+1: // 4
		return TableLayout{State: true, Name: clamp(width-bareFixed, 1, colName)}
	default:
		return TableLayout{Name: clamp(width, 1, colName)}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// TruncateVisible cuts s to width visible columns, ignoring ANSI escapes and
// closing any style it cuts through so the color does not bleed into the rest
// of the row.
func TruncateVisible(s string, width int) string {
	if visibleLen(s) <= width {
		return s
	}
	var b strings.Builder
	visible, cut := 0, false
	inEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\x1b' {
			inEscape = true
			b.WriteByte(c)
			continue
		}
		if inEscape {
			b.WriteByte(c)
			if c == 'm' {
				inEscape = false
			}
			continue
		}
		if visible >= width {
			cut = true
			break
		}
		if c < 0x80 || c >= 0xC0 {
			visible++
		}
		b.WriteByte(c)
	}
	if cut {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// truncateName cuts an unstyled session name, marking the cut. Names come
// straight from tmux and carry no escapes, so this counts runes.
func truncateName(name string, width int) string {
	runes := []rune(name)
	if len(runes) <= width {
		return name
	}
	if width <= 1 {
		return string(runes[:max(0, width)])
	}
	return string(runes[:width-1]) + "…"
}

// VisibleWidth returns how many terminal columns s occupies, ignoring ANSI
// escapes. Exported so callers rendering into a fixed-width pane - and the
// tests that check they did - have one definition of "what is actually on
// screen" to work from.
func VisibleWidth(s string) int {
	return visibleLen(s)
}
```

`StripANSI` already exists: task 4 needed it for the status bar tests and added it to `notification.go`, next to the `ansiPattern` it uses. Leave it there and do not redeclare it here.

`internal/view/table.go` - delete the old `const` block (its names move to `layout.go`) and render against the layout:

```go
// RenderTable renders the session table rows, dropping columns to fit width.
func RenderTable(sessions []*session.Session, cursor int, selected map[string]bool, staleThreshold int, width, height int, notification string) string {
	if len(sessions) == 0 {
		return DimStyle.Render("  No sessions")
	}

	layout := LayoutForWidth(width)

	var b strings.Builder
	rendered := 0
	for i, s := range sessions {
		if i >= height {
			break
		}
		line := renderRow(s, i, selected[s.Name], staleThreshold, width, i == cursor, layout)
		if rendered > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
		rendered++
	}
	// ... padding and notification block unchanged
}

func renderRow(s *session.Session, index int, selected bool, staleThreshold int, width int, isCursor bool, layout TableLayout) string {
	var bg *lipgloss.Color
	if isCursor {
		bg = &BarBg
	}

	name := truncateName(s.Name, layout.Name)

	var cells []string
	if layout.Indicator {
		cells = append(cells, IndicatorWithBg(s, selected, bg))
	}
	if layout.Index {
		cells = append(cells, indexCol(index, bg))
	}
	if layout.State {
		cells = append(cells, StateIndicatorWithBg(s, bg))
	}

	if isCursor {
		p := PlainOnBar()
		cells = append(cells, p.Render(name)+p.Render(strings.Repeat(" ", max(0, layout.Name-visibleLen(name)))))
		if layout.Git > 0 {
			git := TruncateVisible(GitColWithBg(s, staleThreshold, bg), layout.Git)
			cells = append(cells, git+p.Render(strings.Repeat(" ", max(0, layout.Git-visibleLen(git)))))
		}
		if layout.PR > 0 {
			cells = append(cells, TruncateVisible(PRColWithBg(s, bg), layout.PR))
		}
		return CursorStyle.Width(width).Render(strings.Join(cells, p.Render(" ")))
	}

	cells = append(cells, padRight(name, layout.Name))
	if layout.Git > 0 {
		cells = append(cells, padRight(TruncateVisible(GitColWithBg(s, staleThreshold, bg), layout.Git), layout.Git))
	}
	if layout.PR > 0 {
		cells = append(cells, TruncateVisible(PRColWithBg(s, bg), layout.PR))
	}
	return strings.Join(cells, " ")
}
```

`SessionName` in `format.go` is now unused by the table. Leave it: `internal/view/detail.go` may use it. Check with `grep -rn 'SessionName(' --include='*.go' .` and delete it only if nothing calls it.

`StripANSI` and `VisibleWidth` go in `layout.go`. Leave `clampNotification` alone - it already uses `ansiPattern` directly, and rewriting it to call `StripANSI` changes nothing.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/view/ -v`
Expected: PASS, including all nine pre-existing `TestClampNotification_*` cases.

- [ ] **Step 5: Confirm the TUI is unchanged by eye**

```bash
make build && ./vigil
```
Expected: identical to before at a normal terminal width. Resize the terminal narrow and watch columns drop without any row wrapping. `q` to quit.

- [ ] **Step 6: Commit**

```bash
git add internal/view/
git commit -m "feat(view): drop table columns progressively as width shrinks"
```

---

## Task 6: Panel mode

**Why:** This is the phase. A session gets the list of every session beside its Claude pane, opt-in, one key.

**Files:**
- Modify: `main.go:22-34` (`parseArgs`), `main.go:66-77` (dispatch), `main.go:88-105` (`printUsage`)
- Modify: `internal/model/model.go` (`New`, `NewPanel`, `newModel`, `View`, `panelView`, `handleKey`)
- Modify: `internal/model/client.go` (`spawnDaemon`)
- Test: `main_test.go` (extend), `internal/model/panel_test.go` (new)

**Interfaces:**
- Consumes: `Model.panelMode` and `daemonHealth` (task 4), `LayoutForWidth` and the 5-argument `RenderStatusBar` (tasks 4-5).
- Produces:
  - `func model.NewPanel(cfg *config.Config, cmd fetch.Commander) Model`
  - `func spawnDaemon() error` and `var daemonSpawner = spawnDaemon` (a var so tests can substitute)
  - `parseArgs` returns `"panel"` for `--panel`
  - `func (m Model) panelView() string`

- [ ] **Step 1: Write the failing tests**

Extend `main_test.go`'s table with:

```go
		{"panel flag", []string{"--panel"}, "panel"},
```

Create `internal/model/panel_test.go`:

```go
package model

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

func panelModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.panelMode = true
	m.detailOpen = false
	m.width, m.height = 40, 10
	m.sessions = []*session.Session{
		{Name: "SC-1 alpha", Git: session.GitStatus{Branch: "feature/a", Modified: 2}},
		{Name: "SC-2 beta", Git: session.GitStatus{Branch: "feature/b"}},
	}
	return m
}

// TestPanelViewFitsItsPane is the requirement that makes a panel usable at
// all: exactly the pane's height, never wider than the pane.
func TestPanelViewFitsItsPane(t *testing.T) {
	m := panelModel(t)
	for _, size := range [][2]int{{40, 10}, {80, 12}, {24, 6}, {200, 10}} {
		m.width, m.height = size[0], size[1]
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Errorf("%dx%d: rendered %d lines", m.width, m.height, len(lines))
		}
		for i, line := range lines {
			if got := view.VisibleWidth(line); got > m.width {
				t.Errorf("%dx%d line %d: %d visible columns", m.width, m.height, i, got)
			}
		}
	}
}

// TestPanelViewHasNoFooter pins that the panel spends its rows on sessions.
func TestPanelViewHasNoFooter(t *testing.T) {
	out := panelModel(t).View()
	for _, help := range []string{"merge", "approve", "cleanup", "rebase"} {
		if strings.Contains(out, help) {
			t.Errorf("panel rendered the keybinding footer (%q)", help)
		}
	}
}

func TestPanelViewShowsNoDaemonWhenSelfPolling(t *testing.T) {
	if !strings.Contains(view.StripANSI(panelModel(t).View()), "no daemon") {
		t.Error("a self-polling panel gave no indication the daemon is down")
	}
}

// TestPanelIgnoresToggleDetail: the detail panel would swallow a 10-row strip
// whole, and its pane captures would run once per panel per tick.
func TestPanelIgnoresToggleDetail(t *testing.T) {
	m := panelModel(t)
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got.(Model).detailOpen {
		t.Error("tab opened the detail panel in a panel")
	}
}

func TestNewPanelSetsPanelMode(t *testing.T) {
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	m := NewPanel(&config.Config{}, fetch.NewMockCommander())
	if !m.panelMode {
		t.Error("NewPanel did not set panelMode")
	}
	if m.detailOpen {
		t.Error("NewPanel left the detail panel open")
	}
}

// TestNewPanelSpawnsTheDaemon is the reason task 1 exists: N panels starting
// at once all try this, and the flock is what makes that safe.
func TestNewPanelSpawnsTheDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t)) // no socket to dial
	NewPanel(&config.Config{}, fetch.NewMockCommander())
	if spawned != 1 {
		t.Errorf("spawned %d daemons, want 1", spawned)
	}
}

func TestNewDoesNotSpawnTheDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
	New(&config.Config{}, fetch.NewMockCommander())
	if spawned != 0 {
		t.Errorf("the TUI spawned %d daemons; only panels do that", spawned)
	}
}

// TestPanelRespawnsARateLimitedDaemon keeps a crashed daemon from leaving
// every panel self-polling forever, without letting a panel fork in a loop.
func TestPanelRespawnsARateLimitedDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	m := panelModel(t)
	m.epoch = 1
	m.lastSpawn = time.Now().Add(-time.Hour)

	got, _ := m.Update(DaemonProbeResultMsg{Epoch: 1})
	if spawned != 1 {
		t.Fatalf("spawned %d, want 1 after a failed probe", spawned)
	}
	next := got.(Model)
	if _, _ = next.Update(DaemonProbeResultMsg{Epoch: next.epoch}); spawned != 1 {
		t.Errorf("spawned %d, want the second attempt rate-limited", spawned)
	}
}
```

`visibleWidth` and `stripEscapes` in those tests are `view.VisibleWidth` and `view.StripANSI`, exported by task 5 for exactly this. Do not write local copies: a second definition of "how wide is this on screen" is a second thing to get wrong. Import `internal/view` in `panel_test.go` and use:

```go
		if got := view.VisibleWidth(line); got > m.width {
```

```go
	if !strings.Contains(view.StripANSI(panelModel(t).View()), "no daemon") {
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'Panel|ParseArgs|Spawn' -v`
Expected: FAIL to compile - `NewPanel`, `daemonSpawner`, `Model.lastSpawn` are undefined, and `parseArgs` rejects `--panel`.

- [ ] **Step 3: Write the implementation**

`main.go`:

```go
	case "--panel":
		return "panel", nil
```

```go
	case "panel":
		if err := runPanel(cfg, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
			os.Exit(1)
		}
```

```go
// runPanel renders the compact session list for a single tmux pane. It shares
// every code path with the dashboard, so panel and dashboard can never
// disagree about state.
func runPanel(cfg *config.Config, cmd fetch.Commander) error {
	p := tea.NewProgram(model.NewPanel(cfg, cmd), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
```

And in `printUsage`:

```go
	_, _ = fmt.Fprintln(w, "  vigil --panel    Run the compact session list for a tmux pane")
```

`internal/model/model.go` - split the constructor:

```go
// New creates a Model for the full dashboard.
func New(cfg *config.Config, cmd fetch.Commander) Model {
	return newModel(cfg, cmd, false)
}

// NewPanel creates a Model for a session's panel: a compact, always-on
// session list in a tmux pane. A panel starts the daemon if none is running,
// because a panel per session self-polling would multiply the gh budget by
// the number of open sessions. Startup races between panels are safe: the
// daemon serializes on an flock and every loser exits immediately.
func NewPanel(cfg *config.Config, cmd fetch.Commander) Model {
	return newModel(cfg, cmd, true)
}

func newModel(cfg *config.Config, cmd fetch.Commander, panel bool) Model {
	// ... the existing body of New, with these differences:
	m := Model{
		// ...
		panelMode:  panel,
		detailOpen: !panel,
		// ...
	}
	// ... cache load unchanged ...

	if conn, err := dialDaemon(protocol.SocketPath()); err == nil {
		_ = conn.SetReadDeadline(time.Now().Add(firstSnapshotTimeout))
		m.daemonConn = conn
		m.daemonDecoder = protocol.NewDecoder(conn)
	} else if panel {
		m.spawnDaemonOnce()
	}

	return m
}

// spawnDaemonOnce starts a daemon at most once every spawnCooldown, so a
// daemon that refuses to stay up cannot turn a panel into a fork loop.
func (m *Model) spawnDaemonOnce() {
	if time.Since(m.lastSpawn) < spawnCooldown {
		return
	}
	m.lastSpawn = time.Now()
	if err := daemonSpawner(); err != nil {
		m.addNotification("could not start daemon: "+err.Error(), "warning")
	}
}
```

New fields and const:

```go
	// lastSpawn is when this panel last tried to start a daemon.
	lastSpawn time.Time
```

```go
// spawnCooldown is the floor between two attempts by one panel to start a
// daemon.
const spawnCooldown = 15 * time.Second
```

The failed-probe branch of `handleProbeResult` (task 4) gains the panel retry:

```go
	if msg.Conn == nil {
		if msg.Epoch != m.epoch || m.daemonConn != nil {
			return m, nil
		}
		if m.panelMode {
			m.spawnDaemonOnce()
		}
		return m, probeTickCmd(m.epoch)
	}
```

`View` branches at the top, and the notification selection moves into a helper both branches use:

```go
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.panelMode {
		return m.panelView()
	}
	// ... existing body, with the notification block replaced by
	// notif := m.activeNotification()
}

// panelView renders the compact panel: a status bar and as many session rows
// as the pane has left. No footer and no detail panel - the rows are what the
// pane is for, and the detail panel's pane captures would run once per panel
// per tick.
func (m Model) panelView() string {
	statusBar := view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width, m.daemonHealth())
	table := view.RenderTable(
		m.visibleSessions(),
		m.cursor,
		m.selected,
		m.cfg.GetSettingInt("stale_threshold"),
		m.width,
		max(1, m.height-1),
		m.activeNotification(),
	)
	return lipgloss.JoinVertical(lipgloss.Left, statusBar, table)
}

// activeNotification returns the newest unexpired notification, styled, or
// "". Expiry is evaluated at render time, which is why both paths keep a 1s
// repaint cadence.
func (m Model) activeNotification() string {
	now := time.Now()
	for i := len(m.notifications) - 1; i >= 0; i-- {
		n := m.notifications[i]
		if !now.Before(n.Expires) {
			continue
		}
		style := lipgloss.NewStyle().Padding(0, 1)
		switch n.Severity {
		case "error":
			style = style.Foreground(view.BrightRed)
		case "warning":
			style = style.Foreground(view.BrightYellow)
		}
		return style.Render(n.Text)
	}
	return ""
}
```

`handleKey` ignores the detail toggle in panel mode:

```go
	case key.Matches(msg, keys.ToggleDetail):
		if m.panelMode {
			return m, nil
		}
		// ... existing body
```

Also check `handleKey`'s `CycleDetailMode` case: it is meaningless with the detail panel closed. Leave it - it mutates `detailMode` and nothing renders it. Do not add a second guard for it.

`internal/model/client.go` - the spawn:

```go
// daemonSpawner is the indirection tests replace; production always uses
// spawnDaemon.
var daemonSpawner = spawnDaemon

// spawnDaemon starts `vigil daemon` detached from this process, so it outlives
// the pane that started it. Its output goes to a log file beside the socket:
// the daemon is silent when healthy, and when it is not, that log is the only
// place the reason survives.
func spawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(protocol.SocketPath()), "vigild.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid detaches it from this pane's process group, so closing the pane
	// or the tmux session does not take the daemon with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it if it exits, rather than leaving a zombie for the life of a
	// long-running panel.
	go func() { _ = cmd.Wait() }()
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -race -v`
Expected: PASS.

- [ ] **Step 5: Run it for real**

```bash
make build
./vigil --panel
```
Expected: a compact list, no footer, fitting the terminal. Then narrow the terminal to ~40 columns and confirm columns drop without wrapping. With no daemon running, the status bar reads `no daemon` at first and the indicator clears within a few seconds as the spawned daemon comes up and the probe connects.

Confirm the spawn actually happened and detached:

```bash
pgrep -fl 'vigil daemon'
cat "${XDG_RUNTIME_DIR:-$HOME/.local/state}/vigil/vigild.log"
```

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go internal/model/
git commit -m "feat(panel): add vigil --panel with an on-demand daemon"
```

---

## Task 7: Documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md` (the carry-forward section)

- [ ] **Step 1: Update `CLAUDE.md`**

In **Architecture**, replace the `internal/daemon` bullet with:

```markdown
- `internal/daemon/` - `vigil daemon`: runs one `Snapshot` per tick at `tmux_interval` (default 1s) so tmux metadata (including bell flags) is never more than a tick stale; git state is gated inside `Snapshot` on `git_interval` (default 3s) and PR state per branch on `pr_interval` (default 30s), each via its own memo. Startup serializes on an flock'd lock file beside the socket (`vigild.sock.lock`), held across the stale-socket removal and the bind, so racing daemons cannot both bind. Every client gets its own writer goroutine and a one-deep latest-wins queue, so a client that stops reading can neither stall the poll loop nor block new connections
```

Add to **Architecture**:

```markdown
- `vigil --panel` - the same `Model` with `panelMode` set: compact status bar, width-responsive table, no detail panel and no footer. A panel starts the daemon if none is running; the dashboard does not
```

Add to **Key Conventions**:

```markdown
- The table drops columns as width shrinks (`view.LayoutForWidth`). At width >= 104 the layout is exactly what it always was: the name column is capped at 52 and never stretches
- Every self-rescheduling tick carries an `epoch`. Bubble Tea ticks cannot be cancelled, so switching between daemon snapshots and self-polling bumps the epoch and the previous generation's ticks retire themselves
- A client that loses the daemon self-polls and probes the socket every 2s until it is back. A connected but silent daemon shows `daemon stale Ns` in the status bar after three poll intervals
- Panel geometry is tmux's concern, not vigil's: the `prefix p` toggle script measures the client and splits. vigil only renders to fit its pane
```

- [ ] **Step 2: Update the spec's carry-forward section**

In `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`, under "Must be resolved before phase 2 ships", mark each of the three resolved with the task that did it, and move the two remaining items from the phase-1 handoff that phase 2 did not address into a "Still open after phase 2" list:

- Collapsing the TUI's self-polling onto `internal/collect` (still duplicated, still able to drift).
- Lazy review-thread fetching (the daemon still spends two GraphQL calls per PR per cycle).
- The daemon-up versus daemon-down TUI comparison was never run as a timing observation.
- A permanently failing `gh` still shows the last known PR indefinitely with no staleness marker. The new marker covers a silent *daemon*, not a silent `gh`.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/
git commit -m "docs: record the phase 2 daemon, panel, and layout changes"
```

---

## Task 8: The tmux toggle

**Why:** `vigil --panel` is inert without a way to put it in a pane. tmux decides placement and vigil renders to fit; keeping those separate is what makes the responsive behavior simple.

**Repository: `~/dotfiles`.** Branch off `master`.

**Files:**
- Create: `scripts/scripts/vigil-panel`
- Create: `scripts/scripts/tests/vigil_panel.bats`
- Modify: `scripts/scripts/tests/stubs/tmux`
- Modify: `scripts/scripts/Makefile` (`SHELL_SCRIPTS`)
- Modify: `tmux/.tmux.conf`

**Interfaces:**
- Produces a script with `panel_pane`, `panel_geometry`, and `main`, sourceable for unit tests and runnable end to end.
- tmux user options, which are the config surface for geometry: `@vigil_panel` (per-pane marker, set by the script), `@vigil_panel_orientation` (`auto` default, or `top` / `left`), `@vigil_panel_size` (columns for `left`, rows for `top`).
- Geometry rule: portrait when `client_height * 2 > client_width` → `split-window -vb -l 10`; otherwise `split-window -hb -l 40`.

- [ ] **Step 1: Extend the tmux stub**

`scripts/scripts/tests/stubs/tmux` - add canned responses for the three queries the toggle makes. Keep the existing cases untouched:

```bash
case "${1:-}" in
  display-message)
    printf '%s\n' "${TMUX_STUB_DISPLAY:-/tmp/stub-worktree}"
    ;;
  has-session)
    exit "${TMUX_STUB_HAS_SESSION:-1}"
    ;;
  list-panes)
    if [ -n "${TMUX_STUB_LIST_PANES:-}" ]; then
      printf '%s\n' "${TMUX_STUB_LIST_PANES}"
    fi
    ;;
  show-options)
    # Keyed by option name: the toggle asks for two different options and a
    # single canned value would feed an orientation into the size.
    for arg in "${@}"; do
      case "${arg}" in
        @vigil_panel_orientation) printf '%s\n' "${TMUX_STUB_PANEL_ORIENTATION:-}" ;;
        @vigil_panel_size) printf '%s\n' "${TMUX_STUB_PANEL_SIZE:-}" ;;
      esac
    done
    ;;
  split-window)
    # Only -P asks for the new pane id back.
    for arg in "${@}"; do
      if [ "${arg}" = "-P" ]; then
        printf '%s\n' "${TMUX_STUB_SPLIT_PANE:-%9}"
        break
      fi
    done
    ;;
esac
```

- [ ] **Step 2: Write the failing tests**

Create `scripts/scripts/tests/vigil_panel.bats`. These run the real script, not a stand-in: a fixture standing in for the script under test hides ordering bugs in that script.

```bash
#!/usr/bin/env bats

load helper

setup() {
  setup_tmux_stub
  PANEL="${BATS_TEST_DIRNAME}/../vigil-panel"
}

@test "a portrait client gets a strip across the top" {
  export TMUX_STUB_DISPLAY="40 60"
  run "${PANEL}"
  [ "${status}" -eq 0 ]
  run tmux_call_args "split-window"
  [[ "${output}" == *"-vb"* ]]
  [[ "${output}" == *"10"* ]]
}

@test "a landscape client gets a column on the left" {
  export TMUX_STUB_DISPLAY="40 200"
  run "${PANEL}"
  [ "${status}" -eq 0 ]
  run tmux_call_args "split-window"
  [[ "${output}" == *"-hb"* ]]
  [[ "${output}" == *"40"* ]]
}

@test "the boundary case counts as landscape" {
  # height*2 == width is not portrait: the rule is strictly greater.
  export TMUX_STUB_DISPLAY="50 100"
  run "${PANEL}"
  run tmux_call_args "split-window"
  [[ "${output}" == *"-hb"* ]]
}

@test "an orientation option overrides the measurement" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_PANEL_ORIENTATION="top"
  run "${PANEL}"
  run tmux_call_args "split-window"
  [[ "${output}" == *"-vb"* ]]
}

@test "a size option overrides the default" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_PANEL_SIZE="60"
  run "${PANEL}"
  run tmux_call_args "split-window"
  [[ "${output}" == *"60"* ]]
  [[ "${output}" != *$'\x1f'"40"* ]]
}

@test "the panel runs vigil in panel mode" {
  export TMUX_STUB_DISPLAY="40 200"
  run "${PANEL}"
  run tmux_call_args "split-window"
  [[ "${output}" == *"vigil --panel"* ]]
}

@test "the new pane is marked and set to close on exit" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_SPLIT_PANE="%7"
  run "${PANEL}"
  [ "${status}" -eq 0 ]
  run tmux_calls
  [[ "${output}" == *"@vigil_panel"* ]]
  [[ "${output}" == *"%7"* ]]
  [[ "${output}" == *"remain-on-exit"* ]]
}

@test "the split leaves focus where it was" {
  export TMUX_STUB_DISPLAY="40 200"
  run "${PANEL}"
  run tmux_call_args "split-window"
  [[ "${output}" == *"-d"* ]]
}

@test "toggling again kills the existing panel" {
  export TMUX_STUB_LIST_PANES="%1 0
%2 1"
  run "${PANEL}"
  [ "${status}" -eq 0 ]
  run tmux_call_args "kill-pane"
  [[ "${output}" == *"%2"* ]]
  run refute_tmux_subcommand "split-window"
  [ "${status}" -eq 0 ]
}

@test "a window with panes but no panel still splits" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_LIST_PANES="%1 0
%2 0"
  run "${PANEL}"
  [ "${status}" -eq 0 ]
  run assert_tmux_subcommand "split-window"
  [ "${status}" -eq 0 ]
  run refute_tmux_subcommand "kill-pane"
  [ "${status}" -eq 0 ]
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/vigil_panel.bats`
Expected: every test fails - `vigil-panel` does not exist.

- [ ] **Step 4: Write the script**

Create `scripts/scripts/vigil-panel`:

```bash
#!/usr/bin/env bash
#
# vigil-panel - toggle a vigil session panel in the current tmux window.
#
# tmux decides where the panel goes; vigil renders to fit whatever pane it
# lands in. Bound to prefix p / prefix C-p.
#
# Geometry is configured with tmux user options rather than vigil's config,
# because placement is tmux's concern and this script is the only reader:
#   @vigil_panel_orientation  auto (default) | top | left
#   @vigil_panel_size         rows for top (default 10), columns for left (40)

set -o errexit
set -o nounset
set -o pipefail

readonly PANEL_FLAG='@vigil_panel'
readonly VIGIL="${VIGIL_BIN:-vigil}"

#######################################
# Print the pane id of this window's panel, if it has one.
# Panes are found by their @vigil_panel marker rather than by position:
# splitting with -b inserts before the existing pane, so every index in the
# window shifts when a panel opens.
#######################################
panel_pane() {
  tmux list-panes -F "#{pane_id} #{${PANEL_FLAG}}" \
    | awk '$2 == "1" { print $1; exit }'
}

#######################################
# Print the split flag and size for the current client.
# Portrait (a vertical monitor) gets a wide strip across the top; anything
# else gets a narrow column on the left.
# Outputs:
#   e.g. "-hb 40"
#######################################
panel_geometry() {
  local orientation size height width
  orientation="$(tmux show-options -gqv "@vigil_panel_orientation")"
  size="$(tmux show-options -gqv "@vigil_panel_size")"
  read -r height width <<< "$(tmux display-message -p '#{client_height} #{client_width}')"

  if [ -z "${orientation}" ] || [ "${orientation}" = "auto" ]; then
    if [ "$((height * 2))" -gt "${width}" ]; then
      orientation="top"
    else
      orientation="left"
    fi
  fi

  case "${orientation}" in
    top) printf '%s %s\n' '-vb' "${size:-10}" ;;
    *)   printf '%s %s\n' '-hb' "${size:-40}" ;;
  esac
}

main() {
  local existing
  existing="$(panel_pane)"
  if [ -n "${existing}" ]; then
    tmux kill-pane -t "${existing}"
    return 0
  fi

  local split size pane
  read -r split size <<< "$(panel_geometry)"
  # -d keeps focus in the pane the binding was pressed in.
  pane="$(tmux split-window "${split}" -l "${size}" -d -P -F '#{pane_id}' "${VIGIL} --panel")"
  tmux set-option -p -t "${pane}" "${PANEL_FLAG}" 1
  # So a dead panel closes its pane instead of leaving a corpse in the layout.
  tmux set-option -p -t "${pane}" remain-on-exit off
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "${@}"
fi
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `bats tests/vigil_panel.bats && bats tests/`
Expected: PASS, and the pre-existing `tmux_lib.bats` and `route_lib.bats` still pass with the extended stub.

- [ ] **Step 6: Add it to lint and bind it**

`scripts/scripts/Makefile` - add `vigil-panel` to `SHELL_SCRIPTS`, alphabetically:

```make
	portal-open short-story-md shortcut-claim shortcut-implement shortcut-worktree \
	tmux-monitor ts vigil-panel worktree-status
```

`tmux/.tmux.conf` - beside the other script bindings, following the existing plain/ctrl pair convention:

```
bind-key p run-shell -b "$HOME/scripts/vigil-panel"
bind-key C-p run-shell -b "$HOME/scripts/vigil-panel"
```

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Try it for real**

```bash
tmux source-file ~/.tmux.conf
```
Then in a session with a Claude pane: `prefix p` opens the panel without stealing focus, `prefix p` again closes it. Check both geometries by resizing the terminal to portrait first. Confirm the panel keeps rendering when the pane is resized, and that killing the panel process (`tmux kill-pane -t <id>`) leaves no dead pane.

- [ ] **Step 8: Commit**

```bash
git add scripts/scripts/vigil-panel scripts/scripts/tests/ scripts/scripts/Makefile tmux/.tmux.conf
git commit -m "feat(tmux): toggle a vigil panel with prefix p"
```

---

## Task 9: Pane targeting by id

**Why:** `launch_claude_in_pane` targets `:claude.1` positionally and `setup_secondary_pane` targets `.2`. tmux pane indexes are positional, and the panel is inserted with `-b`, before the existing pane. Phase 2 does not trip this - Claude is launched at session creation, before any panel exists, and a re-dispatch returns early rather than relaunching - but phase 3 adds the panel *at* creation, which makes both targets wrong. Twenty lines now, while the context is fresh, instead of a mysterious SIGKILL later.

**Repository: `~/dotfiles`.**

**Files:**
- Modify: `scripts/scripts/lib/tmux.sh:79-107` (`launch_claude_in_pane`, `setup_secondary_pane`), `:124-166` (`create_tmux_session`)
- Modify: `scripts/scripts/tests/tmux_lib.bats`

**Interfaces:**
- Produces `claude_pane_target <session_name>`, printing a `pane_id` when the claude pane carries `@vigil_claude`, and falling back to `=<session>:claude.1` when it does not, so sessions created before this change keep working.

- [ ] **Step 1: Write the failing tests**

Add to `scripts/scripts/tests/tmux_lib.bats`:

```bash
@test "create_tmux_session marks the claude pane" {
  create_tmux_session "SC-1 demo" "/tmp/wt" true "" ""
  run tmux_calls
  [[ "${output}" == *"@vigil_claude"* ]]
}

@test "launch_claude_in_pane targets the marked pane by id" {
  export TMUX_STUB_LIST_PANES="%4 0
%5 1"
  launch_claude_in_pane "SC-1 demo" "/tmp/wt" "claude --model opus"
  run tmux_call_args "respawn-pane"
  [[ "${output}" == *"%5"* ]]
  [[ "${output}" != *":claude.1"* ]]
}

@test "launch_claude_in_pane falls back to the positional target for older sessions" {
  export TMUX_STUB_LIST_PANES=""
  launch_claude_in_pane "SC-1 demo" "/tmp/wt" "claude --model opus"
  run tmux_call_args "respawn-pane"
  [[ "${output}" == *"=SC-1 demo:claude.1"* ]]
}

@test "setup_secondary_pane sends its command to the pane it just made" {
  export TMUX_STUB_DISPLAY="120"
  export TMUX_STUB_SPLIT_PANE="%8"
  setup_secondary_pane "SC-1 demo" "nit"
  run tmux_call_args "send-keys"
  [[ "${output}" == *"%8"* ]]
  [[ "${output}" != *":claude.2"* ]]
}

@test "setup_secondary_pane returns focus to the claude pane by id" {
  export TMUX_STUB_DISPLAY="120"
  export TMUX_STUB_LIST_PANES="%4 1"
  setup_secondary_pane "SC-1 demo" "nit"
  run tmux_call_args "select-pane"
  [[ "${output}" == *"%4"* ]]
}
```

The existing test `launch_claude_in_pane respawns the pane instead of sending keys` asserts `=SC-1 demo:claude.1`. It keeps passing, because `setup_tmux_stub` leaves `TMUX_STUB_LIST_PANES` unset and the fallback fires. Leave it exactly as it is: it is now the fallback's regression test.

Note `TMUX_STUB_DISPLAY="120"` - `setup_secondary_pane` reads `#{window_width}` through `display-message`, which the stub answers with that one variable.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bats tests/tmux_lib.bats`
Expected: the five new tests fail; everything else passes.

- [ ] **Step 3: Write the implementation**

In `scripts/scripts/lib/tmux.sh`, add before `launch_claude_in_pane`:

```bash
#######################################
# Print a tmux target for a session's claude pane.
# Resolved by the @vigil_claude marker rather than by position: panels are
# inserted with split-window -b, before the existing pane, and tmux pane
# indexes are positional, so .1 stops meaning "the claude pane" the moment a
# window gains a panel. Falls back to the positional target for sessions
# created before the marker existed.
# Arguments:
#   session_name
# Outputs:
#   a pane id (e.g. %5) or "=<session>:claude.1"
#######################################
claude_pane_target() {
  local session_name="${1}"
  local pane
  pane="$(tmux list-panes -t "=${session_name}:claude" -F '#{pane_id} #{@vigil_claude}' 2>/dev/null \
    | awk '$2 == "1" { print $1; exit }')" || pane=""
  if [ -n "${pane}" ]; then
    printf '%s' "${pane}"
    return 0
  fi
  printf '%s' "=${session_name}:claude.1"
}
```

`launch_claude_in_pane` uses it:

```bash
launch_claude_in_pane() {
  local session_name="${1}"
  local session_dir="${2}"
  local command="${3}"
  local target
  target="$(claude_pane_target "${session_name}")"

  tmux respawn-pane -k -t "${target}" -c "${session_dir}" \
    "${command}; exec \"\${SHELL}\""
}
```

`setup_secondary_pane` splits from the claude pane and talks to the pane it created:

```bash
setup_secondary_pane() {
  local session="${1}"
  local pane_command="${2}"
  local claude_pane width split new_pane
  claude_pane="$(claude_pane_target "${session}")"
  width="$(tmux display-message -t "=${session}:claude" -p '#{window_width}')"

  if [ "${width}" -ge 200 ]; then
    split='-h'
  else
    split='-v'
  fi

  new_pane="$(tmux split-window -t "${claude_pane}" "${split}" -c '#{pane_current_path}' -P -F '#{pane_id}')"
  tmux send-keys -t "${new_pane}" "${pane_command}" Enter
  tmux select-pane -t "${claude_pane}"
}
```

`create_tmux_session` marks the claude pane right after creating the session, before anything splits the window:

```bash
  tmux new-session -d -s "${session_name}" -n "claude" -c "${session_dir}"
  # Mark the pane so later targeting does not depend on its index, which
  # shifts when a panel is inserted before it.
  tmux set-option -p -t "=${session_name}:claude" @vigil_claude 1
  tmux new-window -t "=${session_name}:2" -n "server" -c "${session_dir}"
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `bats tests/ && make lint`
Expected: PASS and clean.

- [ ] **Step 5: Verify against a real session**

```bash
dispatch sc-<some story>
```
Expected: the worktree and session are created, Claude launches in the claude pane, `nit` runs in the split pane, and focus lands on Claude. Then `prefix p` to add a panel and confirm `tmux list-panes -F '#{pane_index} #{pane_id} #{@vigil_claude}'` still identifies the right pane after the indexes have shifted.

- [ ] **Step 6: Commit**

```bash
git add scripts/scripts/lib/tmux.sh scripts/scripts/tests/tmux_lib.bats
git commit -m "fix(tmux): target session panes by id instead of position"
```

---

## Verification before calling phase 2 done

Not a task; the gate. All of it observed, not reasoned about.

- [ ] `make test && make lint` in `~/vigil`; `bats tests/ && make lint` in `~/dotfiles/scripts/scripts`.
- [ ] `make install`, then with the daemon stopped: `vigil` behaves exactly as before.
- [ ] **The comparison phase 1 never did.** With the daemon up and a TUI attached, watch timing rather than appearance: how fast a new session appears, how fast a bell highlights, whether any PR column blanks, whether any spurious `-> idle` notification fires. Repeat with the daemon stopped. Note both.
- [ ] Two panels plus a full `vigil`, all three live at once. `lsof <socket>` shows three clients on one daemon. Confirm one poller: `gh api rate_limit` before and after a 5 minute window should show roughly 1,920/hour of GraphQL, the same as one client.
- [ ] `kill -9` the daemon with panels open. Every panel should show `no daemon`, keep rendering correct data, and reconnect within a few seconds as a panel respawns it. No panel should be left self-polling once one is back.
- [ ] Start ten panels at once (`for i in $(seq 10); do vigil --panel & done` in separate panes) and confirm exactly one daemon survives: `pgrep -fc 'vigil daemon'` is 1.
- [ ] Resize a panelled window through portrait and landscape and back. No wrapped rows at any width.

package dispatch

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct{ name, input string }{
		{"empty", ""},
		{"whitespace", "   "},
		{"too long", strings.Repeat("x", 501)},
		{"control characters", "sc-1\x00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate(c.input); err == nil {
				t.Errorf("Validate(%q) = nil, want an error", c.input)
			}
		})
	}
	if err := Validate("https://app.shortcut.com/x/story/12345"); err != nil {
		t.Errorf("Validate rejected a good URL: %v", err)
	}
}

// fakeDaemon answers on a socket: it reads one request and then broadcasts
// snapshots containing whatever jobs it was told to report.
type fakeDaemon struct {
	mu       sync.Mutex
	received []*protocol.Request
	reply    func(req *protocol.Request) []protocol.Job
	silent   bool
	listener net.Listener
}

func startFakeDaemon(t *testing.T, path string, silent bool, reply func(*protocol.Request) []protocol.Job) *fakeDaemon {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	d := &fakeDaemon{reply: reply, silent: silent, listener: l}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go d.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return d
}

func (d *fakeDaemon) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	dec := protocol.NewRequestDecoder(conn)
	for {
		req, err := dec.Next()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.received = append(d.received, req)
		silent := d.silent
		d.mu.Unlock()
		if silent {
			continue
		}
		// A real daemon's first post-connect broadcast is the poll tick that
		// predates the submit, so it never carries the job. Sending a
		// job-less snapshot first is what makes awaitAck's loop-until-found
		// behavior load-bearing rather than incidentally passing because the
		// job was in the first (and only) frame checked.
		if err := protocol.Encode(conn, &protocol.Snapshot{Version: protocol.Version}); err != nil {
			return
		}
		for i := 0; i < 5; i++ {
			if err := protocol.Encode(conn, &protocol.Snapshot{
				Version: protocol.Version,
				Jobs:    d.reply(req),
			}); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func socketPath(t *testing.T) string {
	t.Helper()
	// Unix socket paths are length-limited; t.TempDir can be long on macOS.
	dir, err := os.MkdirTemp("", "vd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestSubmitReturnsTheAckedJob(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}}
	})

	job, err := Submit(context.Background(), Options{
		Input: "sc-12345", Cwd: "/Users/x/portal",
		SocketPath: path, AckTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if job.Input != "sc-12345" {
		t.Errorf("got %+v", job)
	}
}

// Submit sends a dispatch request naming the caller's cwd. Both fields are
// load-bearing and neither is exercised by the ID/Version checks elsewhere:
// a wrong Type gets every dispatch refused by a real daemon, and a missing
// Cwd runs every hook in the daemon's own directory instead of the user's
// repo.
func TestSubmitSendsTheRequestTypeAndCwd(t *testing.T) {
	path := socketPath(t)
	d := startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}}
	})

	if _, err := Submit(context.Background(), Options{
		Input: "sc-12345", Cwd: "/Users/x/portal",
		SocketPath: path, AckTimeout: 3 * time.Second,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.received) != 1 {
		t.Fatalf("got %d requests, want 1", len(d.received))
	}
	got := d.received[0]
	if got.Type != protocol.RequestDispatch {
		t.Errorf("got type %q, want %q", got.Type, protocol.RequestDispatch)
	}
	if got.Cwd != "/Users/x/portal" {
		t.Errorf("got cwd %q, want /Users/x/portal", got.Cwd)
	}
}

// A refusal comes back as a refused job, and Submit must report its reason
// rather than the skew message.
func TestSubmitReportsARefusalReason(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{
			ID: req.ID, Input: req.Input,
			State: protocol.JobRefused, Status: "duplicate of an in-flight dispatch",
		}}
	})

	_, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 3 * time.Second,
	})
	if err == nil {
		t.Fatal("got nil, want a refusal error")
	}
	if !strings.Contains(err.Error(), "duplicate of an in-flight dispatch") {
		t.Errorf("got %v, want the refusal reason", err)
	}
	if errors.Is(err, ErrNoAck) {
		t.Error("a refusal was reported as a missing ack")
	}
}

// A failed job was accepted and ran; only a refused job was rejected. Submit
// must not conflate the two, or "vigil dispatch" would exit 1 - and print
// "dispatch refused" - for a job the daemon actually queued and ran.
func TestSubmitTreatsAFailedJobAsAccepted(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{
			ID: req.ID, Input: req.Input,
			State: protocol.JobFailed, Status: "no branch for story 1",
		}}
	})

	job, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Submit: %v, want a failed-but-accepted job to be reported as success", err)
	}
	if job.State != protocol.JobFailed {
		t.Errorf("got state %q, want %q", job.State, protocol.JobFailed)
	}
}

// A daemon that predates phase 4 never reads the frame, so no job ever
// appears. The message has to name the cause, because "make install and
// restart the daemon" is not guessable from a timeout.
func TestSubmitAgainstASilentDaemonSaysItMayBeOld(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, true, nil)

	_, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoAck) {
		t.Fatalf("got %v, want ErrNoAck", err)
	}
	if !strings.Contains(err.Error(), "older vigil") {
		t.Errorf("got %q, want the message to name an older vigil", err.Error())
	}
}

// The other side of the message: a daemon that is broadcasting snapshots is
// demonstrably reading and writing the protocol, so version skew is the one
// diagnosis it cannot be. Reporting skew here sent the user to reinstall over
// a job that was accepted and running - and dispatch-from-chrome raised a
// failure notification for it.
func TestSubmitAgainstALiveDaemonThatNeverPublishesDoesNotBlameTheVersion(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, false, func(*protocol.Request) []protocol.Job {
		return []protocol.Job{{ID: "someone-elses", Input: "sc-9", State: protocol.JobRunning}}
	})

	_, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoAck) {
		t.Fatalf("got %v, want ErrNoAck", err)
	}
	if strings.Contains(err.Error(), "older vigil") {
		t.Errorf("got %q, want no version-skew claim from a daemon that is broadcasting", err.Error())
	}
	if !strings.Contains(err.Error(), "alive") {
		t.Errorf("got %q, want the message to say the daemon is alive", err.Error())
	}
}

// A connection that dies before any snapshot arrives is a crash, not an old
// daemon and not a slow one.
func TestSubmitAgainstADaemonThatHangsUpSaysSo(t *testing.T) {
	path := socketPath(t)
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
		// The request is read before the hang-up. Closing on accept races the
		// submitting write instead, which fails with a broken pipe long
		// before awaitAck is reached, and tests a different path entirely.
		_, _ = protocol.NewRequestDecoder(conn).Next()
		_ = conn.Close()
	}()

	_, err = Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 3 * time.Second,
	})
	if !errors.Is(err, ErrNoAck) {
		t.Fatalf("got %v, want ErrNoAck", err)
	}
	if strings.Contains(err.Error(), "older vigil") {
		t.Errorf("got %q, want a closed connection reported as one", err.Error())
	}
}

func TestSubmitSpawnsADaemonWhenNoneAnswers(t *testing.T) {
	path := socketPath(t)
	spawned := make(chan struct{})
	var once sync.Once

	_, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: path, AckTimeout: 500 * time.Millisecond,
		Spawn: func() error {
			once.Do(func() {
				startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
					return []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}}
				})
				close(spawned)
			})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-spawned:
	default:
		t.Error("Submit did not spawn a daemon")
	}
}

func TestSubmitReportsASpawnFailure(t *testing.T) {
	_, err := Submit(context.Background(), Options{
		Input: "sc-12345", SocketPath: socketPath(t), AckTimeout: 200 * time.Millisecond,
		Spawn: func() error { return errors.New("no executable") },
	})
	if err == nil || !strings.Contains(err.Error(), "no executable") {
		t.Errorf("got %v, want the spawn failure", err)
	}
}

func TestSubmitGeneratesDistinctIDs(t *testing.T) {
	path := socketPath(t)
	d := startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}}
	})
	for _, input := range []string{"sc-1", "sc-2"} {
		if _, err := Submit(context.Background(), Options{
			Input: input, SocketPath: path, AckTimeout: 3 * time.Second,
		}); err != nil {
			t.Fatalf("Submit(%s): %v", input, err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.received) != 2 {
		t.Fatalf("got %d requests, want 2", len(d.received))
	}
	if d.received[0].ID == d.received[1].ID {
		t.Errorf("two submissions shared an id: %s", d.received[0].ID)
	}
	if d.received[0].Version != protocol.Version {
		t.Errorf("got version %d, want %d", d.received[0].Version, protocol.Version)
	}
}

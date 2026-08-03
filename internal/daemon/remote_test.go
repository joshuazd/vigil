package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/transition"
)

// testSocketPath keeps the socket under a short directory: sun_path is 104
// bytes on macOS and t.TempDir() embeds the test's full name, which these
// names overrun.
func testSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "vigild.sock")
}

// mergedPRCommander answers tmux and git for one session on one branch, with
// gh reporting a merged PR. Reaching Done is what makes the effects assertion
// below meaningful: Done is the one transition that runs auto_cleanup.
func mergedPRCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)
	return cmd
}

// TestPollIssuesNoGhCalls holds the daemon's poll path to local work only.
// poll runs inline in Run's select loop, so a network call there stalls new
// connections and dispatch requests as well as publication. The collector's
// own TestSnapshotIssuesNoGhCalls cannot see a fetch reintroduced on this side
// of the seam.
//
// Two polls, not one: the PR store has no working set until the first Snapshot
// posts one, so a fetch reintroduced ahead of Snapshot would spend nothing on
// the very first call and everything after it.
func TestPollIssuesNoGhCalls(t *testing.T) {
	cmd := mergedPRCommander()
	s := &Server{Collector: collect.New(&config.Config{}, cmd)}

	ctx := context.Background()
	s.poll(ctx)
	s.poll(ctx)

	if got := cmd.CallCount("gh"); got != 0 {
		t.Fatalf("got %d gh calls from two polls, want 0: remote fetching belongs on the collector's workers", got)
	}
}

// TestColdStartRunsNoEffects is the regression the whole PRPending mechanism
// exists to prevent. Async PR fetching means the daemon's first poll sees no
// PR; without the skip it seeds alpha at Idle, and the poll after the fetch
// lands reads as Idle -> Done, which runs auto_cleanup against a worktree that
// was already merged before this daemon started.
//
// This drives the passes synchronously rather than starting the workers, so
// the assertion is about ordering and not about how fast a goroutine ran.
func TestColdStartRunsNoEffects(t *testing.T) {
	cmd := mergedPRCommander()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx) // no PR data yet: alpha is pending and must not be seeded
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx) // PR data lands: this is alpha's first real sighting
	s.pendingEffects.Wait()

	if got := effects.count(); got != 0 {
		t.Fatalf("got %d effect runs on a cold start, want 0: an already-merged session must not look like a fresh transition into Done", got)
	}
}

// TestColdStartStillReportsALaterTransition proves the test above is not
// vacuous. If the skip muted alpha permanently, both tests would be green with
// the notify hook broken.
func TestColdStartStillReportsALaterTransition(t *testing.T) {
	state := `{"number": 42, "state": "OPEN"}`
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		return state, nil
	}
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx)
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx) // seeds at Review

	state = `{"number": 42, "state": "MERGED"}`
	s.Collector.Invalidate()
	s.Collector.RefreshRemote(ctx)
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1: the skip must mute the seed, not the session", got)
	}
}

// TestRunStartsTheRemoteWorkers pins the one line that is easy to leave out
// and impossible to notice. Without Collector.Start in Run, a real daemon
// polls forever and never fetches a PR, and every collect-level test still
// passes because they drive RefreshRemote directly.
func TestRunStartsTheRemoteWorkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := mergedPRCommander()
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for cmd.CallCount("gh") == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no gh call after 3s: Run never started the remote workers")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunWaitsForAnInFlightPass: Run returning is what releases the flock and
// unlinks the socket. Doing that with a gh child still running leaves an
// orphan holding a pipe, and the next daemon start races the unlink.
func TestRunWaitsForAnInFlightPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return `{"number": 42, "state": "MERGED"}`, nil
	}
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		cancel()
		<-done
		t.Fatal("the gh stub was never reached")
	}

	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("Run returned while a remote pass was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the pass finished")
	}
}

// TestSlowRemoteDoesNotStallNewConnections is the secondary win. poll runs
// inline in Run's select loop, so a gh call on that path means the daemon
// accepts no connections and handles no dispatch requests for its whole
// duration. With the fetch on a worker, a wedged gh costs a stale PR column
// and nothing else.
func TestSlowRemoteDoesNotStallNewConnections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release := make(chan struct{})
	cmd := mergedPRCommander()
	cmd.HandlerFuncs["gh"] = func(context.Context, string, []string) (string, error) {
		<-release
		return "", nil
	}
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   10 * time.Millisecond,
		SocketPath: testSocketPath(t),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		// Released before the join, not after: Run waits for the remote
		// workers, and one of them is parked in the gh stub.
		close(release)
		<-done
	}()

	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", s.SocketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := protocol.NewDecoder(conn).Next(); err != nil {
		t.Fatalf("no snapshot while a remote pass was blocked: %v", err)
	}
}

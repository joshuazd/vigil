package daemon

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/selfbin"
)

type sizedInfo struct {
	fs.FileInfo
	size int64
}

func (s sizedInfo) Size() int64        { return s.size }
func (s sizedInfo) ModTime() time.Time { return time.Unix(0, 0) }

// binOnDisk is a prober whose answer a test can change from another goroutine
// while Run's goroutine is reading it.
func binOnDisk(size *atomic.Int64) selfbin.Prober {
	return selfbin.Prober{
		Executable: func() (string, error) { return "/bin/vigil", nil },
		Stat: func(string) (fs.FileInfo, error) {
			return sizedInfo{size: size.Load()}, nil
		},
	}
}

// steppingClock advances a full check interval on every read, so every check
// Run makes is past the watcher's rate limit and no test has to sleep through
// it.
func steppingClock() func() time.Time {
	var calls atomic.Int64
	start := time.Now()
	return func() time.Time {
		return start.Add(time.Duration(calls.Add(1)) * (selfbin.CheckInterval + time.Second))
	}
}

func TestARunWhoseBinaryIsReplacedShutsDownAndReturnsErrRestart(t *testing.T) {
	var size atomic.Int64
	size.Store(100)
	s := testServer(t)
	s.bin = selfbin.NewWatcher(binOnDisk(&size), time.Now().Add(-time.Hour))
	s.clock = steppingClock()

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	conn := dialWhenReady(t, s.SocketPath)
	t.Cleanup(func() { _ = conn.Close() })
	size.Store(200)

	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("Run returned %v, want ErrRestart", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never restarted after its binary was replaced")
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("connected client not closed by the restart: %v; Run returned without its shutdown", err)
	}
	if _, err := os.Stat(s.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("socket still present after a restart: %v; the re-exec'd image could not bind", err)
	}
	release, err := s.acquireLock()
	if err != nil {
		t.Fatalf("lock still held after a restart: %v; the re-exec'd image could not start", err)
	}
	release()
}

func TestAnUnchangedBinaryNeverRestarts(t *testing.T) {
	var size atomic.Int64
	size.Store(100)
	s := testServer(t)
	s.bin = selfbin.NewWatcher(binOnDisk(&size), time.Now().Add(-time.Hour))
	s.clock = steppingClock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)
	time.Sleep(10 * s.Interval)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

// replacedServer is a Server whose binary has changed and been seen once, so
// the next restartDue confirms it. Every test of a busy reason starts from
// this and differs from TestAnIdleServerIsDueARestart in one thing.
func replacedServer(t *testing.T, cmd fetch.Commander) (*Server, time.Time) {
	t.Helper()
	var size atomic.Int64
	size.Store(100)
	now := time.Now()
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		bin:       selfbin.NewWatcher(binOnDisk(&size), now.Add(-time.Hour)),
		jobs:      newJobs(testJobsConfig(), newBlockingStream(), fetch.NewMockCommander(), func(string, ...any) {}),
		requests:  make(chan *protocol.Request, queueDepth),
	}
	size.Store(200)
	if s.restartDue(now) {
		t.Fatal("a single observation confirmed a change")
	}
	return s, now.Add(selfbin.CheckInterval + time.Second)
}

func TestAnIdleServerIsDueARestart(t *testing.T) {
	s, now := replacedServer(t, fetch.NewMockCommander())
	if !s.restartDue(now) {
		t.Fatal("an idle daemon with a replaced binary is not due a restart")
	}
}

func TestAQueuedJobDefersARestart(t *testing.T) {
	s, now := replacedServer(t, fetch.NewMockCommander())
	s.jobs.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "x"})
	if s.restartDue(now) {
		t.Fatal("restarted with a queued job; the shutdown would drop accepted work")
	}
}

func TestARunningJobDefersARestart(t *testing.T) {
	stream := newBlockingStream()
	s, now := replacedServer(t, fetch.NewMockCommander())
	s.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.jobs.work(ctx)
	s.jobs.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "x"})
	waitForJobState(t, s.jobs, "a", protocol.JobRunning)
	if s.restartDue(now) {
		t.Fatal("restarted with a running job; the shutdown would kill a dispatch mid-worktree-add")
	}
}

func TestARunningEffectDefersARestart(t *testing.T) {
	s, now := replacedServer(t, fetch.NewMockCommander())
	release := make(chan struct{})
	s.goEffect(func() { <-release })
	if s.restartDue(now) {
		t.Fatal("restarted with a transition effect running; it may be a cleanup mid-worktree-remove")
	}
	close(release)
	s.pendingEffects.Wait()
	if !s.restartDue(now.Add(selfbin.CheckInterval + time.Second)) {
		t.Fatal("still not due a restart once the effect had finished")
	}
}

func TestAnUnhandledRequestDefersARestart(t *testing.T) {
	s, now := replacedServer(t, fetch.NewMockCommander())
	s.requests <- &protocol.Request{ID: "a"}
	if s.restartDue(now) {
		t.Fatal("restarted with a request read and not yet handled; it would be lost")
	}
}

// The warm is the one call that holds the index lock, and cancelling it is a
// SIGKILL that leaves index.lock behind in the user's worktree.
func TestACacheWarmDefersARestart(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F "+fetch.ListPanesFormat, "1700000000|$1|alpha|/repo", nil)
	cmd.OnArgs("tmux list-windows -a -F "+fetch.BellFormat, "", nil)
	cmd.OnArgs("git --no-optional-locks status --porcelain", "", nil)
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
		"git status --porcelain": func(context.Context, string, []string) (string, error) {
			close(entered)
			<-release
			return "", nil
		},
	}
	s, now := replacedServer(t, cmd)
	ctx := context.Background()
	if _, err := s.Collector.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	passed := make(chan struct{})
	go func() {
		s.Collector.RefreshDirty(ctx)
		close(passed)
	}()
	<-entered

	if s.restartDue(now) {
		t.Fatal("restarted with a cache warm holding the index lock")
	}
	close(release)
	<-passed
	if !s.restartDue(now.Add(selfbin.CheckInterval + time.Second)) {
		t.Fatal("still not due a restart once the warm had finished")
	}
}

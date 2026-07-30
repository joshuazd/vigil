package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

// blockingStream is a StreamCommander whose run does not return until released.
// Used to observe a job mid-flight.
type blockingStream struct {
	mu sync.Mutex
	// ignoreContext makes a run outlive cancellation. Only the
	// goroutine-unwind test sets it; a real job dies with the daemon.
	ignoreContext bool
	started       chan struct{}
	release       chan struct{}
	lines         []string
	err           error
	runs          int
	maxAtOnce     int
	inFlight      int
}

func newBlockingStream(lines ...string) *blockingStream {
	return &blockingStream{
		started: make(chan struct{}, 16),
		release: make(chan struct{}),
		lines:   lines,
	}
}

func (b *blockingStream) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	b.mu.Lock()
	b.runs++
	b.inFlight++
	if b.inFlight > b.maxAtOnce {
		b.maxAtOnce = b.inFlight
	}
	lines := append([]string(nil), b.lines...)
	err := b.err
	ignoreContext := b.ignoreContext
	b.mu.Unlock()

	b.started <- struct{}{}
	for _, line := range lines {
		onLine(line)
	}
	if ignoreContext {
		<-b.release
	} else {
		select {
		case <-b.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
	return err
}

func (b *blockingStream) counts() (runs, maxAtOnce int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runs, b.maxAtOnce
}

func testJobsConfig() *config.Config {
	return &config.Config{
		Hooks:    map[string]any{"dispatch": "dispatch {input}"},
		Settings: map[string]any{"dispatch_timeout": "300"},
	}
}

func findJob(list []protocol.Job, id string) *protocol.Job {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func waitForJobState(t *testing.T, j *jobs, id, want string) protocol.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := findJob(j.snapshot(), id); got != nil && got.State == want {
			return *got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s; snapshot: %+v", id, want, j.snapshot())
	return protocol.Job{}
}

func TestASubmittedJobIsQueuedThenRunsThenSucceeds(t *testing.T) {
	stream := newBlockingStream(">>> fetching story")
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})

	running := waitForJobState(t, j, "a", protocol.JobRunning)
	if running.Status != "fetching story" {
		t.Errorf("got status %q, want the streamed line stripped of its prefix", running.Status)
	}
	close(stream.release)
	done := waitForJobState(t, j, "a", protocol.JobSucceeded)
	if done.Ended == 0 {
		t.Error("Ended was not stamped")
	}
}

func TestJobsRunOneAtATime(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "b", Input: "sc-2"})

	waitForJobState(t, j, "a", protocol.JobRunning)
	if got := findJob(j.snapshot(), "b"); got == nil || got.State != protocol.JobQueued {
		t.Fatalf("second job should be queued, got %+v", got)
	}
	close(stream.release)
	waitForJobState(t, j, "b", protocol.JobSucceeded)
	if _, maxAtOnce := stream.counts(); maxAtOnce != 1 {
		t.Errorf("ran %d jobs at once, want 1", maxAtOnce)
	}
}

func TestADuplicateInputIsRefusedAsAFailedJob(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	waitForJobState(t, j, "a", protocol.JobRunning)
	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "b", Input: "sc-1"})

	dup := waitForJobState(t, j, "b", protocol.JobFailed)
	if !strings.Contains(dup.Status, "duplicate") {
		t.Errorf("got %q, want a duplicate reason", dup.Status)
	}
	close(stream.release)
	if runs, _ := stream.counts(); runs != 1 {
		t.Errorf("ran %d times, want 1: the duplicate must not execute", runs)
	}
}

func TestAnUnknownRequestVersionIsRefusedAsAFailedJob(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	j.submit(&protocol.Request{Version: 99, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})

	got := findJob(j.snapshot(), "a")
	if got == nil || got.State != protocol.JobFailed {
		t.Fatalf("got %+v, want a failed job", got)
	}
	if !strings.Contains(got.Status, "99") {
		t.Errorf("got %q, want the version named", got.Status)
	}
	if runs, _ := stream.counts(); runs != 0 {
		t.Errorf("ran %d times, want 0", runs)
	}
}

func TestAnUnknownRequestTypeIsRefusedAsAFailedJob(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	j.submit(&protocol.Request{Version: protocol.Version, Type: "explode", ID: "a", Input: "sc-1"})

	got := findJob(j.snapshot(), "a")
	if got == nil || got.State != protocol.JobFailed {
		t.Fatalf("got %+v, want a failed job", got)
	}
}

func TestAFailingJobKeepsItsLastOutputLineAsTheReason(t *testing.T) {
	stream := newBlockingStream(">>> fetching", "!!! no branch for story 1")
	stream.err = context.DeadlineExceeded
	close(stream.release)
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	got := waitForJobState(t, j, "a", protocol.JobFailed)
	if !strings.Contains(got.Status, "no branch for story 1") {
		t.Errorf("got %q, want the last output line", got.Status)
	}
}

// The prefixes lib/output.sh writes are stripped for display, tolerantly: an
// unrecognized line still shows.
func TestStatusLinesAreStrippedOfTheirPrefixAndAnsi(t *testing.T) {
	cases := []struct{ in, want string }{
		{">>> fetching story", "fetching story"},
		{"!!! broke", "broke"},
		{"\x1b[0;32m>>> coloured\x1b[0m", "coloured"},
		{"plain line", "plain line"},
		{"", ""},
	}
	for _, c := range cases {
		if got := statusLine(c.in); got != c.want {
			t.Errorf("statusLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestASucceededJobIsPrunedAndAFailedOneIsRetainedLonger(t *testing.T) {
	stream := newBlockingStream()
	close(stream.release)
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	now := time.Now()
	j.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "ok", Input: "sc-1"})
	waitForJobState(t, j, "ok", protocol.JobSucceeded)
	j.submit(&protocol.Request{Version: 99, ID: "bad", Input: "sc-2"})

	now = now.Add(succeededRetention + time.Second)
	list := j.snapshot()
	if findJob(list, "ok") != nil {
		t.Error("a succeeded job outlived its retention")
	}
	if findJob(list, "bad") == nil {
		t.Error("a failed job was pruned on the succeeded schedule")
	}

	now = now.Add(failedRetention)
	if findJob(j.snapshot(), "bad") != nil {
		t.Error("a failed job outlived its retention")
	}
}

func TestTheJobHookReceivesTheClientAndTheCwd(t *testing.T) {
	stream := &recordingStream{released: make(chan struct{})}
	close(stream.released)
	tmux := fetch.NewMockCommander()
	tmux.On("tmux", "900|/dev/ttys009\n", nil)
	j := newJobs(testJobsConfig(), stream, tmux, func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch,
		ID: "a", Input: "sc-1", Cwd: "/Users/x/portal",
	})
	waitForJobState(t, j, "a", protocol.JobSucceeded)

	if stream.dir != "/Users/x/portal" {
		t.Errorf("got dir %q, want the job's cwd", stream.dir)
	}
	if !containsEnv(stream.env, "VIGIL_CLIENT=/dev/ttys009") {
		t.Errorf("got env %q, want VIGIL_CLIENT", stream.env)
	}
}

func TestNoAttachedClientMeansAnEmptyVigilClient(t *testing.T) {
	stream := &recordingStream{released: make(chan struct{})}
	close(stream.released)
	tmux := fetch.NewMockCommander()
	tmux.On("tmux", "", nil)
	j := newJobs(testJobsConfig(), stream, tmux, func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	waitForJobState(t, j, "a", protocol.JobSucceeded)

	if !containsEnv(stream.env, "VIGIL_CLIENT=") {
		t.Errorf("got env %q, want an empty VIGIL_CLIENT", stream.env)
	}
}

type recordingStream struct {
	mu       sync.Mutex
	dir      string
	env      []string
	released chan struct{}
}

func (r *recordingStream) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	r.mu.Lock()
	r.dir = dir
	r.env = append([]string(nil), env...)
	r.mu.Unlock()
	<-r.released
	return nil
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

package daemon

import (
	"context"
	"errors"
	"fmt"
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

// run flips State to Running and streams the first status line in two
// separate critical sections (the second reached only after
// fetch.MostRecentClient and RunHookStream's setup), so a snapshot taken the
// instant State changes can still see an empty Status. Polling on State alone
// caught that gap under -race with enough concurrent load elsewhere in the
// package to widen the window; this waits for both.
func waitForJobStatus(t *testing.T, j *jobs, id, state, status string) protocol.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := findJob(j.snapshot(), id); got != nil && got.State == state && got.Status == status {
			return *got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s with status %q; snapshot: %+v", id, state, status, j.snapshot())
	return protocol.Job{}
}

func TestASubmittedJobIsQueuedThenRunsThenSucceeds(t *testing.T) {
	stream := newBlockingStream(">>> fetching story")
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})

	waitForJobStatus(t, j, "a", protocol.JobRunning, "fetching story")
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

func TestADuplicateInputIsRefused(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	waitForJobState(t, j, "a", protocol.JobRunning)
	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "b", Input: "sc-1"})

	dup := waitForJobState(t, j, "b", protocol.JobRefused)
	if !strings.Contains(dup.Status, "duplicate") {
		t.Errorf("got %q, want a duplicate reason", dup.Status)
	}
	close(stream.release)
	// Wait for "a" to actually finish before reading counts: State flips to
	// Running before RunStream is called, so checking counts right after the
	// close (rather than after a's terminal state) can read the run as not
	// yet started.
	waitForJobState(t, j, "a", protocol.JobSucceeded)
	if runs, _ := stream.counts(); runs != 1 {
		t.Errorf("ran %d times, want 1: the duplicate must not execute", runs)
	}
}

// The named hazard: submit's duplicate scan and its insert must be one
// critical section, not two. Every other test in this file calls submit from
// a single goroutine, so this is the only one that can catch a refactor that
// splits them - work is deliberately never started, so a win is "queued",
// not "running".
func TestConcurrentSubmitsOfTheSameInputYieldExactlyOneWinner(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			j.submit(&protocol.Request{
				Version: protocol.Version, Type: protocol.RequestDispatch,
				ID: fmt.Sprintf("job-%d", i), Input: "sc-1",
			})
		}()
	}
	wg.Wait()

	list := j.snapshot()
	if len(list) != n {
		t.Fatalf("got %d jobs, want %d", len(list), n)
	}
	runnable := 0
	duplicates := 0
	for _, job := range list {
		switch job.State {
		case protocol.JobQueued, protocol.JobRunning:
			runnable++
		case protocol.JobRefused:
			if strings.Contains(job.Status, "duplicate") {
				duplicates++
			}
		}
	}
	if runnable != 1 {
		t.Errorf("got %d runnable jobs, want exactly 1: %+v", runnable, list)
	}
	if duplicates != n-1 {
		t.Errorf("got %d jobs refused as duplicates, want %d: %+v", duplicates, n-1, list)
	}
}

func TestAnUnknownRequestVersionIsRefused(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	j.submit(&protocol.Request{Version: 99, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})

	got := findJob(j.snapshot(), "a")
	if got == nil || got.State != protocol.JobRefused {
		t.Fatalf("got %+v, want a refused job", got)
	}
	if !strings.Contains(got.Status, "99") {
		t.Errorf("got %q, want the version named", got.Status)
	}
	if runs, _ := stream.counts(); runs != 0 {
		t.Errorf("ran %d times, want 0", runs)
	}
}

func TestAnUnknownRequestTypeIsRefused(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	j.submit(&protocol.Request{Version: protocol.Version, Type: "explode", ID: "a", Input: "sc-1"})

	got := findJob(j.snapshot(), "a")
	if got == nil || got.State != protocol.JobRefused {
		t.Fatalf("got %+v, want a refused job", got)
	}
}

func TestEmptyInputIsRefused(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: ""})

	got := findJob(j.snapshot(), "a")
	if got == nil || got.State != protocol.JobRefused {
		t.Fatalf("got %+v, want a refused job", got)
	}
	if !strings.Contains(got.Status, "empty") {
		t.Errorf("got %q, want an empty-input reason", got.Status)
	}
	if runs, _ := stream.counts(); runs != 0 {
		t.Errorf("ran %d times, want 0", runs)
	}
}

// A full queue is the one refusal registered in two steps: submit inserts the
// job as queued, then flips it to refused after releasing the lock because
// the non-blocking send found no room. work is deliberately never started, so
// pending fills up and stays full.
func TestAFullQueueIsRefused(t *testing.T) {
	stream := newBlockingStream()
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})

	for i := 0; i < queueDepth; i++ {
		j.submit(&protocol.Request{
			Version: protocol.Version, Type: protocol.RequestDispatch,
			ID: fmt.Sprintf("filler-%d", i), Input: fmt.Sprintf("sc-%d", i),
		})
	}

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "overflow", Input: "sc-overflow"})

	got := findJob(j.snapshot(), "overflow")
	if got == nil || got.State != protocol.JobRefused {
		t.Fatalf("got %+v, want a refused job", got)
	}
	if !strings.Contains(got.Status, "queue is full") {
		t.Errorf("got %q, want a queue-full reason", got.Status)
	}
}

// A plain exit error, not a context deadline: that distinction is what
// TestATimedOutJobReportsTheTimeoutNotTheLastLine pins on the other side.
func TestAFailingJobKeepsItsLastOutputLineAsTheReason(t *testing.T) {
	stream := newBlockingStream(">>> fetching", "!!! no branch for story 1")
	stream.err = errors.New("exit status 1")
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

// A context deadline is not a plain exit error: dispatch_timeout killed the
// job, so the reason is the timeout, not whatever the job last printed
// before it was cut off. Without this, a hang and a timeout look identical
// in the status bar.
func TestATimedOutJobReportsTheTimeoutNotTheLastLine(t *testing.T) {
	stream := newBlockingStream(">>> cloning repository")
	stream.err = context.DeadlineExceeded
	close(stream.release)
	j := newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.work(ctx)

	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1"})
	got := waitForJobState(t, j, "a", protocol.JobFailed)
	if got.Status != "timed out after 300s" {
		t.Errorf("got %q, want the timeout reason", got.Status)
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

// TestASucceededJobIsPrunedAndARefusedOneIsRetainedLonger also pins that
// refused shares failed's retention, not succeeded's: "bad" here is refused
// (a bad version), not failed, and the two get the same schedule.
func TestASucceededJobIsPrunedAndARefusedOneIsRetainedLonger(t *testing.T) {
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
		t.Error("a refused job was pruned on the succeeded schedule")
	}

	now = now.Add(failedRetention)
	if findJob(j.snapshot(), "bad") != nil {
		t.Error("a refused job outlived its retention")
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

func TestDismissTerminalRemovesOnlyFailedAndRefusedJobs(t *testing.T) {
	j := newJobs(&config.Config{}, nil, nil, func(string, ...any) {})
	j.byID = map[string]*protocol.Job{
		"f": {ID: "f", State: protocol.JobFailed},
		"r": {ID: "r", State: protocol.JobRefused},
		"q": {ID: "q", State: protocol.JobQueued},
		"n": {ID: "n", State: protocol.JobRunning},
		"s": {ID: "s", State: protocol.JobSucceeded},
	}
	j.order = []string{"f", "r", "q", "n", "s"}
	j.cwds = map[string]string{"f": "/tmp/f", "q": "/tmp/q"}

	if !j.dismissTerminal() {
		t.Fatal("dismissTerminal reported no change with two terminal jobs present")
	}

	got := map[string]bool{}
	for _, job := range j.snapshot() {
		got[job.ID] = true
	}
	for _, id := range []string{"q", "n", "s"} {
		if !got[id] {
			t.Errorf("job %q was removed; only failed and refused may be", id)
		}
	}
	for _, id := range []string{"f", "r"} {
		if got[id] {
			t.Errorf("job %q survived dismissal", id)
		}
	}
	if _, ok := j.cwds["f"]; ok {
		t.Error("the dismissed job's cwd was left behind")
	}
	if _, ok := j.cwds["q"]; !ok {
		t.Error("a surviving job lost its cwd")
	}
}

func TestDismissTerminalReportsNoChangeWhenThereIsNothingToDismiss(t *testing.T) {
	j := newJobs(&config.Config{}, nil, nil, func(string, ...any) {})
	j.byID = map[string]*protocol.Job{"n": {ID: "n", State: protocol.JobRunning}}
	j.order = []string{"n"}
	if j.dismissTerminal() {
		t.Fatal("dismissTerminal reported a change with only a running job present")
	}
}

// An old daemon receiving a dismiss frame takes submit's empty-ID path and
// registers nothing. This is the whole reason the frame carries no ID, and it
// is pinned here rather than in a comment.
func TestSubmitDropsAnEmptyIDRequestWithoutRegisteringAnything(t *testing.T) {
	j := newJobs(&config.Config{}, nil, nil, func(string, ...any) {})
	j.submit(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDismiss})
	if got := j.snapshot(); len(got) != 0 {
		t.Fatalf("snapshot has %d jobs, want 0: %+v", len(got), got)
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

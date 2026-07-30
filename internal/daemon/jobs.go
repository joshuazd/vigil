package daemon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

// Retention after a job ends. A success is a line the user glances at; a
// failure has to survive long enough to be read by someone who was looking
// somewhere else when it happened.
const (
	succeededRetention = 10 * time.Second
	failedRetention    = 10 * time.Minute
)

// queueDepth bounds pending submissions. Serialized execution means a deep
// queue is a queue nobody asked for; a click that arrives past this is refused
// visibly rather than buffered invisibly.
const queueDepth = 16

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// statusLine renders one line of job output for display. Tolerant on purpose:
// lib/output.sh writes ">>> " and "!!! " prefixes, but that is a soft read of
// its format rather than a contract, so an unrecognized line still shows.
func statusLine(raw string) string {
	s := ansiPattern.ReplaceAllString(raw, "")
	s = strings.TrimSpace(s)
	for _, prefix := range []string{">>> ", "!!! "} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimPrefix(s, prefix)
		}
	}
	return s
}

// jobs is the daemon's dispatch queue. One job runs at a time: two concurrent
// `git worktree add` calls in one repository contend on the index lock, and
// dispatches arrive one click at a time.
//
// mu guards byID, order and cwds. A running job's goroutine writes Status
// through setStatus, and poll reads the whole table through snapshot, so
// nothing hands out a *Job that a goroutine is still writing.
type jobs struct {
	mu    sync.Mutex
	byID  map[string]*protocol.Job
	order []string

	// cwds holds each job's working directory, keyed by ID. Not on
	// protocol.Job: the cwd is a property of the request, not of the
	// published job, and has no business being broadcast to every client.
	cwds map[string]string

	pending chan string

	cfg    *config.Config
	stream fetch.StreamCommander
	cmd    fetch.Commander
	logf   func(string, ...any)

	// now is a seam for the retention tests.
	now func() time.Time
}

func newJobs(cfg *config.Config, stream fetch.StreamCommander, cmd fetch.Commander, logf func(string, ...any)) *jobs {
	return &jobs{
		byID:    make(map[string]*protocol.Job),
		cwds:    make(map[string]string),
		pending: make(chan string, queueDepth),
		cfg:     cfg,
		stream:  stream,
		cmd:     cmd,
		logf:    logf,
		now:     time.Now,
	}
}

// submit registers a request. A refusal is registered too, as a refused job
// naming the reason: the submitting client waits for its id to appear in a
// snapshot, so a silent drop would be indistinguishable from a daemon that
// never read the frame. Refused is distinct from failed: a refused job never
// ran.
func (j *jobs) submit(req *protocol.Request) {
	if req == nil || req.ID == "" {
		return
	}

	reason := ""
	switch {
	case req.Version != protocol.Version:
		reason = fmt.Sprintf("unsupported request version %d, this daemon speaks %d", req.Version, protocol.Version)
	case req.Type != protocol.RequestDispatch:
		reason = fmt.Sprintf("unsupported request type %q", req.Type)
	case req.Input == "":
		reason = "empty dispatch input"
	}

	j.mu.Lock()
	if _, exists := j.byID[req.ID]; exists {
		j.mu.Unlock()
		return
	}
	if reason == "" && j.inFlightInputLocked(req.Input) {
		reason = "duplicate of an in-flight dispatch"
	}

	now := j.now().Unix()
	job := &protocol.Job{ID: req.ID, Input: req.Input, State: protocol.JobQueued, Started: now}
	if reason != "" {
		job.State = protocol.JobRefused
		job.Status = reason
		job.Ended = now
	}
	j.byID[req.ID] = job
	j.order = append(j.order, req.ID)
	j.cwds[req.ID] = req.Cwd
	j.mu.Unlock()

	if reason != "" {
		j.logf("dispatch %s refused: %s", req.ID, reason)
		return
	}

	select {
	case j.pending <- req.ID:
	default:
		j.refuse(req.ID, "dispatch queue is full")
	}
}

// refuse is fail's counterpart for the one refusal registered in two steps: a
// full queue is discovered only after the job is already inserted as queued,
// so it is flipped to refused here rather than in submit's reason switch.
func (j *jobs) refuse(id, reason string) {
	j.finish(id, protocol.JobRefused, reason)
	j.logf("dispatch %s refused: %s", id, reason)
}

func (j *jobs) inFlightInputLocked(input string) bool {
	for _, existing := range j.byID {
		if existing.Input != input {
			continue
		}
		if existing.State == protocol.JobQueued || existing.State == protocol.JobRunning {
			return true
		}
	}
	return false
}

// work runs queued jobs one at a time until ctx ends.
func (j *jobs) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-j.pending:
			j.run(ctx, id)
		}
	}
}

func (j *jobs) run(ctx context.Context, id string) {
	j.mu.Lock()
	job, ok := j.byID[id]
	if !ok || job.State != protocol.JobQueued {
		j.mu.Unlock()
		return
	}
	job.State = protocol.JobRunning
	job.Started = j.now().Unix()
	input, cwd := job.Input, j.cwds[id]
	j.mu.Unlock()

	// Resolved per job rather than per submission: a dispatch takes long
	// enough that the client the user is sitting at can change in between.
	client := fetch.MostRecentClient(ctx, j.cmd)

	err := j.cfg.RunHookStream(ctx, j.stream, "dispatch",
		map[string]string{"input": input},
		cwd,
		[]string{"VIGIL_CLIENT=" + client},
		j.cfg.GetSettingDuration("dispatch_timeout"),
		func(line string) { j.setStatus(id, statusLine(line)) },
	)
	if err != nil {
		j.failFromOutput(id, err)
		return
	}
	j.finish(id, protocol.JobSucceeded, "")
}

func (j *jobs) setStatus(id, status string) {
	if status == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if job, ok := j.byID[id]; ok {
		job.Status = status
	}
}

// failFromOutput prefers the job's last output line as the reason: a script
// that explained itself has already said something better than "exit 1". A
// context deadline is the one case where that preference is wrong: the job
// did not explain its own failure, dispatch_timeout killed it mid-line, so
// the last line the user sees is stale progress rather than a reason - the
// timeout itself is reported instead.
func (j *jobs) failFromOutput(id string, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		timeout := j.cfg.GetSettingDuration("dispatch_timeout")
		j.fail(id, fmt.Sprintf("timed out after %ds", int(timeout.Seconds())))
		return
	}

	j.mu.Lock()
	last := ""
	if job, ok := j.byID[id]; ok {
		last = job.Status
	}
	j.mu.Unlock()
	if last == "" {
		last = err.Error()
	}
	j.fail(id, last)
}

func (j *jobs) fail(id, reason string) {
	j.finish(id, protocol.JobFailed, reason)
	j.logf("dispatch %s failed: %s", id, reason)
}

func (j *jobs) finish(id, state, status string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.byID[id]
	if !ok {
		return
	}
	job.State = state
	if status != "" {
		job.Status = status
	}
	job.Ended = j.now().Unix()
}

// snapshot returns a copy of the job table, pruning what has outlived its
// retention. A copy, not the live values: a running job's goroutine writes
// Status, and poll marshals what this returns.
func (j *jobs) snapshot() []protocol.Job {
	j.mu.Lock()
	defer j.mu.Unlock()

	now := j.now()
	kept := make([]string, 0, len(j.order))
	out := make([]protocol.Job, 0, len(j.order))
	for _, id := range j.order {
		job, ok := j.byID[id]
		if !ok {
			continue
		}
		if expired(job, now) {
			delete(j.byID, id)
			delete(j.cwds, id)
			continue
		}
		kept = append(kept, id)
		out = append(out, *job)
	}
	j.order = kept
	if len(out) == 0 {
		return nil
	}
	return out
}

func expired(job *protocol.Job, now time.Time) bool {
	if job.Ended == 0 {
		return false
	}
	ended := time.Unix(job.Ended, 0)
	switch job.State {
	case protocol.JobSucceeded:
		return now.Sub(ended) > succeededRetention
	case protocol.JobFailed, protocol.JobRefused:
		return now.Sub(ended) > failedRetention
	}
	return false
}

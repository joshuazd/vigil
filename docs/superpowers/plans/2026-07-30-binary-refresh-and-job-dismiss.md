# Binary refresh and job dismissal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the user a key that clears a finished dispatch line, make a vigil client restart itself when its binary changes, and make an out-of-date daemon say so instead of silently withholding data.

**Architecture:** A new leaf package `internal/selfbin` stats the running executable's path and returns a comparable `Stamp`. Clients hold one and re-exec when it changes; the daemon publishes its own startup stamp in the snapshot so a client can render `daemon outdated`. Separately, a new `dismiss` request type over the existing bidirectional socket lets any client clear the daemon's terminal jobs for everybody at once.

**Tech Stack:** Go, Bubble Tea, lipgloss, newline-delimited JSON over a unix socket. Tests are `go test -race ./...`.

**Design:** `docs/superpowers/specs/2026-07-30-binary-refresh-and-job-dismiss-design.md`. Read it before starting. Where this plan and the design disagree, raise it rather than guessing - eleven briefs in the phase 4 plan contained defects written by the plan's author.

## Global Constraints

- `protocol.Version` stays **1**. Both new protocol elements are additive. Do not bump it.
- **No global mutable state.** Seams are struct fields with nil-means-default, following `daemon.Server.Detector` / `Effects`. The one exception is `main`, which already uses `model.SetDaemonSpawner` for exactly this reason.
- **Prefer no code comments.** Comment only where the code's meaning cannot be inferred from reading it - which, in this codebase, means the *why*, never the *what*.
- **Never use the em dash.** Use a plain dash.
- `make test` is `go test -race ./...`. `-race` is not optional.
- **A test is not evidence until it has been seen to fail.** Every task below has an explicit "run it and watch it fail" step. Do not skip it, and do not accept a test that passes before its implementation exists.
- Do not touch `ExecCommander.Run`'s missing `WaitDelay`. It is a known landmine and out of scope.
- Do not add cancel-a-running-job, do not dismiss succeeded jobs, and do not restart the daemon. All three are explicitly rejected in the design.

## File Structure

| File | Responsibility |
|---|---|
| `internal/selfbin/selfbin.go` (create) | `Stamp` and `Prober`. Stats the running executable's path. Knows nothing about vigil. |
| `internal/selfbin/selfbin_test.go` (create) | Tests for the above. |
| `internal/protocol/protocol.go` (modify) | `RequestDismiss` constant, `Snapshot.DaemonBin` field. |
| `internal/daemon/jobs.go` (modify) | `dismissTerminal`. |
| `internal/daemon/daemon.go` (modify) | Route requests by type; carry and publish `BinStamp`. |
| `internal/model/model.go` (modify) | Binary tracking, restart flag, esc cascade, `daemonHealth`. |
| `internal/model/messages.go` (modify) | `Snapshot`'s new field on `SnapshotMsg`. |
| `internal/model/client.go` (modify) | Carry `DaemonBin` from the decoded snapshot into `SnapshotMsg`. |
| `main.go` (modify) | The exec seam and the post-`p.Run()` re-exec. |

---

### Task 1: `internal/selfbin` - stat the running executable

**Files:**
- Create: `internal/selfbin/selfbin.go`
- Test: `internal/selfbin/selfbin_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Stamp struct { Size int64 \`json:"size"\`; ModNano int64 \`json:"mod_nano"\` }`
  - `func (s Stamp) Zero() bool`
  - `type Prober struct { Executable func() (string, error); Stat func(string) (fs.FileInfo, error) }` - note `io/fs`, not `os`
  - `func (p Prober) Current() (Stamp, bool)` - `false` means "could not tell", and every caller must treat that as *unchanged*.

- [ ] **Step 1: Write the failing tests**

Create `internal/selfbin/selfbin_test.go`:

```go
package selfbin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeInfo struct {
	fs.FileInfo
	size int64
	mod  time.Time
}

func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) ModTime() time.Time { return f.mod }

func proberFor(path string, info fs.FileInfo, statErr error) Prober {
	return Prober{
		Executable: func() (string, error) { return path, nil },
		Stat:       func(string) (fs.FileInfo, error) { return info, statErr },
	}
}

func TestCurrentReportsSizeAndModTime(t *testing.T) {
	mod := time.Unix(1700000000, 1234)
	got, ok := proberFor("/bin/vigil", fakeInfo{size: 42, mod: mod}, nil).Current()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.Size != 42 || got.ModNano != mod.UnixNano() {
		t.Fatalf("got %+v, want size 42 and mod %d", got, mod.UnixNano())
	}
}

func TestCurrentFailsClosedWhenStatFails(t *testing.T) {
	if _, ok := proberFor("/bin/vigil", nil, errors.New("boom")).Current(); ok {
		t.Fatal("ok = true after a stat failure, want false: the caller reads false as unchanged")
	}
}

func TestCurrentFailsClosedWhenTheExecutableCannotBeResolved(t *testing.T) {
	p := Prober{
		Executable: func() (string, error) { return "", errors.New("boom") },
		Stat:       func(string) (fs.FileInfo, error) { t.Fatal("stat called after Executable failed"); return nil, nil },
	}
	if _, ok := p.Current(); ok {
		t.Fatal("ok = true, want false")
	}
}

func TestZeroDistinguishesAnUnsetStamp(t *testing.T) {
	if !(Stamp{}).Zero() {
		t.Fatal("the zero Stamp is not Zero()")
	}
	if (Stamp{Size: 1}).Zero() {
		t.Fatal("a populated Stamp reports Zero()")
	}
}

// A Prober with no funcs set must work against the real running binary, since
// that is how every non-test caller builds one.
func TestAZeroProberStatsTheRealExecutable(t *testing.T) {
	got, ok := Prober{}.Current()
	if !ok {
		t.Fatal("ok = false for the real test binary")
	}
	if got.Zero() {
		t.Fatal("the real test binary stamped as zero")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	info, err := os.Stat(filepath.Clean(exe))
	if err != nil {
		t.Skip("cannot stat the test binary")
	}
	if got.Size != info.Size() {
		t.Fatalf("size %d, want %d", got.Size, info.Size())
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/selfbin/ -v`
Expected: build failure - `undefined: Prober`, `undefined: Stamp`.

- [ ] **Step 3: Write the implementation**

Create `internal/selfbin/selfbin.go`:

```go
// Package selfbin identifies the image a vigil process is running, so a
// long-lived one can notice it has been replaced on disk.
package selfbin

import (
	"io/fs"
	"os"
)

// Stamp identifies a binary by size and modification time rather than by the
// main.version ldflag: that string comes from `git describe --dirty`, which is
// identical across two consecutive dirty builds - the change that matters most
// during development.
type Stamp struct {
	Size    int64 `json:"size"`
	ModNano int64 `json:"mod_nano"`
}

func (s Stamp) Zero() bool { return s == Stamp{} }

// Prober resolves and stats the running executable. The nil funcs are the real
// ones; a test supplies its own.
type Prober struct {
	Executable func() (string, error)
	Stat       func(string) (fs.FileInfo, error)
}

// Current stamps the path this process was launched from. `make install`
// renames a new file over that path rather than writing in place, so the
// running process keeps its old inode while the path resolves to the new file.
//
// A false second return means "could not tell", and every caller reads it as
// unchanged. Failing closed is the point: a process that cannot stat itself
// must never conclude it is out of date.
func (p Prober) Current() (Stamp, bool) {
	executable := p.Executable
	if executable == nil {
		executable = os.Executable
	}
	stat := p.Stat
	if stat == nil {
		stat = func(name string) (fs.FileInfo, error) { return os.Stat(name) }
	}

	path, err := executable()
	if err != nil || path == "" {
		return Stamp{}, false
	}
	info, err := stat(path)
	if err != nil || info == nil {
		return Stamp{}, false
	}
	return Stamp{Size: info.Size(), ModNano: info.ModTime().UnixNano()}, true
}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/selfbin/ -v`
Expected: PASS, all five tests.

- [ ] **Step 5: Verify the tests can fail (mutation check)**

Change `return Stamp{}, false` in the stat-error branch to `return Stamp{}, true` and re-run. `TestCurrentFailsClosedWhenStatFails` must fail. Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/selfbin/
git commit -m "feat(selfbin): stamp the running executable's path"
```

---

### Task 2: Protocol - the dismiss type and the daemon's stamp

**Files:**
- Modify: `internal/protocol/protocol.go`
- Test: `internal/protocol/protocol_test.go`

**Interfaces:**
- Consumes: `selfbin.Stamp` from Task 1.
- Produces:
  - `const RequestDismiss = "dismiss"`
  - `Snapshot.DaemonBin selfbin.Stamp` with tag `json:"daemon_bin,omitempty"`

- [ ] **Step 1: Write the failing tests**

Append to `internal/protocol/protocol_test.go`:

```go
func TestSnapshotCarriesTheDaemonBinaryStamp(t *testing.T) {
	var buf bytes.Buffer
	want := selfbin.Stamp{Size: 991, ModNano: 12345}
	if err := Encode(&buf, &Snapshot{Version: Version, DaemonBin: want}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.DaemonBin != want {
		t.Fatalf("DaemonBin = %+v, want %+v", got.DaemonBin, want)
	}
}

// The additive claim, tested rather than asserted in a comment: a snapshot
// written by a daemon that predates the field still decodes at version 1, and
// the field reads as the zero Stamp.
func TestASnapshotWithoutTheStampStillDecodesAtVersionOne(t *testing.T) {
	line := []byte(`{"version":1,"timestamp":7,"sessions":[]}` + "\n")
	got, err := NewDecoder(bytes.NewReader(line)).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !got.DaemonBin.Zero() {
		t.Fatalf("DaemonBin = %+v, want the zero Stamp", got.DaemonBin)
	}
}

func TestTheDismissRequestTypeRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeRequest(&buf, &Request{Version: Version, Type: RequestDismiss}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := NewRequestDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Type != RequestDismiss {
		t.Fatalf("Type = %q, want %q", got.Type, RequestDismiss)
	}
	if got.ID != "" {
		t.Fatalf("ID = %q, want empty: an old daemon must drop this frame silently rather than register a refused job for an unknown type", got.ID)
	}
}
```

Add `"github.com/jzinkduda/vigil/internal/selfbin"` to the test file's imports (and `"bytes"` if it is not already there).

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/protocol/ -run 'DaemonBinaryStamp|WithoutTheStamp|DismissRequestType' -v`
Expected: build failure - `unknown field DaemonBin`, `undefined: RequestDismiss`.

- [ ] **Step 3: Write the implementation**

In `internal/protocol/protocol.go`, beside `RequestDispatch`:

```go
const RequestDispatch = "dispatch"

// RequestDismiss clears the daemon's failed and refused jobs. It carries an
// empty ID on purpose: jobs.submit drops an empty-ID request before its reason
// switch, so a new client pressing dismiss at an old daemon is a silent no-op
// rather than a fresh refused job naming the type the old daemon does not know
// - a red line, undismissable for ten minutes, produced by the key meant to
// clear one.
const RequestDismiss = "dismiss"
```

Add the field to `Snapshot`, below `Jobs`:

```go
	// DaemonBin is the stamp the daemon took of its own image at startup.
	// Additive for the same reason Jobs is: an old daemon omits it and a new
	// client reads the zero Stamp, which it correctly reports as outdated.
	DaemonBin selfbin.Stamp `json:"daemon_bin,omitempty"`
```

Add `"github.com/jzinkduda/vigil/internal/selfbin"` to the imports.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/protocol/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Commit**

```bash
git add internal/protocol/
git commit -m "feat(protocol): add the dismiss request type and the daemon binary stamp"
```

---

### Task 3: Daemon - `dismissTerminal` and request routing

**Files:**
- Modify: `internal/daemon/jobs.go`
- Modify: `internal/daemon/daemon.go` (the `case req := <-s.requests:` arm in `Run`)
- Test: `internal/daemon/jobs_test.go`

**Interfaces:**
- Consumes: `protocol.RequestDismiss` from Task 2.
- Produces: `func (j *jobs) dismissTerminal() bool` - true when at least one job was removed.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/jobs_test.go`:

```go
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
```

Append to `internal/daemon/daemon_test.go` (adjust the harness to whatever that file already uses to drive `Run`; if it has none, drive `handleRequest` directly and say so in the commit):

```go
func TestRunRoutesADismissFrameToDismissTerminal(t *testing.T) {
	s := &Server{jobs: newJobs(&config.Config{}, nil, nil, func(string, ...any) {})}
	s.jobs.byID = map[string]*protocol.Job{"f": {ID: "f", State: protocol.JobFailed}}
	s.jobs.order = []string{"f"}

	s.handleRequest(&protocol.Request{Version: protocol.Version, Type: protocol.RequestDismiss})

	if got := s.jobs.snapshot(); len(got) != 0 {
		t.Fatalf("the failed job survived a dismiss frame: %+v", got)
	}
}

// The unknown-type refusal must keep working. Routing dismiss must not turn
// the default arm into a refusal of its own: submit already owns that.
func TestRunStillRefusesAnUnknownRequestType(t *testing.T) {
	s := &Server{jobs: newJobs(&config.Config{}, nil, nil, func(string, ...any) {})}

	s.handleRequest(&protocol.Request{Version: protocol.Version, Type: "nonsense", ID: "x", Input: "in"})

	got := s.jobs.snapshot()
	if len(got) != 1 || got[0].State != protocol.JobRefused {
		t.Fatalf("got %+v, want one refused job", got)
	}
	if !strings.Contains(got[0].Status, "nonsense") {
		t.Fatalf("refusal reason %q does not name the type", got[0].Status)
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/daemon/ -run 'DismissTerminal|EmptyIDRequest|DismissFrame|UnknownRequestType' -v`
Expected: build failure - `j.dismissTerminal undefined`, `s.handleRequest undefined`.

- [ ] **Step 3: Write the implementation**

In `internal/daemon/jobs.go`, after `refuse`:

```go
// dismissTerminal drops every job the user has already been told about and
// can do nothing more with. Queued and running jobs stay: removing those is
// cancellation, which owns a process group and a half-made worktree and is a
// different feature. Succeeded jobs stay because they expire in ten seconds
// anyway.
func (j *jobs) dismissTerminal() bool {
	j.mu.Lock()
	defer j.mu.Unlock()

	kept := make([]string, 0, len(j.order))
	removed := false
	for _, id := range j.order {
		job, ok := j.byID[id]
		if ok && (job.State == protocol.JobFailed || job.State == protocol.JobRefused) {
			delete(j.byID, id)
			delete(j.cwds, id)
			removed = true
			continue
		}
		kept = append(kept, id)
	}
	j.order = kept
	return removed
}
```

In `internal/daemon/daemon.go`, replace the body of the request arm in `Run`:

```go
		case req := <-s.requests:
			s.handleRequest(req)
```

and add the method beside `publishJobs`:

```go
// handleRequest routes one client frame. The default arm stays submit rather
// than becoming a refusal: submit's reason switch already produces the
// unsupported-type refusal, and that behaviour must not move.
func (s *Server) handleRequest(req *protocol.Request) {
	if s.jobs == nil || req == nil {
		return
	}
	switch req.Type {
	case protocol.RequestDismiss:
		if !s.jobs.dismissTerminal() {
			return
		}
	default:
		s.jobs.submit(req)
	}
	// Immediately, not on the next tick. The submitting CLI waits to see its
	// id in a snapshot, and on a cold daemon the next tick is behind a first
	// poll that runs git and gh across every session.
	s.publishJobs(s.jobs.snapshot())
}
```

Move the existing explanatory comment from the old arm into `handleRequest` as above; do not leave a duplicate behind.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/daemon/ -v`
Expected: PASS, including every pre-existing dispatch and refusal test.

- [ ] **Step 5: Verify the tests can fail (mutation check)**

Delete the `case protocol.RequestDismiss:` arm so dismiss falls to `submit`. `TestRunRoutesADismissFrameToDismissTerminal` must fail while `TestRunStillRefusesAnUnknownRequestType` still passes - that pairing is what proves the two paths are pinned independently. Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): clear failed and refused jobs on a dismiss request"
```

---

### Task 4: Daemon - publish its own binary stamp

**Files:**
- Modify: `internal/daemon/daemon.go` (`Server` struct, `New`, `poll`)
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `selfbin.Prober` from Task 1, `Snapshot.DaemonBin` from Task 2.
- Produces: `Server.BinStamp selfbin.Stamp`, set once in `New` and copied into every snapshot.

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/daemon_test.go`:

```go
func TestPollPublishesTheDaemonBinaryStamp(t *testing.T) {
	want := selfbin.Stamp{Size: 4242, ModNano: 99}
	s := newTestServer(t)
	s.BinStamp = want

	s.poll(context.Background())

	s.mu.Lock()
	latest := s.latest
	s.mu.Unlock()
	if latest == nil {
		t.Fatal("poll published no snapshot")
	}
	if latest.DaemonBin != want {
		t.Fatalf("DaemonBin = %+v, want %+v", latest.DaemonBin, want)
	}
}

func TestNewStampsTheRunningBinary(t *testing.T) {
	s := New(&config.Config{}, fetch.NewMockCommander())
	if s.BinStamp.Zero() {
		t.Fatal("New left BinStamp zero; a client reads that as an outdated daemon")
	}
}
```

`newTestServer` is whatever helper `daemon_test.go` already uses to build a `Server` with a stub collector. If there is none, build the `Server` literal inline the way the neighbouring tests do. Add the `selfbin` import.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/daemon/ -run 'DaemonBinaryStamp|StampsTheRunningBinary' -v`
Expected: build failure - `unknown field BinStamp`.

- [ ] **Step 3: Write the implementation**

Add to the `Server` struct, below `Log`:

```go
	// BinStamp identifies the image this daemon is running. Published in every
	// snapshot so a client - which stats the same path for its own restart
	// check - can tell the user the daemon is behind. The daemon never acts on
	// it: restarting itself would drop every client connection, so every panel
	// would bounce through daemon-lost on every install.
	BinStamp selfbin.Stamp
```

In `New`, after the `srv := &Server{...}` literal and before the jobs wiring:

```go
	srv.BinStamp, _ = selfbin.Prober{}.Current()
```

In `poll`, add the field to the snapshot literal:

```go
	snap := &protocol.Snapshot{
		Version:   protocol.Version,
		Timestamp: time.Now().Unix(),
		Sessions:  sessions,
		Jobs:      jobList,
		DaemonBin: s.BinStamp,
	}
```

Add `"github.com/jzinkduda/vigil/internal/selfbin"` to the imports.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/daemon/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): publish the daemon's own binary stamp in every snapshot"
```

---

### Task 5: Client - carry `DaemonBin` through to the model

**Files:**
- Modify: `internal/model/messages.go` (`SnapshotMsg`)
- Modify: `internal/model/client.go` (the `SnapshotMsg` construction)
- Modify: `internal/model/model.go` (`Model` field, both snapshot handlers)
- Test: `internal/model/client_test.go`

**Interfaces:**
- Consumes: `Snapshot.DaemonBin` from Task 2.
- Produces: `SnapshotMsg.DaemonBin selfbin.Stamp`, and `Model.daemonBin selfbin.Stamp` holding the last value seen.

- [ ] **Step 1: Write the failing test**

Append to `internal/model/client_test.go`:

```go
func TestASnapshotCarriesTheDaemonBinaryStampIntoTheModel(t *testing.T) {
	m := newTestModel()
	m.daemonConn = nil
	m.daemonDecoder = &protocol.Decoder{}
	want := selfbin.Stamp{Size: 77, ModNano: 5}

	next, _ := m.handleSnapshot(SnapshotMsg{Sessions: fixtureSessions(), DaemonBin: want})

	if got := next.(Model).daemonBin; got != want {
		t.Fatalf("daemonBin = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./internal/model/ -run 'CarriesTheDaemonBinaryStamp' -v`
Expected: build failure - `unknown field DaemonBin in SnapshotMsg`.

- [ ] **Step 3: Write the implementation**

In `internal/model/messages.go`, beside `Jobs` on `SnapshotMsg`:

```go
	// DaemonBin is the stamp the daemon took of its own image at startup.
	// Zero on the self-polling path, and zero from a daemon too old to send
	// it, which is the same thing as far as a client is concerned.
	DaemonBin selfbin.Stamp
```

In `internal/model/client.go`, at the `return SnapshotMsg{...}` that carries `snap.Jobs`:

```go
		return SnapshotMsg{Sessions: snap.Sessions, Jobs: snap.Jobs, DaemonBin: snap.DaemonBin, Epoch: epoch}
```

In `internal/model/model.go`, add to the `Model` struct beside `lastSnapshot`:

```go
	// daemonBin is the last stamp a daemon published for its own image.
	daemonBin selfbin.Stamp
```

Set it in **both** places `m.jobs = msg.Jobs` appears in `handleSnapshot` - the local branch and the daemon branch:

```go
	m.jobs = msg.Jobs
	m.daemonBin = msg.DaemonBin
```

Add the `selfbin` import to each modified file.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/model/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/model/
git commit -m "feat(model): carry the daemon's binary stamp from the snapshot"
```

---

### Task 6: Client - track the on-disk binary and raise the restart flag

**Files:**
- Modify: `internal/model/model.go`
- Test: `internal/model/model_test.go` (or a new `internal/model/restart_test.go`)

**Interfaces:**
- Consumes: `selfbin.Prober`, `selfbin.Stamp` from Task 1.
- Produces:
  - `Model.binProber selfbin.Prober`, `Model.binAtStart selfbin.Stamp`, `Model.binOnDisk selfbin.Stamp`, `Model.lastBinCheck time.Time`, `Model.startedAt time.Time`, `Model.restartRequested bool`
  - `func (m Model) RestartRequested() bool` - exported, read by `main` after `p.Run()`
  - `func (m *Model) checkBinary(now time.Time)` - called from the two tick handlers

Nothing execs in this task. The flag is set and nothing acts on it yet, which is what keeps this task independently reviewable.

- [ ] **Step 1: Write the failing tests**

Create `internal/model/restart_test.go`:

```go
package model

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/selfbin"
)

type stubInfo struct {
	fs.FileInfo
	size int64
}

func (s stubInfo) Size() int64        { return s.size }
func (s stubInfo) ModTime() time.Time { return time.Unix(0, 0) }

func proberReturning(size int64, err error) selfbin.Prober {
	return selfbin.Prober{
		Executable: func() (string, error) { return "/bin/vigil", nil },
		Stat: func(string) (fs.FileInfo, error) {
			if err != nil {
				return nil, err
			}
			return stubInfo{size: size}, nil
		},
	}
}

// binModel returns a model that has been running long enough to be past the
// startup floor, stamped at size 100.
func binModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.startedAt = time.Now().Add(-time.Hour)
	m.binProber = proberReturning(100, nil)
	stamp, ok := m.binProber.Current()
	if !ok {
		t.Fatal("the stub prober failed")
	}
	m.binAtStart = stamp
	m.binOnDisk = stamp
	return m
}

func TestAnUnchangedBinaryDoesNotRequestARestart(t *testing.T) {
	m := binModel(t)
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("restart requested for an unchanged binary")
	}
}

func TestAChangedBinaryRequestsARestart(t *testing.T) {
	m := binModel(t)
	m.binProber = proberReturning(200, nil)
	m.checkBinary(time.Now())
	if !m.restartRequested {
		t.Fatal("no restart requested after the binary changed")
	}
	if m.binOnDisk.Size != 200 {
		t.Fatalf("binOnDisk.Size = %d, want 200", m.binOnDisk.Size)
	}
}

func TestAFailedStatIsReadAsUnchanged(t *testing.T) {
	m := binModel(t)
	m.binProber = proberReturning(0, errors.New("boom"))
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("a stat failure requested a restart; it must fail closed")
	}
	if m.binOnDisk.Size != 100 {
		t.Fatal("a stat failure overwrote the last good on-disk stamp")
	}
}

func TestTheCheckIsRateLimited(t *testing.T) {
	m := binModel(t)
	now := time.Now()
	m.checkBinary(now)
	m.binProber = proberReturning(200, nil)
	m.checkBinary(now.Add(time.Second))
	if m.restartRequested {
		t.Fatal("the check ran again within the rate limit window")
	}
	m.checkBinary(now.Add(binCheckInterval + time.Second))
	if !m.restartRequested {
		t.Fatal("the check never ran again after the rate limit window")
	}
}

func TestAFreshlyStartedProcessDoesNotRestart(t *testing.T) {
	m := binModel(t)
	m.startedAt = time.Now()
	m.binProber = proberReturning(200, nil)
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("a process restarted within the startup floor; a bad stamp would spin")
	}
}

func TestARestartWaitsForAnOpenPrompt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(m *Model)
	}{
		{"confirm prompt", func(m *Model) { m.confirmAction = ConfirmCleanup }},
		{"dispatch prompt", func(m *Model) { m.dispatchActive = true }},
		{"multi-selection", func(m *Model) { m.selected["alpha"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := binModel(t)
			tc.apply(&m)
			m.binProber = proberReturning(200, nil)
			m.checkBinary(time.Now())
			if m.restartRequested {
				t.Fatalf("restarted with a %s open, losing unsaved intent", tc.name)
			}
		})
	}
}

func TestRestartRequestedIsReadable(t *testing.T) {
	m := binModel(t)
	if m.RestartRequested() {
		t.Fatal("RestartRequested true on a fresh model")
	}
	m.restartRequested = true
	if !m.RestartRequested() {
		t.Fatal("RestartRequested does not report the flag")
	}
}
```

`ConfirmCleanup` is a guess - check the real name of the confirm enum's cleanup value in `model.go` and use it. `binModel` sets `cancel` nowhere, which is fine here: none of these tests reach the quit branch.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/model/ -run 'Binary|Restart|RateLimited|FreshlyStarted' -v`
Expected: build failure - `undefined: binCheckInterval`, `m.checkBinary undefined`.

- [ ] **Step 3: Write the implementation**

Add the constants near the top of `internal/model/model.go`:

```go
// binCheckInterval is how often a client stats its own binary. A stat is
// cheap; doing it on every 1s tick would still be pointless.
const binCheckInterval = 10 * time.Second

// binRestartFloor is the anti-loop guard. A re-exec'd process stamps at its
// own startup, so a stable binary never fires twice - that is the structural
// defence. This bounds the damage if a stat is somehow nondeterministic: one
// exec per floor rather than a hot spin that makes a panel unusable.
const binRestartFloor = 30 * time.Second
```

Add the fields to `Model`, beside `lastSpawn`:

```go
	// binProber stats this process's own image. Zero value is the real one.
	binProber   selfbin.Prober
	binAtStart  selfbin.Stamp
	binOnDisk   selfbin.Stamp
	lastBinCheck time.Time
	startedAt    time.Time

	// restartRequested asks the caller to re-exec after the program exits.
	// The exec cannot happen here: Bubble Tea owns raw mode and the alt
	// screen, and a process exec'd from inside Update inherits a terminal
	// nobody restored.
	restartRequested bool
```

In `newModel`, after the context is built:

```go
	startStamp, _ := selfbin.Prober{}.Current()
```

and set `startedAt: time.Now()`, `binAtStart: startStamp`, `binOnDisk: startStamp` in the `Model` literal.

Add the method beside `daemonHealth`:

```go
// RestartRequested reports whether this process asked to be replaced by a
// newer image on disk. Read by main after p.Run() returns and Bubble Tea has
// restored the terminal.
func (m Model) RestartRequested() bool { return m.restartRequested }

func (m *Model) checkBinary(now time.Time) {
	if m.restartRequested {
		return
	}
	if !m.lastBinCheck.IsZero() && now.Sub(m.lastBinCheck) < binCheckInterval {
		return
	}
	m.lastBinCheck = now

	stamp, ok := m.binProber.Current()
	if !ok {
		return
	}
	m.binOnDisk = stamp
	if stamp == m.binAtStart {
		return
	}
	if now.Sub(m.startedAt) < binRestartFloor {
		return
	}
	// Unsaved user intent. All three are states the user is actively in and
	// about to leave, so no indicator is needed for the wait.
	if m.confirmAction != ConfirmNone || m.dispatchActive || len(m.selected) > 0 {
		return
	}
	m.restartRequested = true
}
```

Call it from the two 1s tick handlers in `Update`, each immediately after its epoch guard passes:

- `case CollectTickMsg:` - after the `m.daemonConn != nil` check, before `m.startPoll(false)`.
- `case RenderTickMsg:` - after the `m.daemonDecoder == nil` check, before the reschedule.

Both need `m` to be addressable; the handlers take a value receiver, so call `m.checkBinary(time.Now())` on the local copy and return the modified `m` as those cases already do.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/model/ -v`
Expected: PASS.

- [ ] **Step 5: Verify the tests can fail (mutation check)**

Delete the `now.Sub(m.startedAt) < binRestartFloor` guard. `TestAFreshlyStartedProcessDoesNotRestart` must fail. Revert. Then delete the `confirmAction`/`dispatchActive`/`selected` guard; all three subtests of `TestARestartWaitsForAnOpenPrompt` must fail. Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/model/
git commit -m "feat(model): notice when this process's binary has been replaced"
```

---

### Task 7: `main` - re-exec after the program exits

**Files:**
- Modify: `main.go` (`runTUI`, `runPanel`, a new `execSelf` seam)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `model.Model.RestartRequested()` from Task 6.
- Produces: `var execSelf = syscall.Exec`, `type restartRequester interface { RestartRequested() bool }`, and `func restartIfRequested(final tea.Model) error`.

**Why an interface rather than a type assertion to `model.Model`:** asserting the concrete type would force a test in package `main` to build a real `model.Model` with the flag set, and the flag is unexported. The only ways out are an exported test-only setter on `Model` - production API that exists solely for a test, in a package that has none - or this one-line interface. `model.Model` satisfies it for free because `RestartRequested()` already has a value receiver.

- [ ] **Step 1: Write the failing test**

Append to `main_test.go`:

```go
// fakeFinalModel stands in for the model tea.Program returns. It exists so
// this test can drive the restart branch without building a real Model, whose
// flag is unexported.
type fakeFinalModel struct{ restart bool }

func (f fakeFinalModel) Init() tea.Cmd                           { return nil }
func (f fakeFinalModel) Update(tea.Msg) (tea.Model, tea.Cmd)     { return f, nil }
func (f fakeFinalModel) View() string                            { return "" }
func (f fakeFinalModel) RestartRequested() bool                  { return f.restart }

func TestRestartIfRequestedExecsTheSamePathAndArgv(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })

	var gotPath string
	var gotArgv []string
	var gotEnv []string
	execSelf = func(path string, argv []string, envv []string) error {
		gotPath = path
		gotArgv = argv
		gotEnv = envv
		return nil
	}

	if err := restartIfRequested(fakeFinalModel{restart: true}); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if gotPath != exe {
		t.Fatalf("exec path = %q, want %q", gotPath, exe)
	}
	if len(gotArgv) == 0 || gotArgv[0] != exe {
		t.Fatalf("argv = %v, want argv[0] to be the executable", gotArgv)
	}
	if len(gotEnv) != len(os.Environ()) {
		t.Fatalf("env has %d entries, want the process environ's %d", len(gotEnv), len(os.Environ()))
	}
}

func TestRestartIfRequestedDoesNothingWithoutTheFlag(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })
	execSelf = func(string, []string, []string) error {
		t.Fatal("exec'd without a restart request")
		return nil
	}
	if err := restartIfRequested(fakeFinalModel{restart: false}); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}
}

// A tea.Model that knows nothing about restarting must be ignored rather than
// panicking the process on its way out.
func TestRestartIfRequestedIgnoresAModelWithoutTheMethod(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })
	execSelf = func(string, []string, []string) error {
		t.Fatal("exec'd for a model that cannot request a restart")
		return nil
	}
	if err := restartIfRequested(nil); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}
}

// The real Model must satisfy restartRequester, or the interface assertion in
// restartIfRequested silently never fires in production. Nothing else catches
// that: every other test here uses the fake.
func TestTheRealModelSatisfiesRestartRequester(t *testing.T) {
	var _ restartRequester = model.New(&config.Config{}, fetch.NewMockCommander())
}
```

No test-only setter on `Model`. The interface is what makes that unnecessary, and `TestTheRealModelSatisfiesRestartRequester` is what stops the interface drifting away from the real type.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test . -run 'RestartIfRequested' -v`
Expected: build failure - `undefined: execSelf`, `undefined: restartIfRequested`.

- [ ] **Step 3: Write the implementation**

In `main.go`, beside `var version`:

```go
// execSelf is a seam. A test that called syscall.Exec directly would replace
// the test binary with a second copy of vigil.
var execSelf = syscall.Exec
```

Add:

```go
// restartRequester is what restartIfRequested needs of a finished program's
// model. An interface rather than a concrete assertion to model.Model so a
// test can supply a fake without model exporting a setter that exists only
// for tests.
type restartRequester interface{ RestartRequested() bool }

// restartIfRequested replaces this process with the newer image on disk. It
// runs after p.Run() has returned, because Bubble Tea restores the terminal on
// its way out and an exec from inside Update would hand the new process raw
// mode and an alt screen nobody left.
func restartIfRequested(final tea.Model) error {
	m, ok := final.(restartRequester)
	if !ok || !m.RestartRequested() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return execSelf(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}
```

Rewrite the two runners:

```go
func runTUI(cfg *config.Config, cmd fetch.Commander) error {
	final, err := tea.NewProgram(model.New(cfg, cmd), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return restartIfRequested(final)
}

// runPanel renders the compact session list for a single tmux pane. It shares
// every code path with the dashboard, so panel and dashboard can never
// disagree about state.
func runPanel(cfg *config.Config, cmd fetch.Commander) error {
	final, err := tea.NewProgram(model.NewPanel(cfg, cmd), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return restartIfRequested(final)
}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race . -v`
Expected: PASS, including every pre-existing `run` test.

- [ ] **Step 5: Verify the test can fail (mutation check)**

Make `restartIfRequested` return nil unconditionally. `TestRestartIfRequestedExecsTheSamePathAndArgv` must fail. Revert.

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: re-exec a client whose binary was replaced on disk"
```

---

### Task 8: Client - esc dismisses failed and refused jobs

**Files:**
- Modify: `internal/model/model.go` (`Model` struct, the `keys.Cancel` case, a new command)
- Test: `internal/model/model_test.go` (or a new `internal/model/dismiss_test.go`)

**Interfaces:**
- Consumes: `protocol.RequestDismiss` from Task 2, `Model.jobs`, `Model.daemonConn`.
- Produces:
  - `Model.daemonWriteMu *sync.Mutex`
  - `func (m Model) hasDismissableJob() bool`
  - `func dismissJobsCmd(conn net.Conn, mu *sync.Mutex) tea.Cmd`

- [ ] **Step 1: Write the failing tests**

Create `internal/model/dismiss_test.go`:

```go
package model

import (
	"net"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func escKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

// escModel is newTestModel plus the two things the esc path needs and the
// shared fixture does not set. cancel is nil in newTestModel, and the quit
// branch calls it - without this every test here nil-panics rather than
// failing usefully.
func escModel() Model {
	m := newTestModel()
	m.cancel = func() {}
	m.daemonWriteMu = &sync.Mutex{}
	return m
}

// isQuit reports whether a command is tea.Quit. There is no existing
// convention for this in the package; this is it. Only call it on a command
// that is safe to run - the dismiss command writes to a socket.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestEscDismissesAFailedJobInsteadOfQuitting(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobFailed}}

	next, cmd := m.Update(escKey())
	if cmd == nil {
		t.Fatal("esc produced no command; it should have sent a dismiss")
	}
	if next.(Model).hasDismissableJob() != true {
		t.Fatal("esc cleared the job locally; the daemon owns the job table")
	}

	done := make(chan *protocol.Request, 1)
	go func() {
		req, err := protocol.NewRequestDecoder(server).Next()
		if err != nil {
			done <- nil
			return
		}
		done <- req
	}()
	go cmd()

	select {
	case req := <-done:
		if req == nil {
			t.Fatal("no readable request frame reached the daemon")
		}
		if req.Type != protocol.RequestDismiss {
			t.Fatalf("Type = %q, want %q", req.Type, protocol.RequestDismiss)
		}
		if req.ID != "" {
			t.Fatalf("ID = %q, want empty so an old daemon drops it silently", req.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the dismiss frame")
	}
}

func TestEscStillClearsAConfirmPromptBeforeDismissing(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.confirmAction = ConfirmCleanup
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobFailed}}

	next, cmd := m.Update(escKey())

	after := next.(Model)
	if after.confirmAction != ConfirmNone {
		t.Fatal("esc did not clear the confirm prompt first")
	}
	if cmd != nil {
		t.Fatal("esc sent a command in the same press that cleared the prompt")
	}
}

func TestEscQuitsWhenThereIsNothingToDismiss(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobRunning}}

	_, cmd := m.Update(escKey())

	if !isQuit(cmd) {
		t.Fatal("esc did not quit with only a running job present")
	}
}

func TestASelfPollingClientQuitsOnEsc(t *testing.T) {
	m := escModel()
	m.daemonConn = nil
	m.jobs = nil

	_, cmd := m.Update(escKey())

	if !isQuit(cmd) {
		t.Fatal("a client with no daemon and no jobs did not quit on esc")
	}
}

func TestHasDismissableJobCoversRefusedAndFailedOnly(t *testing.T) {
	for state, want := range map[string]bool{
		protocol.JobFailed:    true,
		protocol.JobRefused:   true,
		protocol.JobQueued:    false,
		protocol.JobRunning:   false,
		protocol.JobSucceeded: false,
	} {
		m := newTestModel()
		m.jobs = []protocol.Job{{ID: "a", State: state}}
		if got := m.hasDismissableJob(); got != want {
			t.Errorf("state %q: hasDismissableJob = %v, want %v", state, got, want)
		}
	}
}
```

Two things about these tests are deliberate and were checked against the real code:

- **`newTestModel` leaves `cancel` nil**, and the quit branch calls `m.cancel()`. Without `escModel`'s `cancel = func() {}` every test here nil-panics instead of failing usefully. No existing test exercises the esc-quit path, so there is no prior art to copy.
- **There is no `quitting` field.** Esc returns `tea.Quit`, so `isQuit` invokes the command and asserts `tea.QuitMsg`. Do not add a field to make the assertion convenient.

`ConfirmCleanup` is a guess - check the real name of the confirm enum's cleanup value in `model.go`.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/model/ -run 'Esc|Dismissable' -v`
Expected: build failure - `m.hasDismissableJob undefined`, `unknown field daemonWriteMu`.

- [ ] **Step 3: Write the implementation**

Add to the `Model` struct, beside `daemonConn`:

```go
	// daemonWriteMu serializes client-to-daemon writes. net.Conn is safe for
	// concurrent read and write, but two concurrent writes can interleave into
	// one malformed frame - which the daemon tolerates and drops, so the
	// dismiss would silently not happen. A pointer, so the value copies Bubble
	// Tea makes all share one.
	daemonWriteMu *sync.Mutex
```

Set `daemonWriteMu: &sync.Mutex{}` in `newModel`'s `Model` literal.

Add beside `daemonHealth`:

```go
func (m Model) hasDismissableJob() bool {
	for _, j := range m.jobs {
		if j.State == protocol.JobFailed || j.State == protocol.JobRefused {
			return true
		}
	}
	return false
}
```

Add the command beside the other `tea.Cmd` builders:

```go
// dismissJobsCmd asks the daemon to clear its terminal jobs. It goes out on
// the client's existing connection - the daemon's per-connection reader takes
// requests from the same socket it writes snapshots to - so there is no dial,
// no daemon spawn and no ack to wait for. The job vanishing from the next
// snapshot is the ack.
func dismissJobsCmd(conn net.Conn, mu *sync.Mutex) tea.Cmd {
	return func() tea.Msg {
		mu.Lock()
		defer mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
		if err := protocol.EncodeRequest(conn, &protocol.Request{
			Version: protocol.Version,
			Type:    protocol.RequestDismiss,
		}); err != nil {
			return ActionResultMsg{Action: "dismiss", OK: false, Message: err.Error()}
		}
		return nil
	}
}
```

Extend the `keys.Cancel` case, inserting the new layer between the existing unwind and the quit:

```go
	case key.Matches(msg, keys.Cancel):
		if m.confirmAction != ConfirmNone || len(m.selected) > 0 || m.dispatchActive {
			m.confirmAction = ConfirmNone
			m.confirmName = ""
			m.selected = make(map[string]bool)
			m.dispatchActive = false
			return m, nil
		}
		if m.daemonConn != nil && m.hasDismissableJob() {
			return m, dismissJobsCmd(m.daemonConn, m.daemonWriteMu)
		}
		m.cancel()
		return m, tea.Quit
	}
```

Add `"net"` and `"sync"` to the imports if they are not already present.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/model/ -v`
Expected: PASS.

- [ ] **Step 5: Verify the tests can fail (mutation check)**

Move the dismiss layer *above* the confirm-prompt layer. `TestEscStillClearsAConfirmPromptBeforeDismissing` must fail. Revert. Then make `hasDismissableJob` return true for `JobRunning`; `TestEscQuitsWhenThereIsNothingToDismiss` must fail. Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/model/
git commit -m "feat(model): esc clears failed and refused dispatch jobs"
```

---

### Task 9: Client - render `daemon outdated`

**Files:**
- Modify: `internal/model/model.go` (`daemonHealth`)
- Test: `internal/model/restart_test.go`

**Interfaces:**
- Consumes: `Model.daemonBin` from Task 5, `Model.binOnDisk` from Task 6.
- Produces: nothing new; `daemonHealth` gains a case.

- [ ] **Step 1: Write the failing tests**

Append to `internal/model/restart_test.go`:

```go
func outdatedModel(t *testing.T) Model {
	t.Helper()
	m := binModel(t)
	m.daemonConn = &net.TCPConn{}
	m.daemonReady = true
	m.lastSnapshot = time.Now()
	m.cfg = &config.Config{}
	return m
}

func TestDaemonHealthReportsAnOutdatedDaemon(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 100}
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

func TestDaemonHealthSaysNothingWhenTheDaemonMatchesDisk(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 200}
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

// A daemon too old to send the field is too old. Absent reads as outdated.
func TestAnAbsentStampReadsAsOutdated(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{}
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

// The client's own probe failing must not accuse the daemon.
func TestAnUnknownOnDiskStampSaysNothing(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{}
	m.daemonBin = selfbin.Stamp{Size: 100}
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

func TestStalenessOutranksOutdatedness(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 100}
	m.lastSnapshot = time.Now().Add(-time.Hour)
	if got := m.daemonHealth(); !strings.HasPrefix(got, "daemon stale") {
		t.Fatalf("daemonHealth = %q, want the staleness marker to win", got)
	}
}
```

Add `net`, `strings`, and the `config` import.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/model/ -run 'Outdated|MatchesDisk|AbsentStamp|StalenessOutranks' -v`
Expected: FAIL - `daemonHealth = "", want "daemon outdated"`.

- [ ] **Step 3: Write the implementation**

Extend `daemonHealth`, adding the case *after* the staleness check and before the final `return ""`:

```go
	if age := time.Since(m.lastSnapshot); age > m.staleAfter() {
		return fmt.Sprintf("daemon stale %ds", int(age.Seconds()))
	}
	// Lowest precedence: a daemon that is behind is still feeding correct
	// data, it is just missing whatever the newer image adds. An absent stamp
	// counts, because a daemon too old to send the field is too old.
	if !m.binOnDisk.Zero() && m.daemonBin != m.binOnDisk {
		return "daemon outdated"
	}
	return ""
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test -race ./internal/model/ -v`
Expected: PASS.

- [ ] **Step 5: Verify the test can fail (mutation check)**

Drop the `!m.binOnDisk.Zero()` guard. `TestAnUnknownOnDiskStampSaysNothing` must fail. Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/model/
git commit -m "feat(model): say so when the daemon is running an older image"
```

---

### Task 10: Full verification and documentation

**Files:**
- Modify: `CLAUDE.md` (Key Conventions)
- Modify: `README.md` if it documents keybindings - check first
- Create: `docs/superpowers/2026-07-30-binary-refresh-handoff.md`

- [ ] **Step 1: Run the whole suite and the linter**

Run: `make test && make lint`
Expected: both clean. Do not proceed past a failure; do not claim completion without pasting the output.

- [ ] **Step 2: Verify on a real machine**

The phase 4 handoff records that both of that phase's real defects were invisible to the test suite. These checks are not optional, and each one must be **observed**, not reasoned about.

- [ ] `make install`, then watch an already-running panel restart itself within ~10s and come back with its table intact.
- [ ] With the daemon still on the old image, confirm a panel shows `daemon outdated`.
- [ ] Restart the daemon; confirm the marker clears.
- [ ] Dispatch something that fails, confirm the red line appears in every panel, press esc in one, and confirm it clears in **all** of them.
- [ ] Press esc again with no job line and confirm vigil quits.
- [ ] Confirm `vigil dispatch` still exits 0 on acceptance against the new daemon.

Run these against the developer's real setup only after the isolated suite is green. If any check needs a throwaway tmux server, give it its own `TMUX_TMPDIR` and kill only PIDs the check started, as phase 4's harness did.

- [ ] **Step 3: Update `CLAUDE.md`**

Add to Key Conventions:

- `esc` unwinds one layer per press: confirm prompt / multi-selection / dispatch prompt, then failed and refused dispatch jobs, then quit. The dismiss layer sends a `RequestDismiss` frame **with an empty ID**, so an old daemon drops it via `jobs.submit`'s empty-ID guard rather than registering an undismissable refusal for a type it does not know.
- Every vigil client stats its own executable every `binCheckInterval` and re-execs when size or mtime changes, deferring while a prompt or a selection is open and never within `binRestartFloor` of its own start. The exec happens in `main` after `p.Run()` returns, because Bubble Tea owns the terminal inside `Update`.
- **The daemon never restarts itself.** It publishes `Snapshot.DaemonBin` and clients render `daemon outdated`. Restarting it would drop every connection, so every panel would bounce through daemon-lost on every install. This was designed, rejected, and the reasoning is in the spec - do not reintroduce it.

- [ ] **Step 4: Write the handoff**

`docs/superpowers/2026-07-30-binary-refresh-handoff.md`, following the shape of the phase 4 handoff: what landed, what was verified and how, what was **not** verified, and the landmines. Carry forward the three landmines the spec names (the dismiss/ack race if dismissal is ever extended to succeeded jobs; a re-exec dropping cursor, filter and sort; `daemon outdated` misreading a daemon launched from a different path). Record `ExecCommander.Run`'s missing `WaitDelay` as still outstanding.

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md docs/
git commit -m "docs: record the binary-refresh and dismiss work"
```

---

## Notes for the implementer

- **Report BLOCKED with evidence rather than guessing.** In phase 4, eleven briefs contained defects written by the plan's author, and every implementer who reported BLOCKED with evidence was right. The most common shape was a brief whose *test* contradicted the brief's *implementation* elsewhere in the same brief. If you see that here, say so.
- **Names in this plan are guesses where they refer to existing code.** `ConfirmCleanup`, `newTestServer`, and the mechanism the existing tests use to assert "esc quit" must be checked against the real code, not copied from here.
- The two tick handlers in Task 6 are the only call sites for `checkBinary`. Do not add a third tick.

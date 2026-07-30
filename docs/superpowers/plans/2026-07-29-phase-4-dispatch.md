# Phase 4: Dispatch Through vigild — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `vigil dispatch <url-or-id>` submits a job to `vigild`, which runs the existing workflow scripts, streams live status into every client's snapshot, and lets the menu bar drop its `tmux display-popup` tunnel.

**Architecture:** The unix socket becomes bidirectional. Clients write `Request` frames; the daemon never does, so direction alone disambiguates the two frame types and `protocol.Version` stays 1. The daemon grows one reader goroutine per connection and a serialized job queue whose state is published as an additive `Snapshot.Jobs` field — the snapshot doubles as the submission ack. Jobs run as children of `vigild` on their own goroutines, with output streamed line by line into a job's `Status`.

**Tech Stack:** Go 1.x, Bubble Tea, lipgloss, unix domain sockets, newline-delimited JSON. Bash + bats-core in `~/dotfiles`.

**Spec:** `docs/superpowers/specs/2026-07-29-phase-4-dispatch-design.md`. Read it before Task 1. Read `docs/superpowers/2026-07-29-phase-3-handoff.md` for the landmines.

## Global Constraints

- **Two repositories.** `~/vigil` (Tasks 1-8) and `~/dotfiles` (Tasks 9-10). Neither half works without the other. Task 11 verifies the pair.
- **`make test` is `go test -race ./...`. `-race` is not optional** — the daemon's design is a concurrency claim, and this phase adds a second goroutine to every connection.
- **`make lint` must report 0 issues.**
- `protocol.Version` stays **1**. Do not bump it.
- **Every subprocess goes through an injected interface.** The only permitted direct `exec` sites are `internal/fetch/cmd.go` and the daemon spawn.
- **No client-side transition effects.** `transition.Runner` is constructed only in `internal/daemon`. This phase adds code next to that boundary; do not cross it.
- **Any test reaching `config.Load(config.ConfigPath())` must set its own `HOME`**, or it reads the developer's real `~/.config/vigil/config.toml`.
- **`newTestModel` leaves `cachePath` empty deliberately.** Do not set it; that is what stops the suite writing the developer's real cache.
- `internal/model` has a `TestMain` that stubs `daemonSpawner`. Leave it in place — without it, tests fork real detached daemons.
- Prefer no code comments. Comment only where the meaning cannot be inferred from the code.
- Do not use the em dash in code, comments, or docs. Use a plain dash.

---

### Task 1: Protocol — Request, Job, Snapshot.Jobs

**Files:**
- Modify: `internal/protocol/protocol.go`
- Test: `internal/protocol/protocol_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `protocol.Request{Version int, Type string, ID string, Input string, Cwd string}`; `protocol.Job{ID, Input, State, Status string, Started, Ended int64}`; `protocol.Snapshot.Jobs []Job`; constants `RequestDispatch = "dispatch"`, `JobQueued = "queued"`, `JobRunning = "running"`, `JobSucceeded = "succeeded"`, `JobFailed = "failed"`; `EncodeRequest(w io.Writer, req *Request) error`; `NewRequestDecoder(r io.Reader) *RequestDecoder` with `(*RequestDecoder).Next() (*Request, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/protocol/protocol_test.go`:

```go
func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := &Request{
		Version: Version,
		Type:    RequestDispatch,
		ID:      "job-1",
		Input:   "sc-12345",
		Cwd:     "/Users/x/portal",
	}
	if err := EncodeRequest(&buf, want); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := NewRequestDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if *got != *want {
		t.Errorf("got %+v, want %+v", *got, *want)
	}
}

// A request with a version this build does not understand must still decode.
// The daemon has to see it to register a failed job explaining the refusal;
// dropping it at the decoder would look identical to a daemon that never read.
func TestRequestDecoderDoesNotRejectAnUnknownVersion(t *testing.T) {
	r := strings.NewReader(`{"version":99,"type":"dispatch","id":"job-1","input":"sc-1"}` + "\n")
	got, err := NewRequestDecoder(r).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Version != 99 {
		t.Errorf("got version %d, want 99", got.Version)
	}
}

func TestRequestDecoderReturnsEOFWhenExhausted(t *testing.T) {
	d := NewRequestDecoder(strings.NewReader(""))
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestSnapshotCarriesJobs(t *testing.T) {
	var buf bytes.Buffer
	snap := &Snapshot{
		Version:   Version,
		Timestamp: 42,
		Jobs: []Job{{
			ID:     "job-1",
			Input:  "sc-12345",
			State:  JobRunning,
			Status: "classifying story for model routing",
		}},
	}
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(got.Jobs))
	}
	if got.Jobs[0].State != JobRunning || got.Jobs[0].Status != "classifying story for model routing" {
		t.Errorf("got %+v", got.Jobs[0])
	}
}

// The no-version-bump decision rests on this: a snapshot written by a daemon
// that predates jobs must decode, with Jobs nil rather than an error.
func TestAJobslessSnapshotStillDecodes(t *testing.T) {
	line := `{"version":1,"timestamp":42,"sessions":[]}` + "\n"
	got, err := NewDecoder(strings.NewReader(line)).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Jobs != nil {
		t.Errorf("got Jobs %v, want nil", got.Jobs)
	}
}

// And the other direction: jobs must not appear in the wire format at all when
// there are none, so an old client sees a byte-identical frame.
func TestNoJobsMeansNoJobsKey(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, &Snapshot{Version: Version, Timestamp: 42}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(buf.String(), "jobs") {
		t.Errorf("frame mentions jobs: %s", buf.String())
	}
}
```

Add `"bytes"`, `"errors"`, `"io"`, `"strings"` to the test file's imports if absent.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/protocol/ -run 'Request|Jobs|Jobless' -v`
Expected: FAIL — `undefined: Request`, `undefined: EncodeRequest`, `undefined: RequestDispatch`, `undefined: JobRunning`, and `snap.Jobs` undefined.

- [ ] **Step 3: Implement**

In `internal/protocol/protocol.go`, add below `maxLine`:

```go
// maxRequestLine bounds one client request. Requests are tiny; this is far
// above any legitimate one and exists so a client cannot make the daemon
// allocate without limit.
const maxRequestLine = 64 << 10

const RequestDispatch = "dispatch"

const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)
```

Add `Jobs` to `Snapshot`:

```go
type Snapshot struct {
	Version   int                `json:"version"`
	Timestamp int64              `json:"timestamp"`
	Sessions  []*session.Session `json:"sessions"`
	// Jobs is additive on purpose: it is what lets this stay protocol
	// version 1. A client that predates it ignores the key, and a client
	// that expects it sees nil against a daemon that does not send it.
	Jobs []Job `json:"jobs,omitempty"`
}
```

Then append:

```go
// Request is a client-to-daemon frame. Only clients write these and only the
// daemon reads them, which is why no envelope is needed to tell them apart
// from a Snapshot.
type Request struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Input   string `json:"input"`
	Cwd     string `json:"cwd"`
}

// Job is one dispatch, as the daemon sees it. Status is the last line the job
// printed, or the reason it failed.
type Job struct {
	ID      string `json:"id"`
	Input   string `json:"input"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Started int64  `json:"started"`
	Ended   int64  `json:"ended"`
}

func EncodeRequest(w io.Writer, req *Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

type RequestDecoder struct {
	scanner *bufio.Scanner
}

func NewRequestDecoder(r io.Reader) *RequestDecoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4*1024), maxRequestLine)
	return &RequestDecoder{scanner: s}
}

// Next deliberately does not check Version. An unrecognized version has to
// reach the daemon so it can answer with a failed job naming the reason;
// refusing here would be indistinguishable from a daemon that never read.
func (d *RequestDecoder) Next() (*Request, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var req Request
	if err := json.Unmarshal(d.scanner.Bytes(), &req); err != nil {
		return nil, err
	}
	return &req, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/protocol/ -v`
Expected: PASS, all tests.

- [ ] **Step 5: Verify the compatibility tests are not vacuous**

Temporarily change the `Jobs` tag from `json:"jobs,omitempty"` to `json:"jobs"`.
Run: `go test ./internal/protocol/ -run TestNoJobsMeansNoJobsKey -v`
Expected: FAIL — the frame contains `"jobs":null`.
Revert the tag and re-run to confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/protocol/
git commit -m "feat(protocol): add Request frames and Snapshot.Jobs

Clients write Request, the daemon writes Snapshot, so direction
disambiguates the two frame types and no envelope is needed. Jobs is
additive, which is what keeps this protocol version 1: an old client
ignores the key and a new client sees nil without it.

RequestDecoder does not reject an unknown version. The daemon has to see
such a request to answer with a failed job naming the refusal, and a drop
at the decoder would look exactly like a daemon that never read."
```

---

### Task 2: fetch.StreamCommander

**Files:**
- Modify: `internal/fetch/cmd.go`
- Test: `internal/fetch/cmd_test.go` (create if absent)

**Interfaces:**
- Consumes: nothing.
- Produces: `fetch.StreamCommander` interface with `RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error`. `*fetch.ExecCommander` and `*fetch.MockCommander` both implement it. `MockCommander.RunStream` splits the configured output on newlines and delivers one `onLine` call per line, recording the call in the same log `Run` uses.

- [ ] **Step 1: Write the failing tests**

Create or append to `internal/fetch/cmd_test.go`:

```go
func TestRunStreamDeliversOneCallPerLine(t *testing.T) {
	var got []string
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "printf 'one\\ntwo\\nthree\\n'"},
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	want := []string{"one", "two", "three"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A script that dies mid-sentence still said something, and the status line
// should show it.
func TestRunStreamDeliversAFinalUnterminatedLine(t *testing.T) {
	var got []string
	c := &ExecCommander{}
	_ = c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "printf 'no newline'"},
		func(line string) { got = append(got, line) })
	if len(got) != 1 || got[0] != "no newline" {
		t.Errorf("got %q, want [\"no newline\"]", got)
	}
}

func TestRunStreamReturnsTheExitError(t *testing.T) {
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "exit 3"}, func(string) {})
	if err == nil {
		t.Fatal("got nil, want an exit error")
	}
}

func TestRunStreamPassesEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	var got []string
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), dir, []string{"VIGIL_CLIENT=/dev/ttys009"},
		"sh", []string{"-c", "printf '%s\\n' \"${VIGIL_CLIENT}\"; pwd"},
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if len(got) != 2 || got[0] != "/dev/ttys009" {
		t.Fatalf("got %q, want the client first", got)
	}
	// macOS resolves /var to /private/var, so compare the resolved paths.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(got[1])
	if gotDir != wantDir {
		t.Errorf("got dir %q, want %q", gotDir, wantDir)
	}
}

func TestRunStreamKilledByContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := &ExecCommander{}
	err := c.RunStream(ctx, "", nil, "sh", []string{"-c", "sleep 5"}, func(string) {})
	if err == nil {
		t.Fatal("got nil, want a kill error")
	}
}

func TestMockCommanderStreamsConfiguredOutput(t *testing.T) {
	m := NewMockCommander()
	m.On("dispatch", ">>> one\n>>> two", nil)
	var got []string
	if err := m.RunStream(context.Background(), "", nil, "dispatch", nil,
		func(line string) { got = append(got, line) }); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if len(got) != 2 || got[0] != ">>> one" || got[1] != ">>> two" {
		t.Errorf("got %q", got)
	}
	if len(m.Calls) != 1 || m.Calls[0].Name != "dispatch" {
		t.Errorf("RunStream did not record its call: %+v", m.Calls)
	}
}
```

Add `"context"`, `"path/filepath"`, `"reflect"`, `"testing"`, `"time"` to the imports.

Before writing, read `internal/fetch/cmd.go` and confirm the `MockCommander` field names used above (`Calls`, `MockCall.Name`, the `On` method). If they differ, use the real names — the plan's author read them at `cmd.go:44-80` but the code is authoritative.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/fetch/ -run RunStream -v`
Expected: FAIL — `c.RunStream undefined`.

- [ ] **Step 3: Implement**

In `internal/fetch/cmd.go`, beside the `Commander` interface:

```go
// StreamCommander runs a subprocess and delivers its output a line at a time.
// A separate interface rather than a method on Commander: every existing fake
// implements Commander, and only the daemon's job runner needs streaming.
//
// onLine is called from a goroutine that is not the caller's. A caller that
// writes shared state from it must synchronize.
type StreamCommander interface {
	RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error
}
```

Then the real implementation:

```go
func (c *ExecCommander) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	pr, pw := io.Pipe()
	// One pipe for both streams: hooks run under `sh -c 'exec 2>&1; ...'`
	// so they are already merged, and anything else that writes to stderr
	// is output the caller wants to see rather than lose.
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return err
	}

	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		s := bufio.NewScanner(pr)
		s.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for s.Scan() {
			onLine(s.Text())
		}
	}()

	waitErr := cmd.Wait()
	// Closing the write end is what ends the scanner; without it the
	// goroutine blocks forever and this function never returns.
	_ = pw.Close()
	<-scanned
	_ = pr.Close()
	return waitErr
}
```

Add `"bufio"` and `"io"` to the imports if absent.

Then the fake, beside `MockCommander.Run`:

```go
func (m *MockCommander) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	out, err := m.Run(ctx, dir, name, args...)
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			onLine(line)
		}
	}
	return err
}
```

Add `"strings"` to the imports if absent.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/fetch/ -race -run 'RunStream|MockCommanderStreams' -v`
Expected: PASS, six tests.

- [ ] **Step 5: Verify the pipe-close is load-bearing**

Delete the `_ = pw.Close()` line after `cmd.Wait()`.
Run: `go test ./internal/fetch/ -run TestRunStreamDeliversOneCallPerLine -timeout 20s`
Expected: FAIL with a timeout / panic, because `<-scanned` never fires.
Restore the line and confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/fetch/
git commit -m "feat(fetch): add StreamCommander for line-by-line output

A segregated interface rather than a method on Commander, which every
existing fake implements. Both streams share one pipe: hooks already merge
them via 'exec 2>&1', and anything else on stderr is output worth showing
rather than dropping.

onLine runs on a goroutine that is not the caller's. Documented on the
interface, because the daemon's job runner writes shared state from it."
```

---

### Task 3: config.RunHookStream and dispatch_timeout

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `fetch.StreamCommander` (Task 2).
- Produces: `(*Config).RunHookStream(ctx context.Context, sc fetch.StreamCommander, name string, vars map[string]string, cwd string, env []string, timeout time.Duration, onLine func(string)) error`. Unexported `(*Config).hookArgv(name string, vars map[string]string) ([]string, error)` shared with `RunHook`. New setting `dispatch_timeout`, env `VIGIL_DISPATCH_TIMEOUT`, default `"300"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestRunHookStreamExpandsAndStreams(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": "printf '>>> fetching %s\\n>>> done\\n' {input}",
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		map[string]string{"input": "sc-12345"}, "", nil, 5*time.Second,
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	want := []string{">>> fetching sc-12345", ">>> done"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// stderr is load-bearing: warn and error in lib/output.sh write there, and a
// failure reason arriving on stderr must reach the status line.
func TestRunHookStreamMergesStderr(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": "printf '!!! broke\\n' >&2",
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, 5*time.Second, func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	if len(got) != 1 || got[0] != "!!! broke" {
		t.Errorf("got %q, want [\"!!! broke\"]", got)
	}
}

func TestRunHookStreamPassesEnv(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": `printf '%s\n' "${VIGIL_CLIENT}"`,
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", []string{"VIGIL_CLIENT=/dev/ttys009"}, 5*time.Second,
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	if len(got) != 1 || got[0] != "/dev/ttys009" {
		t.Errorf("got %q, want the client", got)
	}
}

func TestRunHookStreamUnconfigured(t *testing.T) {
	cfg := &Config{}
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, time.Second, func(string) {})
	if !errors.As(err, new(*HookNotConfigured)) {
		t.Errorf("got %v, want HookNotConfigured", err)
	}
}

func TestRunHookStreamHonoursTheTimeout(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"dispatch": "sleep 5"}}
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, 50*time.Millisecond, func(string) {})
	if err == nil {
		t.Fatal("got nil, want a timeout error")
	}
}

func TestDispatchTimeoutDefaultsTo300s(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &Config{}
	if got := cfg.GetSettingDuration("dispatch_timeout"); got != 300*time.Second {
		t.Errorf("got %v, want 5m0s", got)
	}
	if !IsSetting("dispatch_timeout") {
		t.Error("dispatch_timeout is not a known setting")
	}
}

// RunHook and RunHookStream share hookArgv. This pins that the sharing did not
// change RunHook's contract: its output is trimmed and stderr is merged.
func TestRunHookStillTrimsAndMergesAfterTheRefactor(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"notify": "printf 'out\\n'; printf 'err\\n' >&2",
	}}
	out, err := cfg.RunHook(context.Background(), &fetch.ExecCommander{}, "notify",
		nil, "", 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "out\nerr" {
		t.Errorf("got %q, want \"out\\nerr\"", out)
	}
}
```

Add `"errors"`, `"reflect"`, `"time"`, `"context"` and the `fetch` import as needed.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'RunHookStream|DispatchTimeout|StillTrims' -v`
Expected: FAIL — `cfg.RunHookStream undefined`, and `TestDispatchTimeoutDefaultsTo300s` fails with `got 0s, want 5m0s`.

- [ ] **Step 3: Add the setting**

In `settingDefaults`, after `"panel_auto"`:

```go
	"dispatch_timeout":      {"VIGIL_DISPATCH_TIMEOUT", "300"},
```

- [ ] **Step 4: Extract hookArgv and add RunHookStream**

Replace the body of `RunHook` down to the `cmd.Run` call so both paths share one construction. Add above `RunHook`:

```go
// hookArgv builds the argv both hook runners use. Shared so the two cannot
// drift on quoting or on the stderr merge, which MergePR depends on: it
// searches hook output for "merged", and gh writes that to stderr. `exec 2>&1;`
// redirects the whole script regardless of its structure, unlike appending
// " 2>&1" to the command, which would only redirect its last clause.
func (c *Config) hookArgv(name string, vars map[string]string) ([]string, error) {
	template := c.GetHook(name)
	if template == "" {
		return nil, &HookNotConfigured{Name: name}
	}
	cmdStr, err := ExpandHook(template, vars)
	if err != nil {
		return nil, err
	}
	return []string{"sh", "-c", "exec 2>&1; " + cmdStr}, nil
}
```

`RunHook` becomes:

```go
func (c *Config) RunHook(ctx context.Context, cmd fetch.Commander, name string, vars map[string]string, cwd string, timeout time.Duration) (string, error) {
	argv, err := c.hookArgv(name, vars)
	if err != nil {
		return "", err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, err := cmd.Run(ctx, cwd, argv[0], argv[1:]...)
	if err != nil {
		return strings.TrimSpace(out), fmt.Errorf("hook %s failed: %w (output: %s)", name, err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}
```

Then append:

```go
// RunHookStream runs a hook and delivers its output a line at a time. Used by
// the daemon's job runner, where a dispatch takes long enough that its last
// line is the only progress a user gets.
//
// onLine is called from RunStream's scanner goroutine, not this one.
func (c *Config) RunHookStream(
	ctx context.Context,
	sc fetch.StreamCommander,
	name string,
	vars map[string]string,
	cwd string,
	env []string,
	timeout time.Duration,
	onLine func(string),
) error {
	argv, err := c.hookArgv(name, vars)
	if err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return sc.RunStream(ctx, cwd, env, argv[0], argv[1:], onLine)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/ -race -v`
Expected: PASS, including every pre-existing test — `RunHook`'s contract must be unchanged.

- [ ] **Step 6: Verify the shared-argv test is not vacuous**

In `hookArgv`, change `"exec 2>&1; "` to `""`.
Run: `go test ./internal/config/ -run 'StillTrimsAndMerges|RunHookStreamMergesStderr' -v`
Expected: both FAIL — the stderr line is missing from each.
Restore and confirm PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add RunHookStream and dispatch_timeout

RunHook and RunHookStream now share hookArgv, so they cannot drift on
quoting or on the 'exec 2>&1' stderr merge. That merge is load-bearing
twice over: MergePR searches hook output for gh's stderr, and a dispatch's
failure reason arrives on stderr via lib/output.sh's error/warn.

dispatch_timeout defaults to 300s, replacing the hardcoded 15s that could
not cover a real dispatch."
```

---

### Task 4: The daemon's job runner

**Files:**
- Create: `internal/daemon/jobs.go`
- Create: `internal/daemon/jobs_test.go`
- Modify: `internal/fetch/tmux.go` (add `MostRecentClient`)
- Test: `internal/fetch/tmux_test.go`

**Interfaces:**
- Consumes: `protocol.Request`, `protocol.Job` and the state constants (Task 1); `fetch.StreamCommander` (Task 2); `(*config.Config).RunHookStream` (Task 3).
- Produces: `fetch.MostRecentClient(ctx context.Context, cmd fetch.Commander) string`. Unexported in `internal/daemon`: `newJobs(cfg *config.Config, stream fetch.StreamCommander, cmd fetch.Commander, logf func(string, ...any)) *jobs`; `(*jobs).submit(req *protocol.Request)`; `(*jobs).snapshot() []protocol.Job`; `(*jobs).work(ctx context.Context)`; constants `succeededRetention = 10 * time.Second`, `failedRetention = 10 * time.Minute`.

- [ ] **Step 1: Write the failing test for MostRecentClient**

Append to `internal/fetch/tmux_test.go`:

```go
func TestMostRecentClientPicksTheHighestActivity(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "1200|/dev/ttys002\n1900|/dev/ttys009\n1500|/dev/ttys004\n", nil)
	if got := MostRecentClient(context.Background(), m); got != "/dev/ttys009" {
		t.Errorf("got %q, want /dev/ttys009", got)
	}
}

func TestMostRecentClientIsEmptyWithNoClients(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "", nil)
	if got := MostRecentClient(context.Background(), m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMostRecentClientIsEmptyWhenTmuxFails(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "", errors.New("no server running"))
	if got := MostRecentClient(context.Background(), m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// Pipe-separated, not whitespace-separated. The phase 3 handoff records a
// verification run misled by awk splitting on a session name's spaces; a client
// name is a tty today but the format string must not be the thing that breaks
// if that ever stops being true.
func TestMostRecentClientUsesPipeSeparatedFormat(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "1|/dev/ttys002\n", nil)
	MostRecentClient(context.Background(), m)
	if len(m.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(m.Calls))
	}
	joined := strings.Join(m.Calls[0].Args, " ")
	if !strings.Contains(joined, "|") {
		t.Errorf("format has no pipe separator: %q", joined)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/fetch/ -run MostRecentClient -v`
Expected: FAIL — `undefined: MostRecentClient`.

- [ ] **Step 3: Implement MostRecentClient**

In `internal/fetch/tmux.go`, beside `MostRecentSession`:

```go
// MostRecentClient returns the name of the tmux client that was active most
// recently, or "" if there is none or tmux cannot be reached. The daemon has no
// tty of its own, so this is how a job learns which client to size a window
// against and switch at the end.
func MostRecentClient(ctx context.Context, cmd Commander) string {
	out, err := cmd.Run(ctx, "", "tmux", "list-clients", "-F", "#{client_activity}|#{client_name}")
	if err != nil {
		return ""
	}
	best := ""
	bestActivity := int64(-1)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		activity, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		if activity > bestActivity {
			bestActivity = activity
			best = parts[1]
		}
	}
	return best
}
```

Add `"strconv"` to the imports if absent.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/fetch/ -run MostRecentClient -v`
Expected: PASS, four tests.

- [ ] **Step 5: Write the failing job-runner tests**

Create `internal/daemon/jobs_test.go`:

```go
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
	if running.Status != ">>> fetching story" {
		t.Errorf("got status %q, want the streamed line", running.Status)
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
```

Note the two `recordingStream` reads happen after the job reached a terminal state, so they are ordered by the state transition; `-race` will say so if that reasoning is wrong.

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./internal/daemon/ -run 'Job|Duplicate|Unknown|Status|Retention|Client' -v`
Expected: FAIL — `undefined: newJobs`, `undefined: statusLine`, `undefined: succeededRetention`.

- [ ] **Step 7: Implement the job runner**

Create `internal/daemon/jobs.go`:

```go
package daemon

import (
	"context"
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
// mu guards byID and order. A running job's goroutine writes Status through
// setStatus, and poll reads the whole table through snapshot, so nothing hands
// out a *Job that a goroutine is still writing.
type jobs struct {
	mu    sync.Mutex
	byID  map[string]*protocol.Job
	order []string

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
		pending: make(chan string, queueDepth),
		cfg:     cfg,
		stream:  stream,
		cmd:     cmd,
		logf:    logf,
		now:     time.Now,
	}
}

// submit registers a request. A refusal is registered too, as a failed job
// naming the reason: the submitting client waits for its id to appear in a
// snapshot, so a silent drop would be indistinguishable from a daemon that
// never read the frame.
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
		job.State = protocol.JobFailed
		job.Status = reason
		job.Ended = now
	}
	j.byID[req.ID] = job
	j.order = append(j.order, req.ID)
	j.mu.Unlock()

	if reason != "" {
		j.logf("dispatch %s refused: %s", req.ID, reason)
		return
	}

	select {
	case j.pending <- req.ID:
	default:
		j.fail(req.ID, "dispatch queue is full")
	}
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
	input, cwd := job.Input, jobCwd(job)
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
// that explained itself has already said something better than "exit 1".
func (j *jobs) failFromOutput(id string, err error) {
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
	case protocol.JobFailed:
		return now.Sub(ended) > failedRetention
	}
	return false
}

func jobCwd(job *protocol.Job) string { return job.cwd }
```

The last line will not compile: `protocol.Job` has no `cwd`. The cwd is a property of the request, not of the published job — it must not appear on the wire. Store it beside the job instead. Replace `jobCwd` and adjust:

- add `cwds map[string]string` to `jobs`, created in `newJobs`
- in `submit`, under the lock, `j.cwds[req.ID] = req.Cwd`
- in `run`, read `cwd := j.cwds[id]` under the same lock that reads the job
- in `snapshot`, `delete(j.cwds, id)` next to `delete(j.byID, id)`
- delete the `jobCwd` function

- [ ] **Step 8: Run to verify they pass**

Run: `go test ./internal/daemon/ -race -run 'Job|Duplicate|Unknown|Status|Retention|Client' -v`
Expected: PASS, eleven tests.

- [ ] **Step 9: Verify serialization and dedup are not vacuous**

Mutation A — remove serialization: in `work`, replace `j.run(ctx, id)` with `go j.run(ctx, id)`.
Run: `go test ./internal/daemon/ -race -run TestJobsRunOneAtATime -v`
Expected: FAIL — either `maxAtOnce` is 2 or the second job is `running` rather than `queued`.

Mutation B — remove dedup: delete the `j.inFlightInputLocked` branch in `submit`.
Run: `go test ./internal/daemon/ -race -run TestADuplicateInputIsRefusedAsAFailedJob -v`
Expected: FAIL — the job reaches `queued`/`running` rather than `failed`.

Revert both and confirm the suite passes.

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/jobs.go internal/daemon/jobs_test.go internal/fetch/tmux.go internal/fetch/tmux_test.go
git commit -m "feat(daemon): add the serialized dispatch job runner

One job at a time: two concurrent git worktree adds in one repository
contend on the index lock, and dispatches arrive one click at a time.

Refusals - a duplicate input, an unknown request version or type - are
registered as failed jobs rather than dropped, because a submitting client
waits for its id to appear in a snapshot and a silent drop is
indistinguishable from a daemon that never read the frame.

The tmux client is resolved per job rather than per submission: a dispatch
takes long enough that the client the user is sitting at can change. It
reaches the scripts as VIGIL_CLIENT, and fetch.MostRecentClient reads a
pipe-separated format rather than a whitespace-separated one."
```

---

### Task 5: The daemon reads Request frames

**Files:**
- Modify: `internal/daemon/client.go`
- Modify: `internal/daemon/daemon.go`
- Test: `internal/daemon/client_test.go`, `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `protocol.NewRequestDecoder` (Task 1); `(*jobs).submit` (Task 4).
- Produces: `(*client).readLoop(ctx context.Context, requests chan<- *protocol.Request)`; `Server.requests chan *protocol.Request`, drained in `Run`'s select and handed to `Server.jobs.submit`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/client_test.go`:

```go
// The writer stays the sole closer of the connection. A reader that hits EOF
// must not close it out from under a writer mid-Encode.
func TestAReaderAtEOFDoesNotKillItsWriter(t *testing.T) {
	server, peer := net.Pipe()
	c := newClient(server)

	logged := make(chan string, 4)
	go c.writeLoop(func(format string, args ...any) {
		logged <- fmt.Sprintf(format, args...)
	})

	requests := make(chan *protocol.Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		c.readLoop(ctx, requests)
	}()

	// A request, then a half-close of the peer's write side, which is what
	// gives the reader an EOF while the writer is still healthy.
	if err := protocol.EncodeRequest(peer, &protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch, ID: "a", Input: "sc-1",
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	select {
	case got := <-requests:
		if got.ID != "a" {
			t.Errorf("got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reader never delivered the request")
	}

	// net.Pipe has no half-close, so drive the reader to EOF by closing the
	// peer, then assert the writer reports the failure itself rather than
	// panicking on a connection someone else closed.
	_ = peer.Close()
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the reader did not exit on EOF")
	}
	c.queue(&protocol.Snapshot{Version: protocol.Version})
	c.stop()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer did not exit")
	}
}
```

Append to `internal/daemon/daemon_test.go`:

```go
func TestASubmittedRequestBecomesAJobInTheNextSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stream := newBlockingStream()
	srv := &Server{
		Collector:  collect.New(testConfig(), fetch.NewMockCommander()),
		Interval:   10 * time.Millisecond,
		SocketPath: filepath.Join(dir, "vigild.sock"),
		Log:        log.New(io.Discard, "", 0),
	}
	srv.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), srv.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	conn := dialWhenReady(t, srv.SocketPath)
	defer func() { _ = conn.Close() }()

	if err := protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch,
		ID: "job-1", Input: "sc-12345",
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	dec := protocol.NewDecoder(conn)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		snap, err := dec.Next()
		if err != nil {
			continue
		}
		if findJob(snap.Jobs, "job-1") != nil {
			cancel()
			<-runDone
			return
		}
	}
	cancel()
	<-runDone
	t.Fatal("the job never appeared in a snapshot")
}
```

If `testConfig` and `dialWhenReady` helpers do not already exist in `daemon_test.go`, read the file and reuse whatever it already uses to build a `Server` and wait for its socket. Do not add a second way to do the same thing.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/daemon/ -run 'ReaderAtEOF|SubmittedRequest' -v`
Expected: FAIL — `c.readLoop undefined`, `srv.jobs undefined`.

- [ ] **Step 3: Add the reader**

In `internal/daemon/client.go`, extend the doc comment on `client` and add the reader:

```go
// readLoop consumes Request frames from this client until the connection ends.
// It never closes the connection: writeLoop is the sole closer, so a reader at
// EOF cannot pull the socket out from under a writer mid-Encode.
func (c *client) readLoop(ctx context.Context, requests chan<- *protocol.Request) {
	dec := protocol.NewRequestDecoder(c.conn)
	for {
		req, err := dec.Next()
		if err != nil {
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
```

- [ ] **Step 4: Wire it into the server**

In `internal/daemon/daemon.go`, add to `Server`:

```go
	// jobs is the dispatch queue. Nil disables submission, which is what a
	// Server literal in a test gets unless it builds one.
	jobs *jobs

	// requests carries client submissions to Run's goroutine, which owns the
	// job table's only writer besides a running job.
	requests chan *protocol.Request
```

In `New`, after `inFlightEffects`:

```go
		requests: make(chan *protocol.Request, queueDepth),
```

and build the runner, which needs a `StreamCommander`. `New` takes a `fetch.Commander`; the concrete `*fetch.ExecCommander` satisfies both, so accept the narrowing here rather than changing `New`'s signature:

```go
	srv := &Server{ /* existing fields */ }
	if stream, ok := cmd.(fetch.StreamCommander); ok {
		srv.jobs = newJobs(cfg, stream, cmd, logger.Printf)
	}
	return srv
```

In `Run`, start the worker and drain requests. After the `accept` goroutine:

```go
	if s.jobs != nil {
		s.pendingEffects.Add(1)
		go func() {
			defer s.pendingEffects.Done()
			s.jobs.work(ctx)
		}()
	}
```

and add a case to the select, before `case <-ticker.C`:

```go
		case req := <-s.requests:
			if s.jobs != nil {
				s.jobs.submit(req)
			}
```

Guard `s.requests` being nil for a bare `Server` literal: a receive on a nil channel blocks forever, which in a select is exactly the right behavior, so no guard is needed. Confirm that reasoning holds by reading the select.

In `addClient`, start the reader beside the writer:

```go
	if s.requests != nil {
		go c.readLoop(ctx, s.requests)
	}
```

`addClient` does not currently take a `ctx`. Thread it: change the signature to `addClient(ctx context.Context, conn net.Conn)` and pass `ctx` from `Run`'s select.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/daemon/ -race -v`
Expected: PASS, including every pre-existing daemon test.

- [ ] **Step 6: Verify the sole-closer rule is load-bearing**

In `readLoop`, add `defer func() { _ = c.conn.Close() }()` at the top.
Run: `go test ./internal/daemon/ -race -count=5`
Expected: FAIL or a race report on the write path, because two goroutines now close and use the same connection.
Remove it and confirm PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): read Request frames from clients

Each connection gains a reader goroutine alongside its writer. The writer
stays the sole closer of the connection: a reader at EOF that closed it
would race a writer mid-Encode, which is the whole reason -race is not
optional in this package.

Requests reach Run's goroutine through a channel, so submission happens
where the job table's other writer already lives."
```

---

### Task 6: Jobs in the snapshot, off the poll goroutine

**Files:**
- Modify: `internal/daemon/daemon.go`
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `(*jobs).snapshot()` (Task 4).
- Produces: `protocol.Snapshot.Jobs` populated on every broadcast.

- [ ] **Step 1: Write the failing test**

The load-bearing one: polling must not stall while a job runs. Append to `internal/daemon/daemon_test.go`:

```go
// A job runs on its own goroutine. poll is synchronous per tick, so a job
// executed there would freeze every panel's snapshot stream for the length of
// a dispatch - 60s or more for a real one.
func TestPollingContinuesWhileAJobIsRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stream := newBlockingStream()
	srv := &Server{
		Collector:  collect.New(testConfig(), fetch.NewMockCommander()),
		Interval:   10 * time.Millisecond,
		SocketPath: filepath.Join(dir, "vigild.sock"),
		Log:        log.New(io.Discard, "", 0),
		requests:   make(chan *protocol.Request, queueDepth),
	}
	srv.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), srv.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	conn := dialWhenReady(t, srv.SocketPath)
	defer func() { _ = conn.Close() }()

	if err := protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch,
		ID: "job-1", Input: "sc-1",
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	select {
	case <-stream.started:
	case <-time.After(3 * time.Second):
		cancel()
		<-runDone
		t.Fatal("the job never started")
	}

	// The job is now blocked inside RunStream. Snapshots must keep arriving,
	// and must show it running.
	dec := protocol.NewDecoder(conn)
	sawRunning := false
	for i := 0; i < 3; i++ {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		snap, err := dec.Next()
		if err != nil {
			cancel()
			<-runDone
			t.Fatalf("snapshot %d never arrived while a job was running: %v", i, err)
		}
		if job := findJob(snap.Jobs, "job-1"); job != nil && job.State == protocol.JobRunning {
			sawRunning = true
		}
	}
	if !sawRunning {
		t.Error("no snapshot showed the job running")
	}

	close(stream.release)
	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run: %v", err)
	}
}

// Run must not return while a job goroutine is still unwinding, the same way
// it already waits on pendingEffects.
//
// Note what this does NOT assert. Cancelling the daemon's context kills the
// job: RunStream uses exec.CommandContext with that same context, and the spec
// is explicit that "the job dies with the daemon". So this uses a stream that
// ignores cancellation, because the invariant under test is goroutine hygiene -
// Run does not return while a goroutine that writes the job table is still
// live - not job survival, which is not a property this design claims.
func TestRunWaitsForAJobGoroutineToUnwind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	stream := newBlockingStream()
	stream.ignoreContext = true
	srv := &Server{
		Collector:  collect.New(testConfig(), fetch.NewMockCommander()),
		Interval:   10 * time.Millisecond,
		SocketPath: filepath.Join(dir, "vigild.sock"),
		Log:        log.New(io.Discard, "", 0),
		requests:   make(chan *protocol.Request, queueDepth),
	}
	srv.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), srv.logf)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	conn := dialWhenReady(t, srv.SocketPath)
	if err := protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version, Type: protocol.RequestDispatch,
		ID: "job-1", Input: "sc-1",
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	<-stream.started
	_ = conn.Close()

	cancel()
	select {
	case <-runDone:
		t.Fatal("Run returned while a job was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(stream.release)
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run never returned after the job finished")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/daemon/ -race -run 'PollingContinues|RunWaitsForAnInFlight' -v`
Expected: FAIL — `TestPollingContinuesWhileAJobIsRunning` reports no snapshot showed the job running (`Snapshot.Jobs` is never populated).

- [ ] **Step 3: Populate Jobs in poll**

In `poll`, build the snapshot with jobs:

```go
	var jobList []protocol.Job
	if s.jobs != nil {
		jobList = s.jobs.snapshot()
	}
	snap := &protocol.Snapshot{
		Version:   protocol.Version,
		Timestamp: time.Now().Unix(),
		Sessions:  sessions,
		Jobs:      jobList,
	}
```

`s.jobs.snapshot()` already returns a copy taken under the job mutex, which is what makes this safe to marshal while a job goroutine writes `Status`.

The `work` goroutine registered in Task 5 is already tracked by `pendingEffects`, so `Run`'s existing `s.pendingEffects.Wait()` is what `TestRunWaitsForAJobGoroutineToUnwind` exercises. `work` calls `j.run(ctx, id)` synchronously on its own goroutine, so it cannot take its `ctx.Done()` branch while a job is executing — the wait therefore covers an in-flight job's goroutine. Confirm that by reading `work` rather than assuming it.

What the wait does **not** do is keep the job alive: `RunStream` uses `exec.CommandContext` with the same context, so cancelling the daemon kills the child. That is the spec's stated behavior ("the job dies with the daemon"), not a defect, which is why the test uses a stream that ignores cancellation to isolate the goroutine-hygiene question from the job-survival one.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/daemon/ -race -v`
Expected: PASS.

- [ ] **Step 5: Verify the off-the-poll-goroutine claim is not vacuous**

In `work`, replace the `select` body so jobs run inline on the poll goroutine instead — the simplest faithful mutation is to call `j.run(ctx, req.ID)` directly from `Run`'s `case req := <-s.requests:` after `submit`, and stop starting the `work` goroutine.
Run: `go test ./internal/daemon/ -race -run TestPollingContinuesWhileAJobIsRunning -v`
Expected: FAIL — snapshots stop arriving while the job is blocked.
Revert and confirm PASS.

- [ ] **Step 6: Run the whole suite and lint**

Run: `make test && make lint`
Expected: all packages ok, 0 issues.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): publish jobs in the snapshot

Snapshot.Jobs comes from a copy taken under the job mutex, so poll can
marshal it while a running job's goroutine writes Status.

Pinned by a test that blocks a job inside RunStream and asserts snapshots
keep arriving: poll is synchronous per tick, so a job run there would
freeze every panel's stream for the length of a dispatch."
```

---

### Task 7: `vigil dispatch`

**Files:**
- Create: `internal/daemon/spawn.go` (moved from `internal/model/client.go`)
- Create: `internal/dispatch/submit.go`
- Create: `internal/dispatch/submit_test.go`
- Modify: `internal/model/client.go` (delete `spawnDaemon`, point `daemonSpawner` at `daemon.Spawn`)
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `protocol.EncodeRequest`, `protocol.NewDecoder`, `protocol.Job` (Task 1).
- Produces: `daemon.Spawn() error`; `dispatch.Validate(input string) error`; `dispatch.Options{Input, Cwd, SocketPath string, Spawn func() error, AckTimeout time.Duration}`; `dispatch.Submit(ctx context.Context, opts Options) (*protocol.Job, error)`; `dispatch.ErrNoAck`.

- [ ] **Step 1: Move the spawn into the daemon package**

Create `internal/daemon/spawn.go` with the body of `internal/model/client.go`'s `spawnDaemon`, renamed `Spawn`, and add the environment strip the spec calls for:

```go
package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// Spawn starts `vigil daemon` detached from this process, so it outlives the
// pane that started it. Its output goes to a log file beside the socket: the
// daemon is silent when healthy, and when it is not, that log is the only place
// the reason survives.
func Spawn() error {
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
	// Not the caller's cwd: that is often a git worktree, and
	// git-worktree-done removes those routinely, leaving a long-lived daemon
	// holding a deleted directory.
	cmd.Dir = "/"
	// TMUX and TMUX_PANE identify one pane's client. A daemon that inherited
	// them would carry that identity for its whole life, stale the moment the
	// pane died, and would make an is-in-tmux check in a job's script lie.
	cmd.Env = withoutTmuxEnv(os.Environ())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid detaches it from this pane's process group, so closing the pane
	// or the tmux session does not take the daemon with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func withoutTmuxEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=") {
			continue
		}
		out = append(out, e)
	}
	return out
}
```

Add `"strings"` to the imports.

In `internal/model/client.go`, delete `spawnDaemon` and change:

```go
var daemonSpawner = daemon.Spawn
```

adding the `internal/daemon` import. Verify no import cycle: `internal/daemon` must not import `internal/model`.

Add to `internal/daemon/spawn_test.go`:

```go
func TestWithoutTmuxEnvDropsOnlyTheTmuxVars(t *testing.T) {
	got := withoutTmuxEnv([]string{
		"PATH=/usr/bin", "TMUX=/tmp/tmux-501/default,123,4",
		"TMUX_PANE=%7", "TMUXP=keep", "HOME=/Users/x",
	})
	want := []string{"PATH=/usr/bin", "TMUXP=keep", "HOME=/Users/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

Note `TMUXP=keep`: a prefix match on `TMUX` rather than `TMUX=` would eat it.

- [ ] **Step 2: Run the move**

Run: `go build ./... && go test ./internal/model/ ./internal/daemon/ -race -run 'Spawn|TmuxEnv' -v`
Expected: PASS. The `internal/model` tests that stub `daemonSpawner` keep working because the var still exists.

- [ ] **Step 3: Write the failing submit tests**

Create `internal/dispatch/submit_test.go`:

```go
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

func (d *fakeDaemon) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.received)
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

// A refusal comes back as a failed job, and Submit must report its reason
// rather than the skew message.
func TestSubmitReportsARefusalReason(t *testing.T) {
	path := socketPath(t)
	startFakeDaemon(t, path, false, func(req *protocol.Request) []protocol.Job {
		return []protocol.Job{{
			ID: req.ID, Input: req.Input,
			State: protocol.JobFailed, Status: "duplicate of an in-flight dispatch",
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
```

- [ ] **Step 4: Run to verify they fail**

Run: `go test ./internal/dispatch/ -v`
Expected: FAIL to build — `undefined: Validate`, `undefined: Submit`, `undefined: Options`, `undefined: ErrNoAck`.

- [ ] **Step 5: Implement submit**

Create `internal/dispatch/submit.go`:

```go
// Package dispatch submits a dispatch job to vigild. The job runs in the
// daemon, so this is a submission client and nothing more: exit 0 means
// accepted, not succeeded.
package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// ErrNoAck means no snapshot ever carried the submitted job. The likely cause
// is a daemon that predates request frames and therefore never read it.
var ErrNoAck = errors.New("daemon did not accept the job")

const maxInput = 500

// dialTimeout matches the client dial elsewhere: a local unix socket answers
// in microseconds or not at all.
const dialTimeout = 300 * time.Millisecond

// spawnSettle bounds how long Submit waits for a freshly spawned daemon to
// bind its socket before giving up.
const spawnSettle = 3 * time.Second

type Options struct {
	Input      string
	Cwd        string
	SocketPath string
	// Spawn starts a daemon when none answers. Nil means do not try.
	Spawn      func() error
	AckTimeout time.Duration
}

// Validate rejects input before it reaches the daemon, so a malformed
// submission never becomes a job at all.
func Validate(input string) error {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return errors.New("dispatch input must not be empty")
	}
	if len(trimmed) > maxInput {
		return fmt.Errorf("dispatch input too long (%d characters, limit %d)", len(trimmed), maxInput)
	}
	for _, c := range trimmed {
		if c < ' ' && c != '\t' {
			return errors.New("dispatch input contains control characters")
		}
	}
	return nil
}

func Submit(ctx context.Context, opts Options) (*protocol.Job, error) {
	if err := Validate(opts.Input); err != nil {
		return nil, err
	}
	input := strings.TrimSpace(opts.Input)

	id, err := newID()
	if err != nil {
		return nil, err
	}

	conn, err := connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	req := &protocol.Request{
		Version: protocol.Version,
		Type:    protocol.RequestDispatch,
		ID:      id,
		Input:   input,
		Cwd:     opts.Cwd,
	}
	if err := protocol.EncodeRequest(conn, req); err != nil {
		return nil, fmt.Errorf("submitting the job: %w", err)
	}

	return awaitAck(conn, id, opts.AckTimeout)
}

// connect dials, and on failure spawns a daemon and retries until it binds.
func connect(ctx context.Context, opts Options) (net.Conn, error) {
	if conn, err := net.DialTimeout("unix", opts.SocketPath, dialTimeout); err == nil {
		return conn, nil
	}
	if opts.Spawn == nil {
		return nil, fmt.Errorf("no daemon listening on %s", opts.SocketPath)
	}
	if err := opts.Spawn(); err != nil {
		return nil, fmt.Errorf("starting a daemon: %w", err)
	}
	deadline := time.Now().Add(spawnSettle)
	for {
		if conn, err := net.DialTimeout("unix", opts.SocketPath, dialTimeout); err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("started a daemon but nothing is listening on %s", opts.SocketPath)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// awaitAck reads snapshots until the submitted job appears. The snapshot is
// the ack: there is no response frame, which is what makes a refusal visible
// in every panel rather than only here.
func awaitAck(conn net.Conn, id string, timeout time.Duration) (*protocol.Job, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	dec := protocol.NewDecoder(conn)
	for {
		snap, err := dec.Next()
		if err != nil {
			return nil, fmt.Errorf("%w; it may be running an older vigil", ErrNoAck)
		}
		for i := range snap.Jobs {
			job := snap.Jobs[i]
			if job.ID != id {
				continue
			}
			if job.State == protocol.JobFailed {
				return &job, fmt.Errorf("dispatch refused: %s", job.Status)
			}
			return &job, nil
		}
	}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a job id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
```

Note `Validate` trims before measuring length, so a 500-character input padded with spaces is accepted rather than refused on its padding.

- [ ] **Step 6: Run to verify they pass**

Run: `go test ./internal/dispatch/ -race -v`
Expected: PASS, nine tests.

- [ ] **Step 7: Wire the subcommand into main.go**

In `parseArgs`, add before `default`:

```go
	case "dispatch":
		return "dispatch", args[1:], nil
```

The `dispatch` case goes in the **later** switch — the one that assigns `err`, after the `tmux`/`git`/`gh` `LookPath` check and after `config.Load`. That placement is the point: unlike `config get`, a dispatch genuinely needs all three binaries, and a missing dependency is worth reporting before a job is queued rather than after.

```go
	switch command {
	case "daemon":
		err = runDaemon(cfg, cmd)
	case "panel":
		err = runPanel(cfg, cmd)
	case "dispatch":
		err = runDispatch(rest, cfg, stdout)
	default:
		err = runTUI(cfg, cmd)
	}
```

and:

```go
// runDispatch submits a job and returns. Exit 0 means the daemon accepted the
// job, not that the job succeeded: the point of the daemon owning it is that it
// outlives this process.
func runDispatch(args []string, cfg *config.Config, stdout io.Writer) error {
	cwd := ""
	var input string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd":
			if i+1 >= len(args) {
				return fmt.Errorf("--cwd needs a path")
			}
			cwd = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown option: %s", args[i])
			}
			input = args[i]
		}
	}
	if input == "" {
		return fmt.Errorf("usage: vigil dispatch [--cwd <path>] <url-or-id>")
	}
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	job, err := dispatch.Submit(context.Background(), dispatch.Options{
		Input:      input,
		Cwd:        cwd,
		SocketPath: protocol.SocketPath(),
		Spawn:      daemon.Spawn,
		AckTimeout: 5 * time.Second,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "dispatch queued: %s\n", job.Input)
	return nil
}
```

Add the `internal/dispatch`, `internal/protocol`, `strings` and `time` imports. Add `vigil dispatch <url-or-id>` to `printUsage`.

- [ ] **Step 8: Add the dispatch-order test**

`main_test.go` already has a table covering all dispatch branches — read it and add `dispatch` to it rather than writing a parallel test. The assertions to add:

```go
func TestDispatchParsesAsItsOwnCommand(t *testing.T) {
	command, rest, err := parseArgs([]string{"dispatch", "--cwd", "/tmp", "sc-1"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if command != "dispatch" {
		t.Errorf("got %q, want dispatch", command)
	}
	if len(rest) != 3 || rest[2] != "sc-1" {
		t.Errorf("got rest %q", rest)
	}
}

// Unlike `config get`, dispatch runs after the dependency check: it needs all
// three binaries, and a queued job is worse than an early error.
func TestDispatchRunsAfterTheDependencyCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"dispatch", "sc-1"}, &stdout, &stderr); code != 1 {
		t.Errorf("got exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found in PATH") {
		t.Errorf("got %q, want a dependency error", stderr.String())
	}
}
```

- [ ] **Step 9: Run to verify they pass**

Run: `go test . -race -run Dispatch -v && make test && make lint`
Expected: PASS, all packages ok, 0 issues.

- [ ] **Step 10: Verify the ack tests are not vacuous**

Mutation: in `awaitAck`, return `(&protocol.Job{ID: id}, nil)` immediately without reading.
Run: `go test ./internal/dispatch/ -run 'SilentDaemon|RefusalReason' -v`
Expected: both FAIL.
Revert and confirm PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/daemon/spawn.go internal/daemon/spawn_test.go internal/dispatch/ internal/model/client.go main.go main_test.go
git commit -m "feat: add vigil dispatch

Submits a job to vigild and returns. Exit 0 means accepted, not succeeded:
the daemon owning the job is the point.

The snapshot is the ack, so there is no response frame and a refusal is
visible in every panel rather than only to the CLI. A daemon that never
reads the frame - one predating this - produces ErrNoAck with a message
naming an older vigil, because 'make install and restart the daemon' is not
guessable from a timeout.

spawnDaemon moves to daemon.Spawn and now strips TMUX and TMUX_PANE: a
daemon that inherited them carried one pane's client identity for its whole
life, stale the moment that pane died."
```

---

### Task 8: The job line, the `d` key, and deleting action.Dispatch

**Files:**
- Create: `internal/view/job.go`
- Create: `internal/view/job_test.go`
- Modify: `internal/model/model.go`, `internal/model/messages.go`, `internal/model/client.go`
- Modify: `internal/fetch/git.go` (add `MainWorktree`)
- Delete: `Dispatch` from `internal/action/action.go` and its tests
- Test: `internal/model/dispatch_test.go` (create), `internal/fetch/git_test.go`

**Interfaces:**
- Consumes: `protocol.Job` (Task 1); `dispatch.Submit`, `dispatch.Validate` (Task 7).
- Produces: `view.RenderJobLine(jobs []protocol.Job, width int) string`; `fetch.MainWorktree(ctx context.Context, cmd fetch.Commander, gitRoot string) string`; `SnapshotMsg.Jobs []protocol.Job`; `Model.jobs []protocol.Job`.

- [ ] **Step 1: Write the failing view test**

Create `internal/view/job_test.go`:

```go
package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func TestRenderJobLineShowsInputAndStatus(t *testing.T) {
	got := RenderJobLine([]protocol.Job{{
		ID: "a", Input: "sc-12345", State: protocol.JobRunning,
		Status: "classifying story for model routing",
	}}, 80)
	if !strings.Contains(got, "sc-12345") {
		t.Errorf("got %q, want the input", got)
	}
	if !strings.Contains(got, "classifying story for model routing") {
		t.Errorf("got %q, want the status", got)
	}
}

func TestRenderJobLineIsEmptyWithNoJobs(t *testing.T) {
	if got := RenderJobLine(nil, 80); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRenderJobLineFitsTheWidth(t *testing.T) {
	got := RenderJobLine([]protocol.Job{{
		ID: "a", Input: "https://app.shortcut.com/workspace/story/12345/a-long-title",
		State: protocol.JobRunning, Status: "creating the worktree and the tmux session",
	}}, 40)
	for _, line := range strings.Split(got, "\n") {
		if w := lipglossWidth(line); w > 40 {
			t.Errorf("line is %d wide, want <= 40: %q", w, line)
		}
	}
}

func TestRenderJobLineShowsAQueuedJobsPosition(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "sc-1", State: protocol.JobRunning, Status: "working"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 80)
	if !strings.Contains(got, "+1") {
		t.Errorf("got %q, want a queued count", got)
	}
}

func TestRenderJobLinePrefersAFailureOverAQueuedJob(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "sc-1", State: protocol.JobFailed, Status: "no branch for story 1"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 80)
	if !strings.Contains(got, "no branch for story 1") {
		t.Errorf("got %q, want the failure reason", got)
	}
}
```

Use whatever width helper the package already has (`lipgloss.Width`, or the existing `TruncateVisible`'s companion) rather than inventing `lipglossWidth` — read `internal/view/layout.go:139` and `internal/view/table.go` first and reuse.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/view/ -run RenderJobLine -v`
Expected: FAIL — `undefined: RenderJobLine`.

- [ ] **Step 3: Implement the job line**

Create `internal/view/job.go`:

```go
package view

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// RenderJobLine renders one line describing dispatch activity, or "" when
// there is none. One line rather than one per job: jobs are serialized, so at
// most one is doing anything, and a panel is ten rows tall.
//
// A failure outranks a running job, which outranks a queued one: a failure is
// the only state the user has to act on.
func RenderJobLine(jobs []protocol.Job, width int) string {
	if len(jobs) == 0 || width <= 0 {
		return ""
	}

	lead := pickJob(jobs)
	if lead == nil {
		return ""
	}

	queued := 0
	for _, j := range jobs {
		if j.State == protocol.JobQueued && j.ID != lead.ID {
			queued++
		}
	}

	marker, colour := "⚡", BrightCyan
	switch lead.State {
	case protocol.JobFailed:
		marker, colour = "✗", BrightRed
	case protocol.JobSucceeded:
		marker, colour = "✓", BrightGreen
	}

	text := fmt.Sprintf("%s %s", marker, lead.Input)
	if lead.Status != "" {
		text += " · " + lead.Status
	}
	if queued > 0 {
		text += fmt.Sprintf(" (+%d)", queued)
	}

	return lipgloss.NewStyle().Foreground(colour).Render(TruncateVisible(text, width))
}

func pickJob(jobs []protocol.Job) *protocol.Job {
	for _, state := range []string{protocol.JobFailed, protocol.JobRunning, protocol.JobSucceeded, protocol.JobQueued} {
		for i := range jobs {
			if jobs[i].State == state {
				return &jobs[i]
			}
		}
	}
	return nil
}
```

Confirm `TruncateVisible` truncates to a visible width including the `(+1)` suffix; if the suffix must survive truncation, truncate `lead.Status` rather than the whole string. The test at width 40 is what tells you.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/view/ -run RenderJobLine -v`
Expected: PASS, five tests.

- [ ] **Step 5: Add MainWorktree**

Append to `internal/fetch/git_test.go`:

```go
func TestMainWorktreeIsTheFirstWorktreeListed(t *testing.T) {
	m := NewMockCommander()
	m.On("git", "worktree /Users/x/portal\nHEAD abc\nbranch refs/heads/main\n\nworktree /Users/x/sc-1\nHEAD def\n", nil)
	if got := MainWorktree(context.Background(), m, "/Users/x/sc-1"); got != "/Users/x/portal" {
		t.Errorf("got %q, want /Users/x/portal", got)
	}
}

func TestMainWorktreeIsEmptyWhenGitFails(t *testing.T) {
	m := NewMockCommander()
	m.On("git", "", errors.New("not a repository"))
	if got := MainWorktree(context.Background(), m, "/tmp"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
```

In `internal/fetch/git.go`:

```go
// MainWorktree returns the main working tree of gitRoot's repository, or "" if
// git cannot answer. A panel's cwd is usually a linked worktree, and a new
// worktree has to be cut from the main one; `worktree list --porcelain` puts
// the main tree first and has done so for far longer than --path-format has
// existed.
func MainWorktree(ctx context.Context, cmd Commander, gitRoot string) string {
	out, err := cmd.Run(ctx, gitRoot, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			return rest
		}
	}
	return ""
}
```

- [ ] **Step 6: Write the failing model tests**

Create `internal/model/dispatch_test.go`:

```go
package model

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func TestASnapshotsJobsReachTheModel(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs: []protocol.Job{{
			ID: "a", Input: "sc-12345", State: protocol.JobRunning, Status: "classifying",
		}},
	})
	got := updated.(Model)
	if len(got.jobs) != 1 || got.jobs[0].ID != "a" {
		t.Fatalf("got jobs %+v", got.jobs)
	}
	if !strings.Contains(got.View(), "sc-12345") {
		t.Error("the view does not show the job")
	}
}

func TestThePanelShowsTheJobLineToo(t *testing.T) {
	m := newTestPanelModel(t)
	m.width, m.height = 40, 10
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs:  []protocol.Job{{ID: "a", Input: "sc-1", State: protocol.JobRunning}},
	})
	if !strings.Contains(updated.(Model).View(), "sc-1") {
		t.Error("the panel does not show the job line")
	}
}

func TestNoJobsMeansNoJobLine(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{Epoch: m.epoch})
	if strings.Contains(updated.(Model).View(), "⚡") {
		t.Error("the view shows a job line with no jobs")
	}
}

func TestTheDispatchKeySubmitsAndValidates(t *testing.T) {
	m := newTestModel(t)
	m.dispatchActive = true
	m.dispatchInput.SetValue("   ")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.dispatchActive {
		t.Error("the input stayed open")
	}
	if cmd == nil {
		t.Fatal("no command was returned")
	}
	msg := cmd()
	result, ok := msg.(ActionResultMsg)
	if !ok {
		t.Fatalf("got %T, want ActionResultMsg", msg)
	}
	if result.OK {
		t.Error("empty input was accepted")
	}
	if !strings.Contains(result.Message, "empty") {
		t.Errorf("got %q, want an empty-input reason", result.Message)
	}
}
```

Read `internal/model`'s existing test helpers first: use whatever builds a dashboard and a panel model (the plan assumes `newTestModel` and a panel equivalent; if the panel helper has another name, use it).

- [ ] **Step 7: Run to verify they fail**

Run: `go test ./internal/model/ -run 'Jobs|JobLine|DispatchKey' -v`
Expected: FAIL — `SnapshotMsg.Jobs` undefined, `m.jobs` undefined.

- [ ] **Step 8: Implement the model side**

In `internal/model/messages.go`, add to `SnapshotMsg`:

```go
	// Jobs is the daemon's dispatch activity. Always nil on the self-polling
	// path: a client runs no jobs, so it knows of none.
	Jobs []protocol.Job
```

In `internal/model/client.go`, in `listenDaemonCmd`, carry them through:

```go
			return SnapshotMsg{Sessions: snap.Sessions, Jobs: snap.Jobs, Epoch: epoch}
```

In `internal/model/model.go`, add to `Model`:

```go
	// jobs is the daemon's dispatch activity, rendered as one line. Empty
	// while self-polling, because a client owns no jobs.
	jobs []protocol.Job
```

In the `SnapshotMsg` handler, beside `m.applySnapshot(msg.Sessions)` on both the daemon and local branches, set `m.jobs = msg.Jobs`. On the local branch that assigns nil, which is correct.

Render it. In `View`, after `table`:

```go
	jobLine := view.RenderJobLine(m.jobs, m.width)
```

and add it to `parts` after `table` when non-empty. In `panelView`:

```go
func (m Model) panelView() string {
	jobLine := view.RenderJobLine(m.jobs, m.width)
	rows := max(1, m.height-1)
	if jobLine != "" {
		rows = max(1, m.height-2)
	}
	statusBar := view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width, m.daemonHealth())
	table := view.RenderTable(
		m.visibleSessions(), m.cursor, m.selected,
		m.cfg.GetSettingInt("stale_threshold"), m.width, rows,
		m.activeNotification(),
	)
	if jobLine == "" {
		return lipgloss.JoinVertical(lipgloss.Left, statusBar, table)
	}
	return lipgloss.JoinVertical(lipgloss.Left, statusBar, table, jobLine)
}
```

In `tableHeight`, add one line of cost when a job line is present:

```go
	if len(m.jobs) > 0 {
		used++
	}
```

Replace `dispatchCmd`:

```go
func (m Model) dispatchCmd(input string) tea.Cmd {
	cwd := m.dispatchCwd()
	return func() tea.Msg {
		if _, err := dispatch.Submit(context.Background(), dispatch.Options{
			Input:      input,
			Cwd:        cwd,
			SocketPath: protocol.SocketPath(),
			Spawn:      daemonSpawner,
			AckTimeout: 5 * time.Second,
		}); err != nil {
			return ActionResultMsg{Action: "dispatch", OK: false, Message: err.Error()}
		}
		return ActionResultMsg{Action: "dispatch", OK: true, Message: "dispatch queued"}
	}
}

// dispatchCwd is the repository a new worktree should be cut from. A panel's
// own cwd is usually a linked worktree, so resolve the main one from the
// selected session and fall back to this process's cwd.
func (m Model) dispatchCwd() string {
	if s := m.selectedSession(); s != nil && s.Git.GitRoot != "" {
		if main := fetch.MainWorktree(m.ctx, m.cmd, s.Git.GitRoot); main != "" {
			return main
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}
```

Add the `internal/dispatch` and `internal/protocol` imports.

- [ ] **Step 9: Delete action.Dispatch**

Remove `Dispatch` from `internal/action/action.go` and every test for it in `internal/action/action_test.go`. Its validation now lives in `dispatch.Validate`, which guards the daemon rather than one client.

Run: `grep -rn "action.Dispatch" --include='*.go' .`
Expected: no output.

- [ ] **Step 10: Run to verify they pass**

Run: `make test && make lint`
Expected: all packages ok, 0 issues.

- [ ] **Step 11: Verify the job line's absence is asserted**

Mutation: make `RenderJobLine` return `"⚡"` unconditionally instead of `""` for no jobs.
Run: `go test ./internal/view/ ./internal/model/ -run 'RenderJobLineIsEmpty|NoJobsMeansNoJobLine' -v`
Expected: both FAIL.
Revert and confirm PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/view/job.go internal/view/job_test.go internal/model/ internal/fetch/git.go internal/fetch/git_test.go internal/action/
git commit -m "feat(model): render the job line and submit d to the daemon

One line rather than one per job: jobs are serialized so at most one is
doing anything, and a panel is ten rows tall. A failure outranks a running
job, because it is the only state the user has to act on.

The d key now submits to the daemon on the same path the CLI uses, which is
what removes the 15s RunHook timeout that could not cover a real dispatch.
action.Dispatch is deleted; its validation moves to dispatch.Validate,
where it guards the daemon rather than one client.

Jobs are nil on the self-polling path on purpose: a client runs no jobs, so
it knows of none."
```

---

### Task 9: `~/dotfiles` — the client reaches the scripts

**Repository: `~/dotfiles`.** Branch `phase-4-dispatch`.

**Files:**
- Modify: `scripts/scripts/lib/tmux.sh`
- Test: `scripts/scripts/tests/tmux_lib.bats`

**Interfaces:**
- Consumes: `VIGIL_CLIENT`, exported by the daemon into a job's environment (Task 4).
- Produces: `client_dimensions [client]` honoring `VIGIL_CLIENT`; `switch_client_to <target>`; `tmux_reachable()`; `DISPATCH_INLINE` replacing `DISPATCH_IN_POPUP`.

- [ ] **Step 1: Write the failing bats tests**

Append to `scripts/scripts/tests/tmux_lib.bats`:

```bash
@test "client_dimensions targets VIGIL_CLIENT when it is set" {
  export VIGIL_CLIENT="/dev/ttys009"
  run client_dimensions
  [ "${status}" -eq 0 ]
  local args
  args="$(tmux_call_args_matching 'display-message')"
  [ -n "${args}" ]
  [ "$(printf '%s' "${args}" | grep -c -- '-c /dev/ttys009')" -eq 1 ]
}

@test "client_dimensions targets no client when VIGIL_CLIENT is empty" {
  export VIGIL_CLIENT=""
  run client_dimensions
  [ "${status}" -eq 0 ]
  local args
  args="$(tmux_call_args_matching 'display-message')"
  [ -n "${args}" ]
  [ "$(printf '%s' "${args}" | grep -c -- '-c')" -eq 0 ]
}

@test "switch_client_to names the client when VIGIL_CLIENT is set" {
  export VIGIL_CLIENT="/dev/ttys009"
  run switch_client_to "=SC-1 demo:claude"
  [ "${status}" -eq 0 ]
  local args
  args="$(tmux_call_args_matching 'switch-client')"
  [ -n "${args}" ]
  [ "$(printf '%s' "${args}" | grep -c -- '-c /dev/ttys009')" -eq 1 ]
  [ "$(printf '%s' "${args}" | grep -c -- '-t =SC-1 demo:claude')" -eq 1 ]
}

@test "switch_client_to omits -c when VIGIL_CLIENT is empty" {
  export VIGIL_CLIENT=""
  run switch_client_to "=SC-1 demo:claude"
  [ "${status}" -eq 0 ]
  local args
  args="$(tmux_call_args_matching 'switch-client')"
  [ -n "${args}" ]
  [ "$(printf '%s' "${args}" | grep -c -- '-c')" -eq 0 ]
}

@test "switch_client_to never attaches" {
  export VIGIL_CLIENT=""
  unset TMUX
  run switch_client_to "=SC-1 demo:claude"
  [ "$(tmux_call_args_matching 'attach-session' | grep -c .)" -eq 0 ]
}

@test "create_tmux_session switches rather than attaching with no TMUX" {
  unset TMUX
  export VIGIL_CLIENT="/dev/ttys009"
  run create_tmux_session "SC-1 demo" "/tmp/wt" false "" ""
  [ "$(tmux_call_args_matching 'attach-session' | grep -c .)" -eq 0 ]
  [ -n "$(tmux_call_args_matching 'switch-client')" ]
}

@test "run_worktree_popup runs inline when DISPATCH_INLINE is set" {
  export DISPATCH_INLINE=1
  run run_worktree_popup --detached --session-name "SC-1 demo" \
    /tmp "${BATS_TEST_DIRNAME}/stubs/session-script" branch "SC-1 demo"
  [ "$(tmux_call_args_matching 'display-popup' | grep -c .)" -eq 0 ]
}
```

**Landmines these tests are written around, from the phase 3 handoff — do not undo them:**
- `tmux_call_args`, `tmux_call_args_matching` and `tmux_call_index` all end in a pipe and therefore **always exit 0**. `run <helper>; [ "${status}" -eq 0 ]` can never fail. Every assertion above is on output, and each is preceded by `[ -n "${args}" ]` so an empty lookup fails loudly instead of passing vacuously.
- **bash exempts a `!`-negated command from `errexit` unless it is the final statement.** The counted form `[ "$(... | grep -c ...)" -eq 0 ]` above does not depend on position.
- **bats disables `errexit` inside `run`.** None of these tests claims to prove fail-soft behavior under `errexit`.

If `tmux_call_args_matching` does not exist with that name, read `tests/helper.bash` and use the real helpers.

- [ ] **Step 2: Run to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f 'VIGIL_CLIENT|switch_client_to|DISPATCH_INLINE|attaching'`
Expected: FAIL — `switch_client_to: command not found`, and `client_dimensions` never passes `-c`.

- [ ] **Step 3: Implement**

In `lib/tmux.sh`, replace `client_dimensions`:

```bash
#######################################
# Print the height and width of a tmux client.
#
# Factored out because three callers depend on it agreeing with itself:
# panel_geometry sizes the panel against the client, create_tmux_session sizes
# the window it will be split into against the same client, and a daemon-run
# dispatch has no client of its own and must be told which one to measure.
# Two copies of the query could disagree about what "no client" looks like, and
# a panel sized for one window and split into another is exactly the bug this
# guards.
#
# Arguments:
#   client - optional client name; defaults to ${VIGIL_CLIENT}, then to the
#            calling client
# Outputs:
#   e.g. "90 350", or " " with no client
#######################################
client_dimensions() {
  local client="${1:-${VIGIL_CLIENT:-}}"
  if [ -n "${client}" ]; then
    tmux display-message -c "${client}" -p '#{client_height} #{client_width}' 2>/dev/null
    return 0
  fi
  tmux display-message -p '#{client_height} #{client_width}' 2>/dev/null
}
```

The `2>/dev/null` also closes a deferred item from the phase 3 handoff: `client_dimensions` runs before `new-session`, so a dispatch from outside tmux with no server printed a connect error to stderr. Both callers key on empty stdout, so silencing it is safe.

Add `tmux_reachable` beside `is_in_tmux`:

```bash
#######################################
# Report whether a tmux server can be reached, starting one if needed.
#
# Distinct from is_in_tmux, which asks whether *this process* is inside a tmux
# client. A daemon-run dispatch is not, and has no business being refused for
# it: it creates sessions through tmux commands, which need a server, not a
# $TMUX.
# Returns:
#   0 if tmux is usable, 1 otherwise
#######################################
tmux_reachable() {
  command -v tmux > /dev/null 2>&1 || return 1
  tmux start-server > /dev/null 2>&1
}
```

Add `switch_client_to`:

```bash
#######################################
# Switch a client to a target, naming the client when one was given.
#
# Never attaches. A daemon-run job has no terminal, and tmux attach-session
# from one would block until the job's timeout rather than failing.
# Arguments:
#   target - a tmux target, e.g. "=SC-1 demo:claude"
# Returns:
#   0 on success, non-zero if tmux refused
#######################################
switch_client_to() {
  local target="${1}"
  if [ -n "${VIGIL_CLIENT:-}" ]; then
    tmux switch-client -c "${VIGIL_CLIENT}" -t "${target}"
    return "${?}"
  fi
  tmux switch-client -t "${target}"
}
```

Replace the switch/attach sites. In `create_tmux_session`, the existing-session branch (around line 244):

```bash
    if ! ${detached}; then
      switch_client_to "=${session_name}"
    fi
```

and the tail (around line 308):

```bash
  if ${detached}; then
    info "Detached session '${session_name}' created"
    info "To attach: tmux attach-session -t '=${session_name}'"
  else
    info "Switching to session '${session_name}'"
    switch_client_to "=${session_name}"
  fi
```

In `run_worktree_popup` (around line 525):

```bash
  if ! ${detached}; then
    info "Switching to session '${session_name}'"
    switch_client_to "=${session_name}"
  fi
```

and rename the inline gate (around line 515):

```bash
  # DISPATCH_INLINE means "do not open a popup, run it here". Set by
  # dispatch-from-chrome's popup ancestor historically, and by a vigild job
  # today - a daemon is not a popup, which is why this is no longer called
  # DISPATCH_IN_POPUP.
  if [ "${DISPATCH_INLINE:-}" = "1" ]; then
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`
Expected: PASS, all tests including the 60 pre-existing ones.

- [ ] **Step 5: Verify the new tests are not vacuous**

Mutation A: in `client_dimensions`, delete the `-c "${client}"` branch so it always measures the calling client.
Run: `bats tests/tmux_lib.bats -f 'client_dimensions targets VIGIL_CLIENT'`
Expected: FAIL.

Mutation B: in `switch_client_to`, drop the `-c "${VIGIL_CLIENT}"`.
Run: `bats tests/tmux_lib.bats -f 'switch_client_to names the client'`
Expected: FAIL.

Mutation C: restore `tmux attach-session` in `create_tmux_session`'s tail.
Run: `bats tests/tmux_lib.bats -f 'switches rather than attaching'`
Expected: FAIL.

Revert all three and confirm the suite passes.

- [ ] **Step 6: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/tests/tmux_lib.bats
git commit -m "feat(tmux): let a daemon-run job name the client it acts on

client_dimensions takes an optional client and defaults to VIGIL_CLIENT, so
a dispatch with no client of its own sizes the window and the panel against
the client the user will actually be switched to. Without this a
daemon-created session gets tmux's 80x24 default-size and a 40-column panel
arrives at ~175 columns, which is the headless case the phase 3 handoff left
open.

switch_client_to replaces every raw switch-client call and never attaches: a
daemon-run job has no terminal, and attach-session from one would block
until the job's timeout instead of failing.

DISPATCH_IN_POPUP becomes DISPATCH_INLINE, because a daemon is not a popup.

client_dimensions also gained 2>/dev/null, closing the deferred item where a
dispatch from outside tmux with no server printed a connect error."
```

---

### Task 10: `~/dotfiles` — the workflow scripts and the menu bar

**Repository: `~/dotfiles`**, branch `phase-4-dispatch`.

**Files:**
- Modify: `scripts/scripts/shortcut-implement`, `scripts/scripts/gh-review`
- Modify: `scripts/scripts/dispatch-from-chrome`
- Test: `scripts/scripts/tests/` (new file `dispatch_from_chrome.bats`)

**Interfaces:**
- Consumes: `switch_client_to`, `tmux_reachable` (Task 9); `vigil dispatch --cwd <path> <input>` (Task 7).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Change the two workflow scripts**

In `shortcut-implement` at line 135 and `gh-review` at line 134, replace the guard:

```bash
  if ! tmux_reachable; then
    error "tmux is not available"
    error "This script needs a reachable tmux server"
    return 1
  fi
```

In both scripts, replace the two `tmux switch-client -t "=${session_name}:claude"` calls (around lines 197 and 231 in `shortcut-implement`, 197 and 218 in `gh-review`) with:

```bash
    switch_client_to "=${session_name}:claude"
```

keeping each call's existing `${detached} ||` guard where there is one.

- [ ] **Step 2: Verify the workflow scripts still pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`
Expected: PASS.

If any existing test asserted on the literal `switch-client` argv from these scripts, it still passes: `switch_client_to` calls `tmux switch-client`. If one asserted the *absence* of `-c`, update it — with `VIGIL_CLIENT` unset the behavior is unchanged, so set it explicitly in that test rather than loosening the assertion.

- [ ] **Step 3: Write the failing dispatch-from-chrome test**

Create `scripts/scripts/tests/dispatch_from_chrome.bats`:

```bash
#!/usr/bin/env bats

load helper

setup() {
  setup_tmux_stub
  export STUB_LOG="${BATS_TEST_TMPDIR}/calls.log"
  export PATH="${BATS_TEST_TMPDIR}/bin:${PATH}"
  mkdir -p "${BATS_TEST_TMPDIR}/bin"

  # osascript stub: report a Shortcut URL, record activations.
  cat > "${BATS_TEST_TMPDIR}/bin/osascript" <<'STUB'
#!/usr/bin/env bash
printf 'osascript %s\n' "${*}" >> "${STUB_LOG}"
if printf '%s' "${*}" | grep -q 'active tab'; then
  printf 'https://app.shortcut.com/ws/story/12345/a-title\n'
fi
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/osascript"

  cat > "${BATS_TEST_TMPDIR}/bin/vigil" <<'STUB'
#!/usr/bin/env bash
printf 'vigil %s\n' "${*}" >> "${STUB_LOG}"
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/vigil"

  export HOME="${BATS_TEST_TMPDIR}/home"
  mkdir -p "${HOME}/portal"
}

@test "dispatch-from-chrome submits the Chrome URL to vigil dispatch" {
  run "${BATS_TEST_DIRNAME}/../dispatch-from-chrome"
  [ "${status}" -eq 0 ]
  [ "$(grep -c 'vigil dispatch' "${STUB_LOG}")" -eq 1 ]
  [ "$(grep -c -- "--cwd ${HOME}/portal" "${STUB_LOG}")" -eq 1 ]
  [ "$(grep -c 'story/12345' "${STUB_LOG}")" -eq 1 ]
}

@test "dispatch-from-chrome opens no popup" {
  run "${BATS_TEST_DIRNAME}/../dispatch-from-chrome"
  [ "$(tmux_call_args_matching 'display-popup' | grep -c .)" -eq 0 ]
}

@test "dispatch-from-chrome honours --repo" {
  mkdir -p "${HOME}/vigil"
  run "${BATS_TEST_DIRNAME}/../dispatch-from-chrome" --repo vigil
  [ "${status}" -eq 0 ]
  [ "$(grep -c -- "--cwd ${HOME}/vigil" "${STUB_LOG}")" -eq 1 ]
}

@test "dispatch-from-chrome rejects a URL it cannot route" {
  cat > "${BATS_TEST_TMPDIR}/bin/osascript" <<'STUB'
#!/usr/bin/env bash
printf 'osascript %s\n' "${*}" >> "${STUB_LOG}"
if printf '%s' "${*}" | grep -q 'active tab'; then
  printf 'https://example.com/nothing\n'
fi
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/osascript"
  run "${BATS_TEST_DIRNAME}/../dispatch-from-chrome"
  [ "$(grep -c 'vigil dispatch' "${STUB_LOG}")" -eq 0 ]
  [ "$(grep -c 'display notification' "${STUB_LOG}")" -ge 1 ]
}
```

Read `tests/helper.bash` for the real stub-setup function name before writing `setup_tmux_stub`.

- [ ] **Step 4: Run to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/dispatch_from_chrome.bats`
Expected: FAIL — the script still opens a popup and never calls `vigil dispatch`.

- [ ] **Step 5: Rewrite dispatch-from-chrome's main**

Keep: the Chrome URL read with its stderr stash, the URL validation, the macOS notifications, the `--repo` flag and its directory check, and the iTerm activate-or-attach branch. Replace only the popup tunnel.

```bash
main() {
  local repo="portal"
  while [ "${#}" -gt 0 ]; do
    case "${1}" in
      --repo)
        repo="${2}"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  if [ ! -d "${HOME}/${repo}" ]; then
    notify "Unknown repo: ${repo}"
    exit 1
  fi

  local url
  url="$(get_chrome_url)"

  if [[ -z "${url}" ]]; then
    local err
    err="$(cat "${CHROME_ERR}" 2>/dev/null)"
    rm -f "${CHROME_ERR}"
    if [[ -n "${err}" ]]; then
      notify "Could not get Chrome tab URL: ${err}"
    else
      notify "Could not get Chrome tab URL - is Chrome running?"
    fi
    exit 1
  fi
  rm -f "${CHROME_ERR}"

  if ! [[ "${url}" =~ app\.shortcut\.com ]] && \
     ! [[ "${url}" =~ github\.com.*/pull/ ]]; then
    notify "Not a Shortcut story or GitHub PR: ${url}"
    exit 0
  fi

  # Bring a client to the front before submitting. The job ends with a
  # switch-client, and vigild resolves the most recently active client to
  # switch: with nothing attached anywhere there is nothing for the teleport
  # to land on, so attach a window first.
  ensure_client

  if ! vigil dispatch --cwd "${HOME}/${repo}" "${url}"; then
    notify "Dispatch failed - see the vigil panel or vigild.log"
    exit 1
  fi
}

#######################################
# Make sure some tmux client exists and iTerm2 is in front, so the job's
# closing switch-client has a client to act on.
#######################################
ensure_client() {
  local tmux_session tmux_path
  tmux_session="$(get_tmux_session)"

  if [[ -n "${tmux_session}" ]] && \
     tmux list-clients -t "${tmux_session}" 2>/dev/null | grep -q .; then
    osascript -e 'tell application "iTerm2" to activate'
    return 0
  fi

  if [[ -z "${tmux_session}" ]]; then
    osascript -e 'tell application "iTerm2" to activate'
    return 0
  fi

  tmux_path="$(command -v tmux)"
  osascript <<APPLESCRIPT
tell application "iTerm2"
  activate
  if (count of windows) = 0 then
    create window with default profile command "${tmux_path} attach -t ${tmux_session}"
  else
    tell current window
      create tab with default profile command "${tmux_path} attach -t ${tmux_session}"
    end tell
  end if
end tell
APPLESCRIPT

  local deadline=$(( SECONDS + 5 ))
  while ! tmux list-clients -t "${tmux_session}" 2>/dev/null | grep -q .; do
    if [ "${SECONDS}" -ge "${deadline}" ]; then
      notify "Timed out waiting for a tmux client to attach"
      return 0
    fi
    sleep 0.1
  done
}
```

Update the file's header comment: it no longer runs `dispatch` in a popup.

- [ ] **Step 6: Run to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`
Expected: PASS, all files.

- [ ] **Step 7: Verify the popup really is gone**

Run: `grep -n "display-popup\|DISPATCH_IN_POPUP\|url_file" dispatch-from-chrome`
Expected: no output.

Run: `grep -rn "DISPATCH_IN_POPUP" .`
Expected: no output anywhere in the repository.

- [ ] **Step 8: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/shortcut-implement scripts/scripts/gh-review \
        scripts/scripts/dispatch-from-chrome scripts/scripts/tests/
git commit -m "feat(dispatch): submit to vigild instead of opening a popup

dispatch-from-chrome now reads the Chrome tab and calls vigil dispatch. The
display-popup tunnel, the URL temp file and DISPATCH_IN_POPUP are gone; the
iTerm activate-or-attach branch stays, because the job ends with a
switch-client and with nothing attached anywhere there is nothing for the
teleport to land on.

The two workflow scripts guard on tmux_reachable rather than \$TMUX: a
daemon-run job is not inside a tmux client and has no business being
refused for it. Their switch-client calls go through switch_client_to so
they honour VIGIL_CLIENT."
```

---

### Task 11: Real-machine verification and the handoff

**Files:**
- Create: `docs/superpowers/2026-07-29-phase-4-handoff.md` (in `~/vigil`)
- Modify: `CLAUDE.md` (in `~/vigil`)

**Interfaces:** none. This task produces evidence and documentation.

**Why this task exists:** the bats tmux stub returns a constant `pane_width` and cannot observe real geometry. That blind spot hid phase 3's 175-column defect through seven per-task reviews. Nothing in Tasks 9-10 can show that a *dispatched* session comes out the right size.

- [ ] **Step 1: Install both halves**

```bash
cd ~/vigil && make build && make install
```

Confirm `make install` used the temp-file rename (a new inode), so a running daemon's image is untouched. Then kill the old daemon and let a panel respawn it:

```bash
pkill -f 'vigil daemon'
```

Wait for a panel to respawn one and confirm: `pgrep -f 'vigil daemon'`.

- [ ] **Step 2: Set the hook**

In `~/.config/vigil/config.toml`, back the file up first, then set:

```toml
dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {input}"
```

Note `--detached` is gone: that is what lets the teleport happen.

- [ ] **Step 3: Verify `vigil dispatch` against a live daemon**

```bash
vigil dispatch --cwd "$HOME/portal" sc-99999
echo "exit: $?"
```

A nonexistent story is fine here — the point is the submission path. Expected: exit 0 and `dispatch queued`, then a `✗` job line in every open panel within a few seconds carrying the script's real failure line, retained for its window.

Record: the exit code, the job line text, and how long the failure stayed.

- [ ] **Step 4: Verify the skew message**

With a daemon from before this branch running (check out `main`, `make install`, restart the daemon, then reinstall the branch binary *without* restarting):

```bash
vigil dispatch --cwd "$HOME/portal" sc-99999
echo "exit: $?"
```

Expected: non-zero, with a message naming an older vigil. This is what the first upgrade will show, so confirm it reads well.

- [ ] **Step 5: Verify a real dispatch end to end, at the right size**

Method, matching phase 3's: a `tmux` shim on `PATH` forwarding to `tmux -L <name>`, an isolated `HOME` and `XDG_RUNTIME_DIR` so the config and socket paths never touch the real ones, and a real client of known dimensions.

With a **portrait** client attached (tall and narrow, so `panel_geometry` should choose `-vb`), dispatch a real story. Then record, from the created session:

```bash
tmux list-panes -a -F '#{session_name}|#{pane_id}|#{pane_width}|#{pane_height}|#{@vigil_panel}'
```

Expected: the window is the client's size, not 80x24, and the panel pane is the configured row count rather than a proportional scale-up of it. **A ~175-column panel here means the `VIGIL_CLIENT` thread is broken somewhere between the daemon and `client_dimensions`.**

Repeat with a **landscape** client and confirm the panel comes out as a 40-column left column.

- [ ] **Step 6: Verify the teleport**

From the menu bar button, with a Shortcut story in the active Chrome tab: iTerm comes forward immediately, the panel shows a live `⚡` line that updates through the script's phases, and when setup finishes the client switches to the new session's `claude` window with Claude already running.

Record how long the job line was visible and whether any phase left it stale for an uncomfortable stretch.

- [ ] **Step 7: Verify a dispatch with nothing attached**

Detach every client (`tmux detach-client -a`), then `vigil dispatch --cwd "$HOME/portal" <story>`. Expected: the job runs, the session is created, no switch happens, and nothing hangs — this is the path where an `attach-session` would have blocked until the timeout.

Confirm no `attach-session` was involved.

- [ ] **Step 8: Verify `d` inside vigil**

Open the dashboard, press `d`, paste a story URL, press Enter. Expected: the same outcome as the menu bar button, and specifically **not** a 15-second timeout.

- [ ] **Step 9: Restore and diff**

Restore the config backup and confirm it diffs clean. Confirm the developer's real tmux server, daemon and sessions were left as they were.

- [ ] **Step 10: Write the handoff**

Create `docs/superpowers/2026-07-29-phase-4-handoff.md`, in the shape of the phase 3 handoff. It must contain:

- What landed, in both repositories, with the branch names.
- **Verification results, and what they do not prove.** Say plainly which checks were run on a real machine, which were only unit-tested, and any counterfactual that failed to reproduce. Phase 3's handoff is the standard: it recorded that its central counterfactual could not be reproduced by hand and said so rather than implying the fix was demonstrated.
- Every correction made to this plan during execution. Assume there are some: six briefs in the phase 3 plan contained defects written by the plan's author.
- The deferred list, by area.
- Landmines, including these, carried forward or newly true:
  - `tmux new-session -d` gives an 80x24 window. Fixed for the dispatch path by `VIGIL_CLIENT`; the next thing that sizes a pane at creation time will hit it again.
  - A job dies with the daemon, leaving the same half-made worktree a dismissed popup leaves.
  - No dismiss key for a failed job; it occupies its line for 10 minutes.
  - Nothing enforces that `transition.Runner` is constructed only in `internal/daemon`.
  - The status line is only as good as the scripts' output: a script that goes quiet for 40 seconds shows a stale line for 40 seconds.
  - The bats tmux stub cannot observe geometry. Anything about pane geometry needs a real tmux server.
- Process notes: what the reviews caught, and what they missed.

- [ ] **Step 11: Update CLAUDE.md**

Add to "Architecture": `internal/dispatch/` and the `vigil dispatch` subcommand. Update the `internal/daemon/` bullet to mention the job runner, the per-connection reader and the serialized queue. Update the `internal/protocol/` bullet: it is no longer only a snapshot protocol.

Add to "Key Conventions":
- The snapshot is the ack; there is no response frame; `protocol.Version` stays 1 because `Jobs` is additive and direction disambiguates the frame types.
- Jobs are serialized and run off the poll goroutine; `Snapshot.Jobs` is a copy taken under the job mutex.
- `VIGIL_CLIENT` is how a daemon-run job learns which client to size a window against and switch, and why it is an environment variable rather than a flag.
- `client_dimensions` honoring `VIGIL_CLIENT` is what keeps a dispatched session off tmux's 80x24 default.
- `vigil dispatch` exits 0 on acceptance, not success.

Move the "In-flight design work" section forward: phase 4 is merged, phase 5 (the work queue) is next, and the same "live on it first" rule applies.

- [ ] **Step 12: Commit**

```bash
cd ~/vigil
git add docs/superpowers/2026-07-29-phase-4-handoff.md CLAUDE.md
git commit -m "docs: record phase 4's verification results and landmines

Includes what the verification does not prove. The bats tmux stub cannot
observe pane geometry, so the assertion that a dispatched session comes out
at the client's size rather than tmux's 80x24 rests entirely on the
real-machine check recorded here."
```

---

## Self-Review

**Spec coverage.** Every section of the spec maps to a task:

| Spec section | Task |
|---|---|
| `Request`, `Job`, `Snapshot.Jobs`, version stays 1 | 1 |
| The snapshot is the ack; skew message | 1, 7 |
| Streaming subprocess output; `StreamCommander` | 2 |
| `RunHookStream`, shared `hookArgv`, `dispatch_timeout` | 3 |
| Serialized queue, dedup, refusals as failed jobs, retention | 4 |
| `MostRecentClient`, `VIGIL_CLIENT` exported | 4 |
| Reader goroutine per client; writer sole closer | 5 |
| Jobs off the poll goroutine; copied under a mutex; `Run` waits | 6 |
| `vigil dispatch [--cwd]`, after the `LookPath` check | 7 |
| Spawn-then-submit | 7 |
| `spawnDaemon` strips `TMUX`/`TMUX_PANE` | 7 |
| Job line in panel and dashboard | 8 |
| `d` submits to the daemon; `action.Dispatch` deleted | 8 |
| `d` resolves the main worktree | 8 |
| `client_dimensions` honors `VIGIL_CLIENT` | 9 |
| `switch_client_to`, never attaches | 9 |
| `is_in_tmux` becomes reachability | 9, 10 |
| `DISPATCH_IN_POPUP` → `DISPATCH_INLINE`, into the hook string | 9, 11 |
| `dispatch-from-chrome` calls `vigil dispatch`, keeps activate-or-attach | 10 |
| Error-handling table | 4, 7 |
| Real-tmux geometry verification | 11 |

**Type consistency.** `RunStream`'s signature is identical in Tasks 2, 4 and 7 (`ctx, dir string, env []string, name string, args []string, onLine func(string)`). `RunHookStream`'s parameter order matches between Task 3's definition and Task 4's call. `dispatch.Options` field names match between Task 7's definition and Task 8's use. `protocol.Job` field names are the same in Tasks 1, 4, 7 and 8.

**One defect found and fixed inline:** Task 4's first draft published the job's cwd via a `jobCwd(job)` helper reading `job.cwd`, a field `protocol.Job` does not and must not have — the cwd is a property of the request and has no business on the wire. Step 7 now calls this out explicitly and specifies the `cwds` side map instead, rather than leaving an implementer to discover a non-compiling plan.

**Known gap, stated rather than hidden:** no task adds an architectural guard that `transition.Runner` is constructed only in `internal/daemon`. It is carried as a landmine in Task 11 instead. Adding it is a one-task follow-up, not phase 4 scope.

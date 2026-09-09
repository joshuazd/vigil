# Instant current-session highlight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a vigil panel re-highlight the current tmux session immediately after a session switch, instead of up to one poll interval later, without adding any polling.

**Architecture:** tmux's `client-session-changed` hook runs `vigil poke`, which writes one `Request` frame to the daemon socket. The daemon's `handleRequest` already ends in an unconditional `publishJobs`, which rebroadcasts the snapshot it already holds - so a poke needs one switch arm and no new method. Each client's `listenDaemonCmd` returns on the arriving snapshot and re-runs `annotateClientFlags`, which re-resolves `IsCurrent`/`IsLast` from tmux state per arriving snapshot (with `newModel` seeding `IsCurrent` once from the cache at startup). No poll, no git, no `gh`, no client changes. (corrected 2026-09-09 after tracing to code)

**Tech Stack:** Go, Bubble Tea, newline-delimited JSON over a unix socket, bats + shellcheck on the `~/dotfiles` side.

**Spec:** `docs/superpowers/specs/2026-09-09-instant-current-session-highlight-design.md`

## Global Constraints

- `protocol.Version` stays **1**. Every change here is additive.
- The poke frame carries an **empty `ID`**. `jobs.submit` drops empty-ID frames before its reason switch, so an old daemon registers no refused job for the frame - it does **not** ignore the poke: `handleRequest`'s `default` arm still reaches the unconditional `publishJobs`, so an old daemon rebroadcasts too, verified 2026-09-09 against a live pre-feature daemon. Never give the frame a generated id. (corrected 2026-09-09, verified against a live pre-feature daemon)
- `make test` is `go test -race ./...`. **`-race` is not optional**: the daemon's design is a concurrency claim.
- `make lint` (golangci-lint) must report 0 issues.
- **Every test brief below mandates a mutation check**: break the subject, run the test, paste the failing output into the task report, restore. Across three prior plans in this repository, nineteen briefs contained tests that would have passed with their subject deleted. A task report without pasted mutation output is incomplete.
- **Do not write a test for the explicit `case protocol.RequestPoke:` arm.** It has no behavioural signature - deleting it drops a poke into `default`, where `submit` discards the empty ID and the same rebroadcast still happens. Any test claiming to cover it is vacuous by construction.
- **No changes to `internal/model`'s production code.** `listenDaemonCmd` already re-runs `annotateClientFlags` per arriving snapshot. Task 4 adds a test only.
- `vigil poke` must be **silent**, exit **0** even with no daemon listening, **spawn no daemon**, and **wait for no ack**.
- The tmux hook must be fail-soft and must **not** be added to `tmux-hop`, which must never reference vigil.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/protocol/protocol.go` | Add `RequestPoke` const + doc comment. Modify. |
| `internal/protocol/protocol_test.go` | Round-trip test. Modify. |
| `internal/daemon/daemon.go:392-399` | One `case` arm in `handleRequest`. Modify. |
| `internal/daemon/daemon_test.go` | Poke behaviour tests. Modify. |
| `internal/dispatch/poke.go` | **Create.** `Poke(socketPath string) error` - dial, write one frame, close. |
| `internal/dispatch/poke_test.go` | **Create.** Frame-on-the-wire and no-listener tests. |
| `main.go` | `parseArgs` + first `switch` arm for `poke`. Modify. |
| `main_test.go` | `poke` exits 0 silently with no daemon. Modify. |
| `internal/model/transition_test.go` | Repeat-snapshot regression test. Modify. |
| `README.md`, `CLAUDE.md` | Docs. Modify. |
| `~/dotfiles/tmux/.tmux.conf` | The hook. Modify. |
| `~/dotfiles/CLAUDE.md` | Why a hook and not `tmux-hop`. Modify. |

---

### Task 1: The poke request type

**Files:**
- Modify: `internal/protocol/protocol.go` (after the `RequestDismiss` const, around line 35)
- Test: `internal/protocol/protocol_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `protocol.RequestPoke` (a `string` const, value `"poke"`). Tasks 2 and 3 both use it.

- [ ] **Step 1: Write the failing test**

Append to `internal/protocol/protocol_test.go`:

```go
// TestThePokeRequestTypeRoundTrips pins the wire value and the empty ID
// together. The empty ID is not incidental: jobs.submit drops an empty-ID
// frame before its reason switch, which is the only thing stopping a poke
// sent to an old daemon from becoming a refused job on every session switch.
func TestThePokeRequestTypeRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeRequest(&buf, &Request{Version: Version, Type: RequestPoke}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := NewRequestDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Type != RequestPoke {
		t.Errorf("Type = %q, want %q", got.Type, RequestPoke)
	}
	if got.ID != "" {
		t.Errorf("ID = %q, want empty (an old daemon must drop this frame)", got.ID)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1 (every change here is additive)", got.Version)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test -race ./internal/protocol/ -run TestThePokeRequestTypeRoundTrips`
Expected: compile failure, `undefined: RequestPoke`.

- [ ] **Step 3: Add the constant**

In `internal/protocol/protocol.go`, directly after the `RequestDismiss` const:

```go
// RequestPoke asks the daemon to rebroadcast the snapshot it already holds.
// It exists so a tmux client-session-changed hook can make every panel
// re-resolve IsCurrent/IsLast at once, which annotateClientFlags only does
// when a snapshot arrives - otherwise the highlight lags by up to a tick.
//
// It deliberately triggers no poll: no tmux re-read, no git, no gh, and no
// nudge to the remote pollers. A poke therefore cannot be slower than the
// tick it pre-empts, which an immediate-poll variant could be on a cold
// worktree.
//
// Like RequestDismiss it carries an empty ID, so jobs.submit drops it before
// its reason switch and a poke aimed at an old daemon registers no refused
// job named for a type that daemon does not know. It is NOT inert against an
// old daemon: handleRequest's default arm still reaches the unconditional
// publishJobs at the bottom of the request handler, so an old daemon
// rebroadcasts its held snapshot exactly as a new one does - verified
// 2026-09-09 against a live pre-feature daemon binary. The daemon never
// restarts itself, so that version skew is the normal state right after
// `make install` - not a corner case.
const RequestPoke = "poke"
```

(corrected 2026-09-09, verified against a live pre-feature daemon)

- [ ] **Step 4: Run it and watch it pass**

Run: `go test -race ./internal/protocol/ -run TestThePokeRequestTypeRoundTrips -v`
Expected: PASS.

- [ ] **Step 5: Mutation check**

Change the const value to `"pokey"`, re-run, paste the failing output into the report, then restore. Expected failure: `Type = "pokey", want "poke"`.

- [ ] **Step 6: Commit**

```bash
git add internal/protocol/protocol.go internal/protocol/protocol_test.go
git commit -m "feat(protocol): add the poke request type"
```

---

### Task 2: The daemon rebroadcasts on a poke

**Files:**
- Modify: `internal/daemon/daemon.go:392-399` (the `switch req.Type` in `handleRequest`)
- Test: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `protocol.RequestPoke` from Task 1.
- Produces: no new exported surface. Behaviour only: a `RequestPoke` frame causes one rebroadcast of `s.latest` and registers no job.

**Context the implementer needs.** `handleRequest` currently reads:

```go
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
	s.publishJobs(s.jobs.snapshot())
}
```

That trailing `publishJobs` is unconditional and is already the rebroadcast primitive. Do **not** add a new method.

The test fixture needs two things `testServer` does not give you. `testServer` returns a bare `&Server{}` with **no jobs runner**, and `handleRequest` returns early when `s.jobs == nil` - so wire one the way the existing dispatch tests do: `srv.jobs = newJobs(testJobsConfig(), stream, fetch.NewMockCommander(), srv.logf)`. And `testServer` sets `Interval: 50 * time.Millisecond`, whose ticker would poll continuously and destroy any "no poll" measurement; set `Interval` to an hour and populate `s.latest` by calling `srv.poll` once **before** `startServer`, while there is no other goroutine and no client.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/daemon_test.go`:

```go
// pokeServer returns a server whose ticker will not fire during a test, with
// one snapshot already published. Interval is an hour and the single poll is
// done here, on the test's own goroutine before Run starts, because a poke
// must be shown to add no subprocess calls of its own - a 50ms ticker would
// make that unmeasurable.
func pokeServer(t *testing.T) *Server {
	t.Helper()
	srv := testServer(t)
	srv.Interval = time.Hour
	// The commander here only satisfies newJobs; no job is ever submitted in
	// these tests. The one whose calls get counted is the collector's, which
	// testServer already primed - reach it with srv.Collector.Cmd.
	srv.jobs = newJobs(testJobsConfig(), newBlockingStream(), fetch.NewMockCommander(), srv.logf)
	// No clients and no Run goroutine yet, so touching clients here is safe.
	srv.poll(context.Background())
	if srv.latest == nil {
		t.Fatal("fixture failed to publish a first snapshot")
	}
	return srv
}

func sendPoke(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version,
		Type:    protocol.RequestPoke,
	}); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
}

// TestAPokeRebroadcastsTheLatestSnapshot is the feature. The client reads the
// snapshot addClient sends it, pokes, and must get a second one without any
// tick having fired - Interval is an hour.
func TestAPokeRebroadcastsTheLatestSnapshot(t *testing.T) {
	srv := pokeServer(t)
	startServer(t, srv)

	conn, err := net.Dial("unix", srv.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	dec := protocol.NewDecoder(conn)
	if _, err := dec.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}

	sendPoke(t, conn)

	second, err := dec.Next()
	if err != nil {
		t.Fatalf("second Next (the poke did not rebroadcast): %v", err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want the same one session named alpha", second.Sessions)
	}
}

// TestAPokeRunsNoPollAndKeepsTheTimestamp pins the economy of the whole
// design. A poke must reuse the held snapshot: no new subprocess, and the
// Timestamp carried over unchanged, because a rebroadcast must not make a
// stalled collector's data look fresh. (corrected 2026-09-09 after tracing to code)
func TestAPokeRunsNoPollAndKeepsTheTimestamp(t *testing.T) {
	srv := pokeServer(t)
	pollCmd := srv.Collector.Cmd.(*fetch.MockCommander)
	before := pollCmd.CallCount("tmux")
	startServer(t, srv)

	conn, err := net.Dial("unix", srv.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	dec := protocol.NewDecoder(conn)
	first, err := dec.Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}

	sendPoke(t, conn)

	second, err := dec.Next()
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if second.Timestamp != first.Timestamp {
		t.Errorf("Timestamp moved %d -> %d; a poke must not poll or restamp",
			first.Timestamp, second.Timestamp)
	}
	if got := pollCmd.CallCount("tmux"); got != before {
		t.Errorf("tmux call count %d -> %d; a poke must issue no subprocess", before, got)
	}
}

// TestAPokeBeforeTheFirstPollBroadcastsNothing guards the one case where a
// rebroadcast would be actively harmful. publishJobs refuses to invent a
// snapshot when latest is nil, because a frame with nil Sessions blanks every
// client's table - far worse than a highlight arriving one tick late. A poke
// can hit this for real: the hook fires on any session switch, including one
// a second after a cold daemon started.
func TestAPokeBeforeTheFirstPollBroadcastsNothing(t *testing.T) {
	srv := testServer(t)
	srv.Interval = time.Hour // no tick will fire, so latest stays nil
	srv.jobs = newJobs(testJobsConfig(), newBlockingStream(), fetch.NewMockCommander(), srv.logf)
	startServer(t, srv)

	conn, err := net.Dial("unix", srv.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	sendPoke(t, conn)

	snap, err := protocol.NewDecoder(conn).Next()
	if err == nil {
		t.Fatalf("got snapshot %+v, want none: a poke must not invent one", snap)
	}
}

// TestAPokeRegistersNoJob pins the empty-ID contract. Give the poke frame a
// generated id and it lands as a refused job on every session switch against
// a daemon that predates RequestPoke.
func TestAPokeRegistersNoJob(t *testing.T) {
	srv := pokeServer(t)
	startServer(t, srv)

	conn, err := net.Dial("unix", srv.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	dec := protocol.NewDecoder(conn)
	if _, err := dec.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}

	sendPoke(t, conn)

	second, err := dec.Next()
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if len(second.Jobs) != 0 {
		t.Errorf("got jobs %+v, want none", second.Jobs)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test -race ./internal/daemon/ -run 'TestAPoke' -v`
Expected: all three FAIL. `TestAPokeRebroadcastsTheLatestSnapshot` fails on `second Next` with an i/o timeout, because a poke currently falls into `default` and is dropped by `submit`'s empty-ID guard before `publishJobs` is reached.

**If any test passes here, stop and report it.** A pass means the frame is already reaching `publishJobs`, and the test is not measuring what it claims.

- [ ] **Step 3: Add the switch arm**

In `internal/daemon/daemon.go`, inside `handleRequest`'s switch, between the `RequestDismiss` and `default` arms:

```go
	case protocol.RequestPoke:
		// Nothing to change: the unconditional publishJobs below is the whole
		// point of the frame. An explicit arm rather than letting this fall
		// through `default`, which reaches the same rebroadcast only by way
		// of submit discarding an empty ID - a coupling between two unrelated
		// pieces of code that would break silently if either moved.
```

Reusing `publishJobs` rather than adding a method is deliberate: it already never invents a snapshot when `latest` is nil, already carries `Timestamp` over, and is already documented as Run's-goroutine-only, which is what makes touching `clients` safe.

- [ ] **Step 4: Run them and watch them pass**

Run: `go test -race ./internal/daemon/ -run 'TestAPoke' -v`
Expected: three PASS.

- [ ] **Step 5: Mutation checks - paste all three**

1. Replace the trailing `s.publishJobs(s.jobs.snapshot())` in `handleRequest` with a bare `return`. Expected: `TestAPokeRebroadcastsTheLatestSnapshot` fails on `second Next` with an i/o timeout. Restore.
2. In `publishJobs`, add `updated.Timestamp = time.Now().Unix()` after `updated := *latest`. Expected: `TestAPokeRunsNoPollAndKeepsTheTimestamp` fails with `Timestamp moved`. Restore.
3. In `jobs.submit`, delete the `req.ID == ""` half of the guard. Expected: `TestAPokeRegistersNoJob` fails with a refused job in the list. Restore.
4. In `publishJobs`, delete the `if latest == nil { return }` early return. Expected: `TestAPokeBeforeTheFirstPollBroadcastsNothing` fails, having received an invented snapshot. Restore.

**A note on mutation 2.** `Timestamp` is Unix *seconds*, so within a fast test a stray poll would often produce the same value. That is why the no-poll claim rests on the `CallCount` assertion and not on the timestamp - the timestamp assertion is specifically about `publishJobs` not restamping. Do not collapse the two.

- [ ] **Step 6: Full suite and lint**

Run: `make test && make lint`
Expected: every package ok, 0 lint issues.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): rebroadcast the held snapshot on a poke"
```

---

### Task 3: `vigil poke`

**Files:**
- Create: `internal/dispatch/poke.go`
- Create: `internal/dispatch/poke_test.go`
- Modify: `main.go` (`parseArgs`, and the **first** `switch command` block around line 75)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `protocol.RequestPoke` from Task 1.
- Produces: `func dispatch.Poke(socketPath string) error` - dials a unix socket, writes one `RequestPoke` frame, closes. Returns a non-nil error when no daemon is listening. `main.go` ignores that error and exits 0.

- [ ] **Step 1: Write the failing tests**

Create `internal/dispatch/poke_test.go`:

```go
package dispatch

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// TestPokeWritesOnePokeFrame is the contract the tmux hook depends on.
func TestPokeWritesOnePokeFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan *protocol.Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		req, err := protocol.NewRequestDecoder(conn).Next()
		if err != nil {
			return
		}
		got <- req
	}()

	if err := Poke(path); err != nil {
		t.Fatalf("Poke: %v", err)
	}

	select {
	case req := <-got:
		if req.Type != protocol.RequestPoke {
			t.Errorf("Type = %q, want %q", req.Type, protocol.RequestPoke)
		}
		if req.ID != "" {
			t.Errorf("ID = %q, want empty", req.ID)
		}
		if req.Version != protocol.Version {
			t.Errorf("Version = %d, want %d", req.Version, protocol.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame arrived")
	}
}

// TestPokeWithNoDaemonFailsWithoutSpawningOne matters because this runs from
// a tmux hook on every session switch. Spawning a daemon there would start
// processes behind the user's back; the error is for main to swallow.
func TestPokeWithNoDaemonFailsWithoutSpawningOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	if err := Poke(path); err == nil {
		t.Fatal("Poke returned nil with nothing listening")
	}
	if _, err := net.Listen("unix", path); err != nil {
		t.Fatalf("Poke appears to have created something at the socket path: %v", err)
	}
}
```

Append to `main_test.go`:

```go
// TestPokeExitsZeroAndSilentlyWithNoDaemon is what keeps the tmux hook quiet.
// A non-zero exit or a line on stderr would surface in the user's pane on
// every session switch. Poke is also dispatched before the tmux/git/gh
// LookPath gate, so a machine missing gh still pokes silently rather than
// printing "gh not found in PATH".
func TestPokeExitsZeroAndSilentlyWithNoDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"poke"}, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("got stdout %q stderr %q, want both empty", stdout.String(), stderr.String())
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test -race ./internal/dispatch/ -run TestPoke -v && go test -race . -run TestPoke -v`
Expected: `undefined: Poke`, and `run([]string{"poke"})` returning 1 with `unknown argument: poke` on stderr.

- [ ] **Step 3: Implement `Poke`**

Create `internal/dispatch/poke.go`:

```go
package dispatch

import (
	"net"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// pokeDialTimeout bounds the dial. This runs from a tmux hook on every
// session switch, so it must never hang: a lost poke costs one tick of a
// stale highlight, which is exactly the state it was trying to improve.
const pokeDialTimeout = 500 * time.Millisecond

// Poke asks a running daemon to rebroadcast the snapshot it already holds, so
// every client re-resolves which session is current without waiting for the
// next tick.
//
// It deliberately does less than Submit. It does not spawn a daemon: a hook
// that fires on every session switch must not start processes. It does not
// wait for an ack, because unlike a dispatch there is no job id to watch for
// and nothing is lost by a dropped frame. The error is returned for tests;
// main swallows it and exits 0 so the hook stays silent.
func Poke(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, pokeDialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version,
		Type:    protocol.RequestPoke,
	})
}
```

- [ ] **Step 4: Wire it into `main.go`**

In `parseArgs`, add alongside the existing cases:

```go
	case "poke":
		return "poke", args[1:], nil
```

In the **first** `switch command` block - the one holding `help`, `version` and `config`, before the `startupDependencies` `LookPath` loop:

```go
	case "poke":
		// Before the dependency gate and before config.Load on purpose. This
		// runs from a tmux hook on every session switch: a "gh not found"
		// line would land in the user's pane, and a poke needs neither the
		// config nor any of tmux/git/gh in PATH.
		_ = dispatch.Poke(protocol.SocketPath())
		return 0
```

Add `internal/dispatch` and `internal/protocol` to `main.go`'s imports if not already present.

- [ ] **Step 5: Run them and watch them pass**

Run: `go test -race ./internal/dispatch/ -run TestPoke -v && go test -race . -run TestPoke -v`
Expected: three PASS.

- [ ] **Step 6: Mutation checks - paste both**

1. In `Poke`, change `Type: protocol.RequestPoke` to `Type: protocol.RequestDismiss`. Expected: `TestPokeWritesOnePokeFrame` fails on `Type = "dismiss"`. Restore.
2. In `main.go`, move the `poke` arm from the first switch into the second (after the `LookPath` loop) and temporarily add a bogus name to `startupDependencies`. Expected: `TestPokeExitsZeroAndSilentlyWithNoDaemon` fails on a non-empty stderr and exit 1. Restore both.

- [ ] **Step 7: Full suite, lint, and a real end-to-end check**

Run: `make test && make lint && make build`
Then, with a daemon running, confirm a poke is accepted rather than refused:

```bash
./vigil poke; echo "exit=$?"
```

Expected: `exit=0`, no output, and no new job line in any open panel. Paste the output into the report.

- [ ] **Step 8: Commit**

```bash
git add internal/dispatch/poke.go internal/dispatch/poke_test.go main.go main_test.go
git commit -m "feat(cli): add vigil poke"
```

---

### Task 4: A repeat snapshot must not double a toast

**Files:**
- Test only: `internal/model/transition_test.go`

**Interfaces:**
- Consumes: nothing. No production code changes.
- Produces: nothing.

**Why this exists.** Before a poke, two identical snapshots in a row were rare. After it they happen on every session switch. `checkStateTransitions` runs `m.detector.Detect(m.sessions)` and adds a toast per event, so if the detector ever returned an event for unchanged input, every session switch would spray duplicate toasts. It should not - the detector compares against stored state - but this is a cheap pin on a behaviour a poke now depends on.

- [ ] **Step 1: Write the test**

Append to `internal/model/transition_test.go`:

```go
// TestARepeatSnapshotAddsNoSecondToast pins what a poke depends on. A poke
// makes the daemon rebroadcast an unchanged snapshot, so identical input
// arrives routinely; if Detect reported an event for it, every tmux session
// switch would duplicate every toast.
func TestARepeatSnapshotAddsNoSecondToast(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions()
	m.sessions = []*session.Session{blockedSession("alpha")}
	m.checkStateTransitions()

	afterRealChange := len(m.notifications)
	if afterRealChange == 0 {
		t.Fatal("fixture produced no toast to begin with")
	}

	// The same sessions again, as a poke's rebroadcast delivers them.
	m.checkStateTransitions()

	if got := len(m.notifications); got != afterRealChange {
		t.Errorf("got %d notifications after a repeat snapshot, want %d", got, afterRealChange)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -race ./internal/model/ -run TestARepeatSnapshotAddsNoSecondToast -v`
Expected: PASS immediately - this is a regression pin, not a bug fix.

- [ ] **Step 3: Mutation check**

This test's subject is in `internal/transition`. In `transition.Detect`, make it emit an event even when the state is unchanged (remove the equality check that suppresses a no-op transition). Expected: this test fails with `got 2 notifications ... want 1`. Paste the output, then restore.

**If the mutation does not make this test fail, report it** - the test is not reaching the detector and needs rewriting rather than committing.

- [ ] **Step 4: Commit**

```bash
git add internal/model/transition_test.go
git commit -m "test(model): pin that a repeat snapshot does not double a toast"
```

---

### Task 5: vigil documentation

**Files:**
- Modify: `README.md` (the Usage code block, and the "A permanent panel outside tmux" section)
- Modify: `CLAUDE.md` (Key Conventions, next to the panel-outside-tmux bullet added on 2026-09-09)

**Interfaces:**
- Consumes: everything from Tasks 1-3.
- Produces: nothing.

- [ ] **Step 1: Add `poke` to the README usage block**

In `README.md`'s Usage fenced block, after the `vigil daemon` entry:

```bash
# Ask a running daemon to rebroadcast its current snapshot, so every client
# re-resolves which tmux session is current. Silent, exits 0 even with no
# daemon, and starts nothing. Meant for a tmux hook.
vigil poke
```

- [ ] **Step 2: Note it in the panel-outside-tmux section**

Append a paragraph to that section:

```markdown
Which session is highlighted is resolved by each client, not by the daemon, and only when a snapshot arrives - so it would otherwise lag a session switch by up to one poll interval. `~/dotfiles` binds tmux's `client-session-changed` hook to `vigil poke`, which makes the daemon rebroadcast the snapshot it already holds. That adds no polling: a poke does no tmux re-read, no git, and no `gh`. A session created or destroyed elsewhere still appears on the normal tick; only the highlight is immediate.
```

- [ ] **Step 3: Add the CLAUDE.md convention bullet**

Insert immediately after the "A panel is supported outside tmux" bullet:

```markdown
- **A poke rebroadcasts; it never polls.** `annotateClientFlags` (`internal/model/client.go:114`) re-resolves `IsCurrent`/`IsLast` from tmux state on every arriving snapshot; `newModel` sets `IsCurrent` once from the cache on startup (`model.go:271`), and that is the only other setter. It runs at two call sites - `listenDaemonCmd` for daemon-fed clients (`client.go:142`) and `collectCmd` for self-polling clients (`client.go:94`), so the poke helps only the daemon-fed path - it rebroadcasts to every connected client, and only they get the re-resolved highlight. `Snapshot` carries no current-session field deliberately, because which session is current belongs to a tmux client and not to the daemon. So the highlight can only move when a snapshot arrives, which is once per `tmux_interval`. `protocol.RequestPoke`, sent by `vigil poke` from `~/dotfiles`' `client-session-changed` hook, makes `handleRequest` fall through to the **unconditional `publishJobs` that was already there** - no new method, and no new poll, git or `gh` work. Three things are load-bearing: the frame carries an **empty ID**, so `jobs.submit`'s guard means a poke against a daemon predating the type registers no refused job rather than one on every switch - **it is not inert against such a daemon**: `handleRequest`'s `default` arm still reaches the unconditional `publishJobs`, so an old daemon rebroadcasts too, verified 2026-09-09 against a live pre-feature binary that answered a poke with a second snapshot carrying the same timestamp and session list (and the daemon never restarts itself, so that skew is normal after `make install`); the poke is handled **on Run's goroutine**, which is the only reason `publishJobs` may reach `broadcast` and touch `s.clients` unguarded; and `publishJobs` **carries `Timestamp` over** deliberately, because a rebroadcast must not make a stalled collector's data look fresh - `Snapshot.Timestamp` currently has no client-side reader, so the carry-over matters only to the daemon's own reasoning. The explicit `case` arm has **no behavioural signature** - deleting it drops the frame into `default`, where `submit` discards the empty ID and the same rebroadcast happens - so it is defended by review, not by a test, like the tickerless remote pollers. A poke adds no polling and no daemon-side tmux re-read, but it does cause each connected client's usual per-snapshot tmux work - two `tmux display-message` calls in `annotateClientFlags`, plus one `tmux capture-pane` for a client with a detail panel open in pane mode. (corrected 2026-09-09, verified against a live pre-feature daemon)
```

- [ ] **Step 4: Verify the suite still passes**

Run: `make test && make lint`
Expected: unchanged - these are docs, but `CLAUDE.md` and `README.md` are large and a broken fence would show in review, so re-read the diff.

- [ ] **Step 5: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: record the poke path and what it deliberately does not refresh"
```

---

### Task 6: The tmux hook, in `~/dotfiles`

**Files:**
- Modify: `~/dotfiles/tmux/.tmux.conf`
- Modify: `~/dotfiles/CLAUDE.md`

**Interfaces:**
- Consumes: `vigil poke` from Task 3.
- Produces: nothing.

**This is a separate repository on its own branch.** Do not commit it together with the vigil changes.

- [ ] **Step 1: Find where hooks are set**

Run: `grep -n 'set-hook' ~/dotfiles/tmux/.tmux.conf`
Place the new hook beside the existing ones. If there are none, put it near the other `set -g` session options with a comment.

- [ ] **Step 2: Add the hook**

```tmux
# Move vigil's current-session highlight the moment a switch lands. Which
# session is current is resolved per client and only when a snapshot arrives,
# so without this the highlight lags by up to one poll interval. `vigil poke`
# makes the daemon rebroadcast the snapshot it already holds - it adds no
# polling and no gh budget.
#
# Fail-soft and backgrounded on purpose: tmux navigation has to keep working
# on a machine with vigil uninstalled, and a switch must never wait on a
# socket write. This belongs here and NOT in tmux-hop, which must never
# reference vigil at all.
set-hook -g client-session-changed 'run-shell -b "vigil poke >/dev/null 2>&1 || true"'
```

- [ ] **Step 3: Verify by hand, with vigil present**

```bash
tmux source-file ~/.tmux.conf
tmux show-hooks -g | grep client-session-changed
```

Then switch sessions with `M-j` and watch the panel's highlight move without a visible delay. Paste the `show-hooks` output into the report and state whether the lag is gone.

- [ ] **Step 4: Verify the uninstalled case**

Confirm the hook is inert without vigil, rather than printing into a pane:

```bash
PATH=/usr/bin:/bin tmux run-shell -b "vigil poke >/dev/null 2>&1 || true"; echo "exit=$?"
```

Expected: `exit=0` and nothing in the pane. Paste it.

- [ ] **Step 5: Run the dotfiles suite**

```bash
cd ~/dotfiles/scripts/scripts && make test && make lint
```

Expected: 422 bats tests pass, shellcheck clean. `.tmux.conf` has no bats coverage, which is why steps 3 and 4 are by hand.

- [ ] **Step 6: Document it**

In `~/dotfiles/CLAUDE.md`, in the `### iTerm2` section after the paragraph about `sessions of <tab>` ordering:

```markdown
**The `client-session-changed` hook is what keeps vigil's highlight in step.**
Which session vigil highlights is resolved by each client and only when a
snapshot arrives, so it otherwise lags a switch by up to one poll interval. The
hook runs `vigil poke`, which makes the daemon rebroadcast the snapshot it
already holds - no extra polling, no `gh`. It is backgrounded with `-b` so a
switch never waits on the socket, and fail-soft so tmux navigation still works
with vigil uninstalled. It lives in `.tmux.conf` and **not** in `tmux-hop`,
which must never reference vigil.
```

- [ ] **Step 7: Commit**

```bash
cd ~/dotfiles
git add tmux/.tmux.conf CLAUDE.md
git commit -m "feat(tmux): poke vigil when the session changes"
```

---

## Final verification

- [ ] `cd ~/vigil && make test && make lint` - all packages ok, 0 issues
- [ ] `cd ~/dotfiles/scripts/scripts && make test && make lint` - 422 bats, shellcheck clean
- [ ] `make install` alone is sufficient here - no need to kill the running daemon first. An old daemon already rebroadcasts on a poke (`handleRequest`'s `default` arm reaches the unconditional `publishJobs`), it just registers no job for the frame; verified 2026-09-09 against a live pre-feature daemon. Switch sessions and confirm the highlight moves immediately. (corrected 2026-09-09, verified against a live pre-feature daemon)
- [ ] Confirm no job line appears in any panel on a session switch - that is the empty-ID contract working
- [ ] Report which tasks' mutation checks were pasted, and flag any that were not

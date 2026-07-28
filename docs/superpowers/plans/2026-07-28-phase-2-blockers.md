# Phase 2 Blockers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the three blockers the phase 2 handoff requires resolved before phase 3, plus trim the review-threads polling query.

**Architecture:** State-transition side effects move to whoever owns the poll loop (the daemon when one is connected, a self-polling client otherwise) behind a shared `internal/transition` package, so detection is not implemented twice. The TUI's self-polling collapses onto `collect.Collector`, so both data paths converge on one `SnapshotMsg` and "both paths render identically" becomes a tautology rather than a convention. The view's column constants are corrected to match what the renderers emit, with tier selection widths frozen at today's values.

**Tech Stack:** Go 1.x, Bubble Tea, lipgloss, `fetch.Commander` for subprocess stubbing. Tests are stdlib `testing`, table-free, no assertion library.

**Spec:** `docs/superpowers/specs/2026-07-27-phase-2-blockers-design.md`

## Global Constraints

- ANSI colors only, no hardcoded hex. Adapts to terminal theme.
- No global mutable state. Config, caches and collaborators are passed explicitly as struct fields.
- `fetch.Commander` is the seam for every subprocess call. Never call `exec` directly in new code.
- `View` is pure. Anything that shells out belongs in `Update` via a `tea.Cmd`.
- `context.Context` for cancellation, threaded from the owner.
- Prefer no code comments. Comment only where meaning cannot be inferred from the code.
- Never use the em dash in code, comments, commit messages or docs. Use a plain dash.
- `make test` runs `go test ./...` under `-race`. Every task must leave it green.
- `make lint` runs `golangci-lint`. Every task must leave it clean.
- Both the daemon path and the self-polling path are permanently supported and must render identically.
- Commit after every task, using Conventional Commits (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`).

---

## File Structure

**Created:**
- `internal/transition/transition.go` - `Event`, `Detector`, `EffectRunner`, `Runner`. Owns transition detection and the side effects a transition triggers. Depends on `session`, `config`, `action`, `fetch`.
- `internal/transition/transition_test.go`

**Modified:**
- `internal/model/model.go` - loses `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd`, `handleTmuxUpdated`, `handleGitUpdated`, `handlePRUpdated`, `gitCache`, `initialPRDone`, `initialLoad`; gains `collector`, `detector`, `effects`; `checkStateTransitions` shrinks to detection plus dispatch.
- `internal/model/client.go` - `annotateClientFlags` extracted from `listenDaemonCmd`; gains `collectCmd`.
- `internal/model/messages.go` - `SnapshotMsg` gains `Local`; three update messages and two tick messages deleted.
- `internal/daemon/daemon.go` - `Server` gains `Detector`, `Effects`, and a `WaitGroup` for effect goroutines; `poll` dispatches transitions.
- `internal/view/layout.go` - `colIndex`, `colState`, the five fixed costs, and five frozen tier thresholds.
- `internal/view/layout_test.go`, `internal/view/table_test.go` - three name-width expectations shift by 1-2.
- `internal/fetch/pr.go` - `reviewThreadsQuery` loses its inner `comments` connection; `fetchReviewThreads` returns only a count; new `FetchReviewComments`.
- `internal/view/detail.go` - review-comments mode renders from passed-in comments rather than `s.PR.ReviewComments`.
- `CLAUDE.md` - architecture notes for `internal/transition` and the collapsed self-polling path.

**Deleted:** nothing wholesale; `internal/model/model.go` shrinks by roughly 200 lines.

---

## Task 1: Extract the per-client annotation helper

Pure refactor with no behaviour change, so the collapse in Task 3 has one definition of "which flags belong to this tmux client" to call.

**Files:**
- Modify: `internal/model/client.go:68-101` (`listenDaemonCmd`)
- Test: `internal/model/client_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func annotateClientFlags(ctx context.Context, cmd fetch.Commander, sessions []*session.Session, fallbackCurrent string)` - mutates `IsCurrent` and `IsLast` in place. Task 2's `collectCmd` calls it.

- [ ] **Step 1: Write the failing test**

Add to `internal/model/client_test.go`:

```go
func TestAnnotateClientFlagsMarksCurrentAndLast(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cmd.OnArgs("tmux list-sessions -F #{session_name}|#{session_last_attached}", "", nil)

	sessions := []*session.Session{{Name: "alpha"}, {Name: "beta"}}
	annotateClientFlags(context.Background(), cmd, sessions, "")

	if sessions[0].IsCurrent {
		t.Error("alpha should not be current")
	}
	if !sessions[1].IsCurrent {
		t.Error("beta should be current")
	}
}

// TestAnnotateClientFlagsBlanksAStaleLast pins the guard that a last-session
// name tmux still remembers, but which is no longer in the snapshot, does not
// mark anything. The fixture names a live current session so the assertion
// cannot pass just because every flag came back false.
func TestAnnotateClientFlagsBlanksAStaleLast(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux": func(_ context.Context, _ string, args []string) (string, error) {
			if len(args) > 0 && args[0] == "display-message" {
				return "alpha", nil
			}
			return "gone|9999999999\nalpha|1", nil
		},
	}

	sessions := []*session.Session{{Name: "alpha"}}
	annotateClientFlags(context.Background(), cmd, sessions, "")

	if !sessions[0].IsCurrent {
		t.Fatal("alpha should be current, so a false IsLast below means something")
	}
	if sessions[0].IsLast {
		t.Error("alpha was marked last, but tmux named a session that is not in the snapshot")
	}
}

func TestAnnotateClientFlagsFallsBackWhenTmuxIsSilent(t *testing.T) {
	cmd := fetch.NewMockCommander()
	sessions := []*session.Session{{Name: "alpha"}}
	annotateClientFlags(context.Background(), cmd, sessions, "alpha")

	if !sessions[0].IsCurrent {
		t.Error("with no answer from tmux, the fallback current name should win")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/ -run TestAnnotateClientFlags -v`
Expected: FAIL, `undefined: annotateClientFlags`.

- [ ] **Step 3: Extract the helper**

In `internal/model/client.go`, add above `listenDaemonCmd`:

```go
// annotateClientFlags fills in the fields that belong to this tmux client
// rather than to the snapshot: which session is current and which was last.
// The daemon serves many clients and cannot know either.
func annotateClientFlags(ctx context.Context, cmd fetch.Commander, sessions []*session.Session, fallbackCurrent string) {
	current := fetch.CurrentSession(ctx, cmd)
	if current == "" {
		current = fallbackCurrent
	}
	last := fetch.LastSession(ctx, cmd)

	names := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		names[s.Name] = true
	}
	if !names[last] {
		last = ""
	}
	for _, s := range sessions {
		s.IsCurrent = s.Name == current
		s.IsLast = s.Name == last
	}
}
```

Add `"github.com/jzinkduda/vigil/internal/session"` to the imports.

- [ ] **Step 4: Call it from `listenDaemonCmd`**

Replace the body of the returned closure in `listenDaemonCmd` (lines 75-100) with:

```go
	return func() tea.Msg {
		snap, err := decoder.Next()
		if err != nil {
			return DaemonLostMsg{Epoch: epoch}
		}
		annotateClientFlags(ctx, cmd, snap.Sessions, fallbackCurrent)
		return SnapshotMsg{Sessions: snap.Sessions, Epoch: epoch}
	}
```

- [ ] **Step 5: Run the full suite**

Run: `make test`
Expected: PASS. The existing `TestListenDaemonEmitsSnapshotMsg` and its neighbours must still pass unchanged - this task adds no behaviour.

- [ ] **Step 6: Mutation-verify the extraction**

Delete the `if !names[last] { last = "" }` line, run `go test ./internal/model/ -run TestAnnotateClientFlagsBlanksAStaleLast`, and confirm it FAILS. Restore with `git checkout -- internal/model/client.go` and re-apply the change, or `git stash`. Do not restore from a `cp` backup: `cp` is aliased to `-i` here and a leftover file of the same name silently wins.

- [ ] **Step 7: Commit**

```bash
git add internal/model/client.go internal/model/client_test.go
git commit -m "refactor(model): extract annotateClientFlags from listenDaemonCmd"
```

---

## Task 2: `collectCmd` and `SnapshotMsg.Local`

Introduces the fallback poll without yet removing the old one, so this task's tests are the only thing exercising it and a failure here cannot be masked by the existing path.

**Files:**
- Modify: `internal/model/client.go`, `internal/model/messages.go`, `internal/model/model.go`
- Test: `internal/model/collect_cmd_test.go` (create)

**Interfaces:**
- Consumes: `annotateClientFlags` (Task 1).
- Produces:
  - `SnapshotMsg{Sessions []*session.Session; Epoch int; Local bool}`
  - `func (m Model) collectCmd() tea.Cmd`
  - `Model.collector *collect.Collector`
  - Task 3 wires `collectCmd` into `Init`, `handleDaemonLost` and `handleProbeResult`.

- [ ] **Step 1: Add the `Local` field and the collector field**

In `internal/model/messages.go`, replace the `SnapshotMsg` declaration:

```go
// SnapshotMsg carries a full session snapshot, with per-client flags already
// resolved. Local says this client collected it itself rather than receiving it
// from a daemon, which is what makes this client the owner of the poll loop and
// therefore responsible for state-transition side effects.
type SnapshotMsg struct {
	Sessions []*session.Session
	Epoch    int
	Local    bool
}
```

In `internal/model/model.go`, add to the `Model` struct next to `cmd`:

```go
	// collector runs the self-polling path. Only collectCmd touches it, and
	// only one collectCmd is ever in flight, which is what keeps its memos
	// single-goroutine: Collector.Snapshot is not safe against reentry.
	collector *collect.Collector
```

In `newModel`, add `collector: collect.New(cfg, cmd),` to the `Model` literal, and add `"github.com/jzinkduda/vigil/internal/collect"` to the imports.

- [ ] **Step 2: Write the failing tests**

Create `internal/model/collect_cmd_test.go`:

```go
package model

import (
	"context"
	"testing"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

func collectFixtureCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|1", nil)
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd
}

func TestCollectCmdEmitsALocalSnapshot(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	msg := m.collectCmd()()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg", msg)
	}
	if !snap.Local {
		t.Error("a self-collected snapshot must be marked Local")
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if !snap.Sessions[0].HasBell {
		t.Error("collect should have carried the bell flag through")
	}
	if !snap.Sessions[0].IsCurrent {
		t.Error("collectCmd must annotate per-client flags, like the daemon path does")
	}
}

// TestCollectCmdEmitsASnapshotWhenTmuxFails is the reschedule hazard. The
// fallback poll self-schedules from its own result, so an outcome that produces
// no message stops polling permanently and silently.
func TestCollectCmdEmitsASnapshotWhenTmuxFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", context.DeadlineExceeded)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	msg := m.collectCmd()()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg even on failure", msg)
	}
	if !snap.Local {
		t.Error("a failed local poll is still a local poll")
	}
	if snap.Sessions != nil {
		t.Errorf("got sessions %+v, want nil so handleSnapshot leaves state alone", snap.Sessions)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/model/ -run TestCollectCmd -v`
Expected: FAIL, `m.collectCmd undefined`.

- [ ] **Step 4: Implement `collectCmd`**

In `internal/model/client.go`, add:

```go
// collectCmd runs one self-polling cycle. It returns a SnapshotMsg on every
// outcome, failures included: handleSnapshot schedules the next poll from this
// message, so an outcome that produced nothing would stop the fallback loop for
// the life of the process. Nil Sessions means the poll failed and handleSnapshot
// must leave the existing sessions alone.
func (m Model) collectCmd() tea.Cmd {
	collector := m.collector
	ctx := m.ctx
	cmd := m.cmd
	fallbackCurrent := m.currentSessionName
	epoch := m.epoch
	return func() tea.Msg {
		sessions, err := collector.Snapshot(ctx)
		if err != nil {
			return SnapshotMsg{Epoch: epoch, Local: true}
		}
		annotateClientFlags(ctx, cmd, sessions, fallbackCurrent)
		return SnapshotMsg{Sessions: sessions, Epoch: epoch, Local: true}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/model/ -run TestCollectCmd -v`
Expected: PASS, both tests.

- [ ] **Step 6: Mutation-verify the failure path**

Change the error branch to `return nil`, run `go test ./internal/model/ -run TestCollectCmdEmitsASnapshotWhenTmuxFails`, and confirm it FAILS with `got <nil>, want SnapshotMsg even on failure`. Restore with `git checkout -- internal/model/client.go` and re-apply.

- [ ] **Step 7: Commit**

```bash
git add internal/model/client.go internal/model/messages.go internal/model/model.go internal/model/collect_cmd_test.go
git commit -m "feat(model): add collectCmd, the self-polling path over collect.Collector"
```

---

## Task 3: Switch the fallback to `collectCmd` and delete the three-tick path

The task the whole collapse exists for. Large deletion; the payoff test is that both paths produce identical sessions.

**Files:**
- Modify: `internal/model/model.go`, `internal/model/messages.go`
- Test: `internal/model/collect_cmd_test.go`, and edits to `internal/model/client_test.go`, `internal/model/epoch_test.go`, `internal/model/reconnect_test.go`, `internal/model/panel_test.go` wherever they reference deleted symbols.

**Interfaces:**
- Consumes: `collectCmd`, `SnapshotMsg.Local` (Task 2).
- Produces: `handleSnapshot` handles both paths and reschedules the local poll. Task 6 changes its `checkStateTransitions` call.

- [ ] **Step 1: Write the failing identical-paths test**

Add to `internal/model/collect_cmd_test.go`:

```go
// TestBothPathsProduceIdenticalSessions is the structural payoff of the
// collapse: "the daemon path and the self-polling path must render identically"
// stops being a convention held up by review and becomes one assertion. It has
// already drifted once.
func TestBothPathsProduceIdenticalSessions(t *testing.T) {
	fixture := func() *fetch.MockCommander {
		cmd := fetch.NewMockCommander()
		cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
			"1700000000|alpha|/tmp/alpha\n1700000001|beta|/tmp/beta", nil)
		cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|1\nbeta|0", nil)
		cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
		cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
			"git rev-parse --show-toplevel": func(_ context.Context, dir string, _ []string) (string, error) {
				return "/repo" + dir, nil
			},
			"git branch --show-current": func(_ context.Context, dir string, _ []string) (string, error) {
				if dir == "/repo/tmp/alpha" {
					return "feature/a", nil
				}
				return "feature/b", nil
			},
		}
		cmd.On("git", "", nil)
		cmd.On("gh", "", nil)
		return cmd
	}

	// The daemon path: collect on the server side, then annotate client-side,
	// which is exactly what daemon.poll plus listenDaemonCmd do.
	serverCmd := fixture()
	served, err := collect.New(&config.Config{}, serverCmd).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("server-side Snapshot: %v", err)
	}
	annotateClientFlags(context.Background(), serverCmd, served, "")

	// The self-polling path.
	localCmd := fixture()
	lm := newTestModel()
	lm.cmd = localCmd
	lm.collector = collect.New(&config.Config{}, localCmd)
	msg, ok := lm.collectCmd()().(SnapshotMsg)
	if !ok {
		t.Fatal("collectCmd did not produce a SnapshotMsg")
	}

	if len(msg.Sessions) != len(served) {
		t.Fatalf("got %d local sessions, want %d from the daemon path", len(msg.Sessions), len(served))
	}
	for i := range served {
		got, want := *msg.Sessions[i], *served[i]
		if got != want {
			t.Errorf("session %d differs between paths:\n local: %+v\ndaemon: %+v", i, got, want)
		}
	}
}
```

Note: `session.Session` contains a `*PRStatus`, so `==` compares pointers. With `gh` stubbed to empty output both paths yield `nil` PRs, which is what makes the struct comparison valid here. If a later change gives the fixture PR data, compare field by field instead.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/model/ -run TestBothPathsProduceIdenticalSessions -v`
Expected: FAIL. Without the rest of this task it may already pass, since `collectCmd` exists; if it passes, that is fine - it is a regression pin for the deletions below, not a driver. Record which.

- [ ] **Step 3: Rewrite `handleSnapshot`**

Replace `internal/model/model.go:783-831` (the whole `handleSnapshot` function) with:

```go
func (m Model) handleSnapshot(msg SnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.epoch {
		return m, nil
	}

	if msg.Local {
		// A failed poll carries no sessions. Reschedule and leave state alone
		// rather than blanking the table.
		if msg.Sessions != nil {
			m.applySnapshot(msg.Sessions)
		}
		cmds := m.checkStateTransitions()
		if m.detailOpen {
			cmds = append(cmds, m.refreshDetailCmd())
		}
		cmds = append(cmds, m.collectCmd())
		return m, tea.Batch(cmds...)
	}

	if m.daemonConn == nil && m.daemonDecoder == nil {
		return m, nil
	}
	if !m.daemonReady {
		if m.daemonConn != nil {
			_ = m.daemonConn.SetReadDeadline(time.Time{})
		}
		m.daemonReady = true
	}
	m.lastSnapshot = time.Now()
	m.applySnapshot(msg.Sessions)

	cmds := m.checkStateTransitions()
	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}
	cmds = append(cmds, listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch))
	return m, tea.Batch(cmds...)
}

// applySnapshot installs a set of sessions and everything derived from them.
// Both paths funnel through here so neither can grow its own idea of what a
// snapshot implies.
func (m *Model) applySnapshot(sessions []*session.Session) {
	m.sessions = sessions
	m.warmCaches()
	session.SortSessions(m.sessions, m.sortMode)
	m.placeCursor()

	snap := make([]*session.Session, len(m.sessions))
	copy(snap, m.sessions)
	go func() { _ = cache.Save(cache.CachePath(), snap) }()
}
```

The `initialPRDone` block from the old `handleSnapshot` goes away with the field itself in Step 5.

Cache writes now happen on both paths. The daemon also writes the same cache from its own poll, so a daemon-fed client duplicates a write of identical bytes; `cache.Save` writes through a temp file and renames, so the loser of a race leaves correct content either way. Accept it rather than gating, so the two paths keep the same code.

- [ ] **Step 4: Rewire `Init`, `handleDaemonLost` and `handleProbeResult`**

In `Init`, replace the self-polling branch (lines 228-238) with:

```go
	cmds = append(cmds, m.collectCmd(), probeTickCmd(m.epoch))
	return tea.Batch(cmds...)
```

In `handleDaemonLost`, replace the returned batch (lines 852-859) with:

```go
	return m, tea.Batch(m.collectCmd(), probeTickCmd(m.epoch))
```

`handleProbeResult` needs no change: bumping the epoch retires the in-flight `collectCmd`, whose `SnapshotMsg` is then dropped by the epoch guard, exactly as it retired the old ticks.

- [ ] **Step 5: Delete the three-tick path**

From `internal/model/model.go` delete:
- `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd`
- `handleTmuxUpdated`, `handleGitUpdated`, `handlePRUpdated`
- `tmuxTickCmd`, `gitTickCmd`, `prTickCmd`
- the `TmuxTickMsg`, `GitTickMsg`, `PRTickMsg`, `TmuxUpdatedMsg`, `GitUpdatedMsg`, `PRUpdatedMsg` cases from the `Update` switch
- the `gitCache`, `initialPRDone` and `initialLoad` struct fields and their initialisers in `newModel`
- the `m.gitCache[s.Name] = s.Git` line from `warmCaches`

From `internal/model/messages.go` delete `TmuxTickMsg`, `GitTickMsg`, `PRTickMsg`, `TmuxUpdatedMsg`, `GitUpdatedMsg`, `PRUpdatedMsg`.

`RenderTickMsg` and `renderTickCmd` stay - the daemon path still needs a repaint heartbeat.

`initialLoad` is removed here but the `if m.initialLoad` guards inside `checkStateTransitions` are not replaced until Task 6. For this task, delete those two guard blocks and accept that a first snapshot will emit a notification per session; Task 6's `Detector` restores the suppression. Note it in the commit message so a bisect lands on the right explanation.

- [ ] **Step 6: Fix the compile fallout in the tests**

Run: `go build ./... && go vet ./...`

Then `go test ./internal/model/`. Expect failures in tests that reference deleted symbols. For each one, decide deliberately:
- A test whose subject was `handleTmuxUpdated`/`handleGitUpdated`/`handlePRUpdated` merge behaviour is testing code that no longer exists. Delete it, and check whether the property it protected is covered by `TestBothPathsProduceIdenticalSessions` or by `collect`'s own tests. If it is not, say so in the commit message rather than silently dropping coverage.
- A test that used a tick message only as a way to drive the model forward should be rewritten to send `SnapshotMsg{Local: true}`.
- `internal/model/epoch_test.go` asserts that retired generations retire. Its subject still exists; update it to drive `collectCmd`/`SnapshotMsg` instead of the deleted ticks. Do not delete it.

- [ ] **Step 7: Run the full suite**

Run: `make test && make lint`
Expected: PASS and clean.

- [ ] **Step 8: Pin the reschedule**

Add to `internal/model/collect_cmd_test.go`:

```go
// collectedAgain walks a command tree, invoking each command, and reports
// whether any produced a local SnapshotMsg. That is the fallback loop
// rescheduling itself, which nothing else drives: there is no ticker.
func collectedAgain(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case SnapshotMsg:
		return msg.Local
	case tea.BatchMsg:
		for _, c := range msg {
			if collectedAgain(c) {
				return true
			}
		}
	}
	return false
}

func TestLocalSnapshotSchedulesTheNextPoll(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	_, next := m.handleSnapshot(SnapshotMsg{Sessions: fixtureSessions(), Local: true})

	if !collectedAgain(next) {
		t.Fatal("a local snapshot scheduled no further poll, so the fallback loop is dead")
	}
}

// TestAFailedLocalPollStillSchedulesTheNextOne is the same property on the
// branch that actually threatens it. A poll that errored carries no sessions,
// and if that branch forgets to reschedule the client goes quiet for the life
// of the process with no indication.
func TestAFailedLocalPollStillSchedulesTheNextOne(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.sessions = fixtureSessions()

	updated, next := m.handleSnapshot(SnapshotMsg{Local: true})

	if !collectedAgain(next) {
		t.Fatal("a failed local poll scheduled no further poll")
	}
	if got := updated.(Model).sessions; len(got) != 1 {
		t.Errorf("a failed poll blanked the session list: %+v", got)
	}
}
```

Add `tea "github.com/charmbracelet/bubbletea"` to the file's imports.

Run: `go test ./internal/model/ -run 'TestLocalSnapshot|TestAFailedLocalPoll' -v`
Expected: PASS.

- [ ] **Step 8b: Mutation-verify the reschedule**

| Mutation | Must fail |
|---|---|
| Remove `cmds = append(cmds, m.collectCmd())` from the `msg.Local` branch | both tests above |
| Change `if msg.Sessions != nil` to unconditional `m.applySnapshot(msg.Sessions)` | `TestAFailedLocalPollStillSchedulesTheNextOne` on the blanking assertion |

Restore with `git checkout -- internal/model/model.go` and re-apply.

- [ ] **Step 9: Commit**

```bash
git add internal/model/ 
git commit -m "refactor(model): collapse self-polling onto collect.Collector

Both data paths now converge on SnapshotMsg and applySnapshot, so the
daemon path and the fallback cannot drift. Deletes the three independent
tmux/git/PR poll cycles, their messages and their handlers.

First-snapshot notification suppression is temporarily gone with the
initialLoad flag; transition.Detector restores it in the next commit."
```

---

## Task 4: `transition.Detector`

**Files:**
- Create: `internal/transition/transition.go`, `internal/transition/transition_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Event struct { Session, PanePath, Branch, GitRoot string; Old, New session.SessionState }`
  - `func NewDetector() *Detector`
  - `func (d *Detector) Detect(sessions []*session.Session) []Event`
  - Task 5 adds `Runner` to this package. Tasks 6 and 7 consume both.

- [ ] **Step 1: Write the failing tests**

Create `internal/transition/transition_test.go`:

```go
package transition

import (
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

// idle and attention build sessions whose State() is unambiguous: HasBell is
// the first branch State() takes, and a nil PR is the second.
func idle(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp/" + name}
}

func attention(name string) *session.Session {
	s := idle(name)
	s.HasBell = true
	return s
}

func TestDetectPrimesSilentlyOnTheFirstCall(t *testing.T) {
	d := NewDetector()
	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events on the priming call, want 0", len(events))
	}
}

func TestDetectReportsOneEventPerChange(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha"), idle("beta")})

	events := d.Detect([]*session.Session{attention("alpha"), idle("beta")})

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Session != "alpha" {
		t.Errorf("got session %q, want alpha", ev.Session)
	}
	if ev.Old != session.Idle || ev.New != session.Attention {
		t.Errorf("got %v -> %v, want idle -> attention", ev.Old, ev.New)
	}
	if ev.PanePath != "/tmp/alpha" {
		t.Errorf("got pane path %q, want /tmp/alpha", ev.PanePath)
	}
}

func TestDetectIsSilentWhenNothingChanged(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{attention("alpha")})
	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events for an unchanged session, want 0", len(events))
	}
}

func TestDetectPrimesANewSessionRatherThanFiring(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha")})
	if events := d.Detect([]*session.Session{idle("alpha"), attention("beta")}); len(events) != 0 {
		t.Fatalf("got %+v, want nothing for a session seen for the first time", events)
	}
}

// TestDetectPrunesVanishedSessions is why prev is replaced rather than updated.
// Without the prune, a session that goes away and comes back in a different
// state fires an event describing a transition that never happened.
func TestDetectPrunesVanishedSessions(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha")})
	d.Detect(nil)

	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %+v, want nothing: alpha vanished, so its return is a first sighting", events)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/transition/ -v`
Expected: FAIL, the package does not exist.

- [ ] **Step 3: Implement `Detector`**

Create `internal/transition/transition.go`:

```go
// Package transition detects session state changes and runs the side effects
// they trigger. Both the daemon and the TUI need this, and one of them is
// always the owner of the poll loop, so it lives here rather than in either.
package transition

import (
	"github.com/jzinkduda/vigil/internal/session"
)

// Event is one session changing state. It carries only what the daemon can
// know: nothing per-tmux-client, so the same Event means the same thing
// wherever it is handled.
type Event struct {
	Session  string
	PanePath string
	Branch   string
	GitRoot  string
	Old, New session.SessionState
}

type Detector struct {
	prev   map[string]session.SessionState
	primed bool
}

func NewDetector() *Detector {
	return &Detector{prev: make(map[string]session.SessionState)}
}

// Detect returns one Event per session whose state changed since the previous
// call. The first call primes and returns nothing, so starting up is not a
// storm of transitions. A session absent from sessions is forgotten, which
// makes its eventual return a first sighting rather than a transition from
// whatever it was before it vanished.
func (d *Detector) Detect(sessions []*session.Session) []Event {
	next := make(map[string]session.SessionState, len(sessions))
	var events []Event
	for _, s := range sessions {
		state := s.State()
		next[s.Name] = state
		if !d.primed {
			continue
		}
		old, seen := d.prev[s.Name]
		if !seen || old == state {
			continue
		}
		events = append(events, Event{
			Session:  s.Name,
			PanePath: s.PanePath,
			Branch:   s.Git.Branch,
			GitRoot:  s.Git.GitRoot,
			Old:      old,
			New:      state,
		})
	}
	d.prev = next
	d.primed = true
	return events
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/transition/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Mutation-verify each guard**

Run each of these, confirm the named test fails, then `git checkout -- internal/transition/transition.go`:

| Mutation | Must fail |
|---|---|
| Delete `if !d.primed { continue }` | `TestDetectPrimesSilentlyOnTheFirstCall` |
| Change `if !seen \|\| old == state` to `if old == state` | `TestDetectPrimesANewSessionRatherThanFiring` |
| Change `d.prev = next` to a loop that merges `next` into `d.prev` | `TestDetectPrunesVanishedSessions` |

If any mutation leaves the suite green, the test does not reach its subject. Fix the test, not the table.

- [ ] **Step 6: Commit**

```bash
git add internal/transition/
git commit -m "feat(transition): add Detector, shared state-change detection"
```

---

## Task 5: `transition.Runner`

**Files:**
- Modify: `internal/transition/transition.go`, `internal/transition/transition_test.go`

**Interfaces:**
- Consumes: `Event` (Task 4).
- Produces:
  - `type EffectRunner interface { Run(ctx context.Context, ev Event) }`
  - `type Runner struct { Cfg *config.Config; Cmd fetch.Commander; Logf func(format string, args ...any) }`
  - `func (r Runner) Run(ctx context.Context, ev Event)`
  - Tasks 6 and 7 hold an `EffectRunner` and call `Run`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/transition/transition_test.go`:

```go
func doneSession(name string) *session.Session {
	s := idle(name)
	s.PR = &session.PRStatus{Number: 7, State: "MERGED"}
	return s
}

func doneEvent(name string) Event {
	return Event{Session: name, PanePath: "/tmp/" + name, Old: session.Review, New: session.Done}
}

// TestRunSkipsCleanupForTheCurrentSession is the guard that replaces the
// model's !s.IsCurrent check. The daemon never annotates IsCurrent, so an
// Event cannot carry it and Run has to ask tmux itself. The fixture enables
// auto_cleanup and names a Done session, so cleanup is the only thing that
// could run: if the guard is gone, tmux kill-session shows up in the calls.
func TestRunSkipsCleanupForTheCurrentSession(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up the session the user is sitting in")
		}
	}
}

func TestRunCleansUpADoneSessionThatIsNotCurrent(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	var killed bool
	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no kill-session in %+v", cmd.Calls)
	}
}

func TestRunSkipsCleanupWhenDisabled(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{Settings: map[string]any{"notifications_enabled": "false"}}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up with auto_cleanup at its default of false")
		}
	}
}

func TestRunSkipsCleanupForANonDoneTransition(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	ev := Event{Session: "alpha", PanePath: "/tmp/alpha", Old: session.Idle, New: session.Blocked}
	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), ev)

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up a session that only went blocked")
		}
	}
}

// TestRunFiresTheNotifyHook uses a hook that writes to a file, because
// config.RunHook shells out through exec rather than through fetch.Commander
// and is not otherwise observable.
func TestRunFiresTheNotifyHook(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "true"},
		Hooks:    map[string]any{"notify": "echo {new_state} > " + marker},
	}

	Runner{Cfg: cfg, Cmd: fetch.NewMockCommander()}.Run(context.Background(), doneEvent("alpha"))

	out, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "done" {
		t.Errorf("got %q, want done: the hook should receive the new state", got)
	}
}

func TestRunSkipsTheHookWhenNotificationsAreOff(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fired")
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "false"},
		Hooks:    map[string]any{"notify": "echo x > " + marker},
	}

	Runner{Cfg: cfg, Cmd: fetch.NewMockCommander()}.Run(context.Background(), doneEvent("alpha"))

	if _, err := os.Stat(marker); err == nil {
		t.Error("the hook ran with notifications disabled")
	}
}
```

Add `"context"`, `"os"`, `"path/filepath"`, `"strings"`, `"github.com/jzinkduda/vigil/internal/config"` and `"github.com/jzinkduda/vigil/internal/fetch"` to the test imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/transition/ -run TestRun -v`
Expected: FAIL, `undefined: Runner`.

- [ ] **Step 3: Implement `Runner`**

Append to `internal/transition/transition.go`:

```go
const (
	hookTimeout    = 5 * time.Second
	cleanupTimeout = 60 * time.Second
)

// EffectRunner is the seam. config.RunHook shells out through exec rather than
// through fetch.Commander, so an interface is what lets a caller assert that an
// effect fired exactly once.
type EffectRunner interface {
	Run(ctx context.Context, ev Event)
}

// Runner performs the side effects of one transition. Exactly one process runs
// these per event: the daemon when clients are attached to one, a self-polling
// client otherwise. Logf is where failures go, because the daemon has no screen.
type Runner struct {
	Cfg  *config.Config
	Cmd  fetch.Commander
	Logf func(format string, args ...any)
}

func (r Runner) Run(ctx context.Context, ev Event) {
	if r.Cfg.GetSettingBool("notifications_enabled") {
		out, err := r.Cfg.RunHook("notify", map[string]string{
			"session":   ev.Session,
			"old_state": ev.Old.String(),
			"new_state": ev.New.String(),
		}, "", hookTimeout)
		if err != nil && !errors.As(err, new(*config.HookNotConfigured)) {
			r.logf("notify hook for %s: %v (output: %s)", ev.Session, err, out)
		}
	}

	if !r.Cfg.GetSettingBool("auto_cleanup") || ev.New != session.Done {
		return
	}
	// Not from a field on Event: the daemon does not annotate IsCurrent, so it
	// would read false and clean up the session the user is sitting in.
	if fetch.CurrentSession(ctx, r.Cmd) == ev.Session {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	out, err := action.CleanupSession(cctx, r.Cfg, r.Cmd, ev.Session, ev.PanePath, ev.Branch, ev.GitRoot)
	if err != nil {
		r.logf("auto-cleanup of %s failed: %v (output: %s)", ev.Session, err, out)
	}
}

func (r Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
```

Extend the imports to `"context"`, `"errors"`, `"time"`, `"github.com/jzinkduda/vigil/internal/action"`, `"github.com/jzinkduda/vigil/internal/config"`, `"github.com/jzinkduda/vigil/internal/fetch"`, `"github.com/jzinkduda/vigil/internal/session"`.

`action` imports only `config` and `fetch`, so there is no cycle.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/transition/ -v`
Expected: PASS, eleven tests.

- [ ] **Step 5: Mutation-verify the guards**

| Mutation | Must fail |
|---|---|
| Delete the `fetch.CurrentSession` check | `TestRunSkipsCleanupForTheCurrentSession` |
| Change `ev.New != session.Done` to `false` | `TestRunSkipsCleanupForANonDoneTransition` |
| Delete the `notifications_enabled` check around the hook | `TestRunSkipsTheHookWhenNotificationsAreOff` |
| Change `"new_state"` to `"newstate"` in the hook vars | `TestRunFiresTheNotifyHook` |

- [ ] **Step 6: Commit**

```bash
git add internal/transition/
git commit -m "feat(transition): add Runner, the per-event side effects

Resolves the current session through fetch.CurrentSession rather than an
Event field: the daemon does not annotate IsCurrent, so a field would read
false and clean up the session the user is sitting in."
```

---

## Task 6: Wire the model to `Detector` and `Runner`

**Files:**
- Modify: `internal/model/model.go`
- Test: `internal/model/transition_test.go` (create)

**Interfaces:**
- Consumes: `transition.NewDetector`, `transition.EffectRunner`, `transition.Runner`, `SnapshotMsg.Local`.
- Produces: `Model.detector`, `Model.effects`, `checkStateTransitions(local bool) []tea.Cmd`. Task 7 uses the same types on the daemon side.

- [ ] **Step 1: Write the failing tests**

Create `internal/model/transition_test.go`:

```go
package model

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/transition"
)

type countingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (c *countingEffects) Run(_ context.Context, ev transition.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *countingEffects) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// drain runs every command a batch produced, so effects dispatched as tea.Cmds
// have actually executed by the time the assertion reads the counter.
func drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(c)
		}
	}
}

func idleSession(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp/" + name}
}

func bellSession(name string) *session.Session {
	s := idleSession(name)
	s.HasBell = true
	return s
}

func transitionModel(effects transition.EffectRunner) Model {
	m := newTestModel()
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "true"}}
	m.detector = transition.NewDetector()
	m.effects = effects
	return m
}

// TestDaemonFedClientsToastButDoNotRunEffects is the blocker, asserted
// directly. Two panels attached to one daemon must produce two toasts, because
// each has its own screen, and zero side effects, because the daemon owns them.
func TestDaemonFedClientsToastButDoNotRunEffects(t *testing.T) {
	effects := &countingEffects{}
	a := transitionModel(effects)
	b := transitionModel(effects)

	for _, m := range []*Model{&a, &b} {
		m.sessions = []*session.Session{idleSession("alpha")}
		m.checkStateTransitions(false)
		m.sessions = []*session.Session{bellSession("alpha")}
		cmds := m.checkStateTransitions(false)
		drain(tea.Batch(cmds...))
	}

	if got := len(a.notifications); got != 1 {
		t.Errorf("client A: got %d notifications, want 1", got)
	}
	if got := len(b.notifications); got != 1 {
		t.Errorf("client B: got %d notifications, want 1", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs across two daemon-fed clients, want 0", got)
	}
}

// TestASelfPollingClientRunsEffectsOnce is the other half: with no daemon this
// client owns the loop, so it must run them, exactly once.
func TestASelfPollingClientRunsEffectsOnce(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{bellSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1", got)
	}
	ev := effects.events[0]
	if ev.Session != "alpha" || ev.Old != session.Idle || ev.New != session.Attention {
		t.Errorf("got %+v, want alpha idle -> attention", ev)
	}
}

func TestTheFirstSnapshotDoesNotToast(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)
	m.sessions = []*session.Session{bellSession("alpha"), idleSession("beta")}

	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications on the priming snapshot, want 0", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs on the priming snapshot, want 0", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/model/ -run 'TestDaemonFedClients|TestASelfPolling|TestTheFirstSnapshot' -v`
Expected: FAIL, `m.detector undefined`.

- [ ] **Step 3: Replace the model's transition state**

In `internal/model/model.go`, delete the `prevStates` field and add next to `cfg`:

```go
	detector *transition.Detector
	effects  transition.EffectRunner
```

In `newModel`, delete `prevStates: make(map[string]session.SessionState),` and add:

```go
		detector: transition.NewDetector(),
		effects:  transition.Runner{Cfg: cfg, Cmd: cmd},
```

Add `"github.com/jzinkduda/vigil/internal/transition"` to the imports. Drop `"github.com/jzinkduda/vigil/internal/action"` if nothing else in the file still uses it - `go build` will say.

- [ ] **Step 4: Rewrite `checkStateTransitions`**

Replace the whole function (`internal/model/model.go:1389-1453` before this task's edits) with:

```go
// checkStateTransitions renders the transitions in the current snapshot and,
// when this client owns the poll loop, runs their side effects. Toasts and
// auto-focus are per-client on purpose: each panel has its own screen and its
// own cursor. Hooks and cleanups are not, which is why local gates them.
func (m *Model) checkStateTransitions(local bool) []tea.Cmd {
	events := m.detector.Detect(m.sessions)
	notify := m.cfg.GetSettingBool("notifications_enabled")

	var cmds []tea.Cmd
	for _, ev := range events {
		if notify {
			m.addNotification(fmt.Sprintf("%s → %s", ev.Session, ev.New), notifSeverity(ev.New))
		}
		if local {
			ev := ev
			effects, ctx := m.effects, m.ctx
			cmds = append(cmds, func() tea.Msg {
				effects.Run(ctx, ev)
				return nil
			})
		}
	}

	if !m.insideTmux && m.cfg.GetSettingBool("auto_focus") && time.Since(m.lastManualNav) > autoFocusCooldown {
		m.maybeAutoFocus()
	}

	return cmds
}
```

The old function returned an `ActionResultMsg` toast for auto-cleanup. That is gone: cleanup now runs in whichever process owns the loop and reports to a log, per the spec. A successful cleanup is still visible, because the session leaves the next snapshot.

- [ ] **Step 5: Update the two call sites**

Task 3 left both calls in `handleSnapshot` as `m.checkStateTransitions()`, because the parameter did not exist yet. Give them their argument now: the call in the `msg.Local` branch becomes `m.checkStateTransitions(true)`, and the one on the daemon branch becomes `m.checkStateTransitions(false)`.

Run: `grep -n 'checkStateTransitions' internal/model/*.go`
Expected: the definition plus exactly two calls, both in `handleSnapshot`, one `true` and one `false`. If a third call site appears, it is a merge artefact - the old handlers that called it were deleted in Task 3.

- [ ] **Step 6: Fix test fallout**

`internal/model/client_test.go:25` initialises `prevStates` in `newTestModel`. Replace it with `detector: transition.NewDetector()` and `effects: transition.Runner{Cfg: &config.Config{}, Cmd: fetch.NewMockCommander()}`. Any test that reached into `prevStates` is testing a field that no longer exists; rewrite it against `Detect` in `internal/transition` or delete it, and say which in the commit message.

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Mutation-verify the ownership split**

| Mutation | Must fail |
|---|---|
| Change `if local` to `if true` | `TestDaemonFedClientsToastButDoNotRunEffects` |
| Change `if local` to `if false` | `TestASelfPollingClientRunsEffectsOnce` |
| Move the `addNotification` call inside `if local` | `TestDaemonFedClientsToastButDoNotRunEffects` |

- [ ] **Step 8: Commit**

```bash
git add internal/model/
git commit -m "fix(model): run transition side effects only when this client polls

A session going blocked fired the notify hook once per attached panel, and
with auto_cleanup on it ran git worktree remove and tmux kill-session once
per panel, concurrently, against one worktree. Toasts stay per-client;
hooks and cleanups belong to whoever owns the poll loop.

Also restores first-snapshot notification suppression, which the collapse
commit dropped along with the initialLoad flag."
```

---

## Task 7: Wire the daemon to `Detector` and `Runner`

**Files:**
- Modify: `internal/daemon/daemon.go`
- Test: `internal/daemon/transition_test.go` (create)

**Interfaces:**
- Consumes: `transition.NewDetector`, `transition.EffectRunner`, `transition.Runner`.
- Produces: `Server.Detector`, `Server.Effects`. Nothing later depends on these.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/transition_test.go`:

```go
package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/transition"
)

type recordingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (r *recordingEffects) Run(_ context.Context, ev transition.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEffects) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// bellSwitch returns a Commander whose bell flag flips the second time tmux is
// asked, which is a state change from idle to attention.
func bellSwitch() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	var windowCalls int
	var mu sync.Mutex
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux": func(_ context.Context, _ string, args []string) (string, error) {
			if len(args) > 1 && args[1] == "-a" && args[0] == "list-panes" {
				return "1700000000|alpha|/tmp/alpha", nil
			}
			if len(args) > 0 && args[0] == "list-windows" {
				mu.Lock()
				windowCalls++
				n := windowCalls
				mu.Unlock()
				if n == 1 {
					return "alpha|0", nil
				}
				return "alpha|1", nil
			}
			return "", nil
		},
	}
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd
}

// TestPollRunsEffectsOncePerEvent is the point of moving them here: the count
// is a property of the event, not of how many clients happen to be attached.
func TestPollRunsEffectsOncePerEvent(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx)
	if got := effects.count(); got != 0 {
		t.Fatalf("got %d effect runs on the priming poll, want 0", got)
	}
	s.poll(ctx)
	s.effects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs for one transition, want 1", got)
	}
	if ev := effects.events[0]; ev.Session != "alpha" {
		t.Errorf("got session %q, want alpha", ev.Session)
	}
}

func TestPollWithoutADetectorDoesNotPanic(t *testing.T) {
	s := &Server{Collector: collect.New(&config.Config{}, bellSwitch())}
	s.poll(context.Background())
}
```

`s.effects` is the `WaitGroup` added in Step 3; the test reaches it because it is in the same package. It exists so `-race` cannot see an effect goroutine outlive the test.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/daemon/ -run TestPoll -v`
Expected: FAIL, `unknown field Detector`.

- [ ] **Step 3: Add the fields and dispatch**

In `internal/daemon/daemon.go`, add to `Server`:

```go
	// Detector and Effects fire state-transition side effects once per event.
	// Clients render their own toasts from their own detectors; only this
	// process runs the hooks and the cleanups, because only this process has
	// one view of state. Nil disables them, which is what a zero-valued Server
	// in a test gets.
	Detector *transition.Detector
	Effects  transition.EffectRunner

	// effects tracks in-flight effect goroutines so shutdown waits for them.
	effects sync.WaitGroup
```

In `New`, set:

```go
		Detector: transition.NewDetector(),
		Effects: transition.Runner{
			Cfg:  cfg,
			Cmd:  cmd,
			Logf: log.New(os.Stderr, "vigil: ", log.LstdFlags).Printf,
		},
```

Reuse the same logger the `Log` field gets rather than building a second one: assign the logger to a local in `New` and use it for both.

At the end of `poll`, after the `cache.Save` call:

```go
	if s.Detector == nil || s.Effects == nil {
		return
	}
	for _, ev := range s.Detector.Detect(sessions) {
		ev := ev
		s.effects.Add(1)
		go func() {
			defer s.effects.Done()
			s.Effects.Run(ctx, ev)
		}()
	}
```

One goroutine per event, so a `notify` hook that hangs for its full 5s cannot delay the next tick.

In `Run`, in the `<-ctx.Done()` branch, wait for them before returning:

```go
		case <-ctx.Done():
			_ = listener.Close()
			accepted.Wait()
			s.closeClients()
			s.effects.Wait()
			return nil
```

Add `"github.com/jzinkduda/vigil/internal/transition"` to the imports.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -v`
Expected: PASS, including the existing daemon, client and lock tests.

- [ ] **Step 5: Verify the N-clients property end to end**

Add to `internal/daemon/transition_test.go`:

```go
// TestEffectsDoNotScaleWithClients pins the property phase 3 depends on: three
// panels attached to one daemon still produce one effect run per event.
func TestEffectsDoNotScaleWithClients(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	for i := 0; i < 3; i++ {
		local, remote := net.Pipe()
		t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
		s.addClient(local)
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := remote.Read(buf); err != nil {
					return
				}
			}
		}()
	}

	ctx := context.Background()
	s.poll(ctx)
	s.poll(ctx)
	s.effects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs with three clients attached, want 1", got)
	}
	s.closeClients()
}
```

Add `"net"` to the test imports. Check `addClient`'s signature takes a `net.Conn`; if the existing `internal/daemon/client_test.go` has a helper that wires a readable client, use that instead of `net.Pipe` by hand.

Run: `go test ./internal/daemon/ -run TestEffectsDoNotScaleWithClients -v`
Expected: PASS.

- [ ] **Step 6: Mutation-verify**

| Mutation | Must fail |
|---|---|
| Move the `Detect` loop inside `broadcast`, once per client | `TestEffectsDoNotScaleWithClients` |
| Delete the `s.Detector == nil` guard | `TestPollWithoutADetectorDoesNotPanic` |
| Delete `s.effects.Wait()` from `Run` | run `go test ./internal/daemon/ -race` and look for a goroutine leak or a race report |

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): fire transition side effects once per event

The daemon owns one view of state, so hooks and auto_cleanup run here and
the count no longer scales with the number of attached panels."
```

---

## Task 8: Correct the layout constants, freeze the thresholds

**Files:**
- Modify: `internal/view/layout.go:9-27`, `internal/view/layout.go:72-113`
- Test: `internal/view/layout_test.go`, `internal/view/table_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: no signature changes. `TableLayout.Total()` becomes exact.

- [ ] **Step 1: Write the failing tests**

Add to `internal/view/layout_test.go`:

```go
// TestTotalMatchesWhatTheRowActuallyRenders is the constraint that was missing.
// The constants drifted from the renderers because nothing compared them, and
// Total() <= width passes happily while rows come out narrower than budgeted.
func TestTotalMatchesWhatTheRowActuallyRenders(t *testing.T) {
	s := &session.Session{
		Name: "a-session-with-a-long-name",
		Git:  session.GitStatus{Branch: "feature/x", Modified: 3, Added: 1},
		PR:   &session.PRStatus{Number: 1234, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"},
	}
	for _, width := range []int{200, 104, 80, 60, 41, 40, 28, 20, 15, 8, 4, 1} {
		layout := LayoutForWidth(width)
		row := renderRow(s, 3, false, 86400, width, false, layout)
		if got := VisibleWidth(row); got != layout.Total() {
			t.Errorf("width %d: row renders %d columns, Total() claims %d", width, got, layout.Total())
		}
	}
}

// TestFrozenThresholdsAdmitAUsefulName stops a future edit to a fixed cost from
// silently producing a name below nameMin at a tier boundary. The thresholds are
// tuned, not derived, so nothing else checks them against the costs.
func TestFrozenThresholdsAdmitAUsefulName(t *testing.T) {
	for _, tier := range []struct {
		name           string
		threshold, fix int
	}{
		{"full", tierFull, fullFixed},
		{"noGit", tierNoGit, noGitFixed},
		{"compact", tierCompact, compactFixed},
		{"noPR", tierNoPR, noPRFixed},
	} {
		if got := tier.threshold - tier.fix; got < nameMin {
			t.Errorf("%s: threshold %d leaves %d columns of name, want at least %d",
				tier.name, tier.threshold, got, nameMin)
		}
	}
}

// TestPanelWidthStillPicksTheCompactTier pins the layout the phase 2 resize
// verification was run at. Deriving the thresholds from the corrected costs
// moves 40 onto the noGit tier, where it gets a 9-column name beside a full PR
// column instead of 20 columns of name and a compact one.
func TestPanelWidthStillPicksTheCompactTier(t *testing.T) {
	l := LayoutForWidth(40)
	if l.Index {
		t.Error("width 40 kept the index column, so it is not on the compact tier")
	}
	if l.PR != colPRCompact {
		t.Errorf("got PR %d, want the compact %d", l.PR, colPRCompact)
	}
	if l.Name < 20 {
		t.Errorf("got name %d, want at least the 20 columns it has today", l.Name)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/view/ -run 'TestTotalMatches|TestFrozenThresholds|TestPanelWidth' -v`
Expected: `TestTotalMatchesWhatTheRowActuallyRenders` FAILS with a 2-column gap at the wide tiers; `TestFrozenThresholdsAdmitAUsefulName` FAILS to compile (`undefined: tierFull`).

- [ ] **Step 3: Correct the constants**

In `internal/view/layout.go`, change:

```go
	colIndicator = 3
	colIndex     = 1
	colState     = 1
```

- [ ] **Step 4: Name the fixed costs and freeze the thresholds**

Replace the `const` block inside `LayoutForWidth` and the `switch` (lines 72-113) with:

```go
// fixed is every column but the name, including every separator: one between
// each pair, so the name contributes a separator on both sides when a column
// follows it.
const (
	fullFixed    = colIndicator + sep + colIndex + sep + colState + sep + sep + colGit + sep + colPR // 50
	noGitFixed   = colIndicator + sep + colIndex + sep + colState + sep + sep + colPR                // 31
	compactFixed = colIndicator + sep + colState + sep + sep + colPRCompact                          // 19
	noPRFixed    = colIndicator + sep + colState + sep                                               // 6
	bareFixed    = colState + sep                                                                    // 2
)

// The width at which each tier is chosen. Tuned, not derived: fixed+nameMin
// would move tierNoGit to 39, and width 40 - the landscape panel's default -
// would stop choosing the compact tier, taking a 9-column name beside a full PR
// column in exchange for 20 columns of name and a compact one. Every value here
// is the width that tier is chosen at today, so every layout verified on a real
// pane stays verified. TestFrozenThresholdsAdmitAUsefulName keeps them honest
// against the costs above.
const (
	tierFull    = 60
	tierNoGit   = 41
	tierCompact = 28
	tierNoPR    = 15
	tierBare    = 4
)

func LayoutForWidth(width int) TableLayout {
	switch {
	case width >= tierFull:
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-fullFixed, nameMin, colName),
			Git:  colGit, PR: colPR,
		}
	case width >= tierNoGit:
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-noGitFixed, nameMin, colName),
			PR:   colPR,
		}
	case width >= tierCompact:
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-compactFixed, nameMin, colName),
			PR:   colPRCompact,
		}
	case width >= tierNoPR:
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-noPRFixed, nameMin, colName),
		}
	case width >= tierBare:
		return TableLayout{State: true, Name: clamp(width-bareFixed, 1, colName)}
	default:
		return TableLayout{Name: clamp(width, 1, colName)}
	}
}
```

Move the `const` blocks to package scope, above `LayoutForWidth`, so the test can read `tierFull` and `fullFixed`.

- [ ] **Step 5: Update the three shifted expectations**

The name column gains 2 columns at the two widest tiers and 1 at the rest. In `internal/view/layout_test.go`:
- `TestLayoutShrinksNameBeforeDroppingColumns`: `l.Name != 28` becomes `l.Name != 30`, and the message becomes `want 30 (80 - 50)`.
- `TestLayoutDropsIndexAndShrinksPRWhenNarrow`: `l.Name != 20` becomes `l.Name != 21`, message `want 21 (40 - 19)`.
- `TestLayoutDropsPRWhenVeryNarrow`: `l.Name != 13` becomes `l.Name != 14`, message `want 14 (20 - 6)`.

`TestLayoutMatchesTodayAtFullWidth` must pass untouched - the name column is capped at 52 well before these widths. If it fails, stop: the change has reached the dashboard, which it must not.

Check `internal/view/table_test.go` for hardcoded widths too, especially `TestTableKeepsNameColumnPinnedAtFullWidth` and `TestTableDropsGitInAPanelWidthRow`. Adjust only expectations that moved for this reason, and note each in the commit message.

- [ ] **Step 6: Run the suite**

Run: `make test && make lint`
Expected: PASS and clean, including `TestLayoutNeverExceedsItsWidth` and `TestTableNeverExceedsItsWidth`.

- [ ] **Step 7: Mutation-verify**

| Mutation | Must fail |
|---|---|
| `colIndex` back to 2 | `TestTotalMatchesWhatTheRowActuallyRenders` |
| `colState` back to 2 | `TestTotalMatchesWhatTheRowActuallyRenders` |
| `tierNoGit` to 39 | `TestPanelWidthStillPicksTheCompactTier` |
| `tierNoPR` to 13 | `TestFrozenThresholdsAdmitAUsefulName` |

- [ ] **Step 8: Commit**

```bash
git add internal/view/
git commit -m "fix(view): match the column constants to what the renderers emit

colIndex and colState reserved 2 columns where indexCol and
StateIndicatorWithBg each render 1, so Total() overstated every row by 2 and
each tier threshold was 1-2 columns pessimistic. Tier selection widths are
frozen at today's values rather than recomputed: deriving them moves width
40 off the compact tier, which is the layout the panel was verified at."
```

---

## Task 9: Trim the polling query

**Files:**
- Modify: `internal/fetch/pr.go:16-31` (`reviewThreadsQuery`), `internal/fetch/pr.go:70-73`, `fetchReviewThreads`
- Test: `internal/fetch/pr_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func fetchReviewThreads(ctx context.Context, cmd Commander, gitRoot string, prNumber int) int` - count only.
  - `func FetchReviewComments(ctx context.Context, cmd Commander, gitRoot, branch string, prNumber int) []session.ReviewComment` - the on-demand path Task 10 calls.

- [ ] **Step 1: Write the failing tests**

Add to `internal/fetch/pr_test.go`:

```go
// TestPollingQueryDoesNotAskForCommentBodies is the budget fix. The bodies are
// read only by the detail panel, for one session at a time, but every poll
// requested five per thread for every open PR.
func TestPollingQueryDoesNotAskForCommentBodies(t *testing.T) {
	if strings.Contains(reviewThreadsQuery, "comments(") {
		t.Error("the polling query still requests comment bodies")
	}
	for _, field := range []string{"isResolved", "isOutdated"} {
		if !strings.Contains(reviewThreadsQuery, field) {
			t.Errorf("the polling query dropped %s, which the unresolved count needs", field)
		}
	}
}

// TestFetchPRStatusStillCountsUnresolvedThreads pins the load-bearing half.
// UnresolvedComments drives session.State() == Unresolved, the badge and the
// transition notifications, so it must survive the trim.
func TestFetchPRStatusStillCountsUnresolvedThreads(t *testing.T) {
	cmd := NewMockCommander()
	cmd.OnArgs("gh pr view feature/x --json number,state,isDraft,url,title,body,statusCheckRollup,reviewDecision,latestReviews,mergeable,reviewRequests",
		`{"number":42,"state":"OPEN","url":"u","title":"t"}`, nil)
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
				{"isResolved":false,"isOutdated":false,"path":"a.go"},
				{"isResolved":true,"isOutdated":false,"path":"b.go"},
				{"isResolved":false,"isOutdated":true,"path":"c.go"}
			]}}}}}`, nil
		},
	}

	pr := FetchPRStatus(context.Background(), cmd, "feature/x", "/repo")
	if pr == nil {
		t.Fatal("got nil PRStatus")
	}
	if pr.UnresolvedComments != 1 {
		t.Errorf("got %d unresolved, want 1: resolved and outdated threads do not count", pr.UnresolvedComments)
	}
	if len(pr.ReviewComments) != 0 {
		t.Errorf("got %d comments from a poll, want 0", len(pr.ReviewComments))
	}
}

func TestFetchReviewCommentsReturnsBodies(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
				{"isResolved":false,"isOutdated":false,"path":"a.go","comments":{"nodes":[
					{"author":{"login":"reviewer"},"body":"this needs a test"}
				]}}
			]}}}}}`, nil
		},
	}

	comments := FetchReviewComments(context.Background(), cmd, "/repo", "feature/x", 42)
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}
	if comments[0].Author != "reviewer" || comments[0].Body != "this needs a test" {
		t.Errorf("got %+v, want the reviewer's comment", comments[0])
	}
	if comments[0].Path != "a.go" {
		t.Errorf("got path %q, want a.go", comments[0].Path)
	}
}
```

Check the existing `internal/fetch/pr_test.go` for the mock key it already uses for the graphql call and match it; if the file does not exist, create it with `package fetch` and imports `context`, `strings`, `testing`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/fetch/ -run 'TestPollingQuery|TestFetchPRStatusStillCounts|TestFetchReviewComments' -v`
Expected: `TestPollingQueryDoesNotAskForCommentBodies` FAILS; `TestFetchReviewComments` FAILS to compile.

- [ ] **Step 3: Split the query in two**

In `internal/fetch/pr.go`, replace `reviewThreadsQuery` with two constants:

```go
// reviewThreadsQuery is the polling query. It asks only what the unresolved
// count needs. The comment bodies it used to fetch are read by one detail panel
// for one session, but this runs for every open PR every pr_interval, and
// GitHub scores the GraphQL limit on nodes requested.
const reviewThreadsQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes { isResolved isOutdated }
      }
    }
  }
}
`

const reviewCommentsQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          isOutdated
          path
          comments(first: 5) {
            nodes { author { login } body }
          }
        }
      }
    }
  }
}
`
```

- [ ] **Step 4: Narrow `fetchReviewThreads` and add `FetchReviewComments`**

Replace `fetchReviewThreads` with:

```go
// fetchReviewThreads returns the number of threads that are neither resolved
// nor outdated. That count drives session.State() == Unresolved, so it is
// polled for every open PR.
func fetchReviewThreads(ctx context.Context, cmd Commander, gitRoot string, prNumber int) int {
	nodes := reviewThreadNodes(ctx, cmd, gitRoot, prNumber, reviewThreadsQuery)
	unresolved := 0
	for _, t := range nodes {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if !jsonBool(tm, "isResolved") && !jsonBool(tm, "isOutdated") {
			unresolved++
		}
	}
	return unresolved
}

// FetchReviewComments fetches the review comment bodies for one PR. Called for
// the selected session when the detail panel is showing comments, never from a
// poll.
func FetchReviewComments(ctx context.Context, cmd Commander, gitRoot, branch string, prNumber int) []session.ReviewComment {
	var comments []session.ReviewComment
	for _, t := range reviewThreadNodes(ctx, cmd, gitRoot, prNumber, reviewCommentsQuery) {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		resolved := jsonBool(tm, "isResolved")
		path := jsonStr(tm, "path")
		nodes, ok := jsonPath(tm, "comments", "nodes").([]any)
		if !ok {
			continue
		}
		for _, c := range nodes {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			author := ""
			if a, ok := cm["author"].(map[string]any); ok {
				author = jsonStr(a, "login")
			}
			comments = append(comments, session.ReviewComment{
				Author:   author,
				Body:     jsonStr(cm, "body"),
				Path:     path,
				Resolved: resolved,
			})
		}
	}
	return comments
}

func reviewThreadNodes(ctx context.Context, cmd Commander, gitRoot string, prNumber int, query string) []any {
	nwo := getNWO(ctx, cmd, gitRoot)
	if nwo == [2]string{} {
		return nil
	}
	out, err := runWithRetry(ctx, cmd, gitRoot, "gh", "api", "graphql",
		"-f", "query="+query,
		"-F", "owner="+nwo[0],
		"-F", "repo="+nwo[1],
		"-F", fmt.Sprintf("number=%d", prNumber))
	if err != nil {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil
	}
	nodes, _ := jsonPath(data, "data", "repository", "pullRequest", "reviewThreads", "nodes").([]any)
	return nodes
}
```

`branch` is unused in `FetchReviewComments` but is in the signature so the call site reads like the other `Fetch*` functions; if `golangci-lint` objects, drop the parameter and update Task 10's call.

In `FetchPRStatus`, replace the review-threads block:

```go
	var unresolved int
	if state == "OPEN" {
		unresolved = fetchReviewThreads(ctx, cmd, gitRoot, number)
	}
```

and delete `ReviewComments: reviewComments,` from the returned struct literal along with the `reviewComments` variable.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/fetch/ -v`
Expected: PASS. Existing tests that asserted `ReviewComments` came back from `FetchPRStatus` will fail; they were testing polling behaviour that is now deliberately gone. Rewrite each against `FetchReviewComments` rather than deleting it.

- [ ] **Step 6: Close the gap between the constant and what is sent**

`TestPollingQueryDoesNotAskForCommentBodies` asserts on a string constant, so pointing `fetchReviewThreads` at `reviewCommentsQuery` would leave it green. Assert on what actually went out. Add:

```go
// TestFetchPRStatusSendsOnlyThePollingQuery asserts on the recorded invocation
// rather than on the constant, so aiming the poll at the wrong query is caught.
func TestFetchPRStatusSendsOnlyThePollingQuery(t *testing.T) {
	cmd := NewMockCommander()
	cmd.OnArgs("gh pr view feature/x --json number,state,isDraft,url,title,body,statusCheckRollup,reviewDecision,latestReviews,mergeable,reviewRequests",
		`{"number":42,"state":"OPEN","url":"u","title":"t"}`, nil)
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`, nil
		},
	}

	FetchPRStatus(context.Background(), cmd, "feature/x", "/repo")

	var sent []string
	for _, c := range cmd.Calls {
		if c.Name == "gh" && len(c.Args) > 1 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			sent = append(sent, strings.Join(c.Args, " "))
		}
	}
	if len(sent) != 1 {
		t.Fatalf("got %d graphql calls from one poll, want 1", len(sent))
	}
	if strings.Contains(sent[0], "comments(") {
		t.Errorf("the poll asked for comment bodies:\n%s", sent[0])
	}
}
```

Run: `go test ./internal/fetch/ -run TestFetchPRStatusSendsOnlyThePollingQuery -v`
Expected: PASS.

- [ ] **Step 7: Mutation-verify**

| Mutation | Must fail |
|---|---|
| Point `fetchReviewThreads` at `reviewCommentsQuery` | `TestFetchPRStatusSendsOnlyThePollingQuery` |
| Count outdated threads as unresolved | `TestFetchPRStatusStillCountsUnresolvedThreads` |
| Return `nil` from `FetchReviewComments` | `TestFetchReviewCommentsReturnsBodies` |
| Restore `ReviewComments:` to `FetchPRStatus`'s struct literal | `TestFetchPRStatusStillCountsUnresolvedThreads` on the comment-count assertion |

- [ ] **Step 8: Commit**

```bash
git add internal/fetch/
git commit -m "perf(fetch): stop polling review comment bodies

The unresolved count drives session.State(), so the call stays, but the
five comment bodies per thread it also requested are read only by the
detail panel for one session. GitHub scores the GraphQL limit on nodes
requested, so this cuts the per-query cost substantially without changing
the call count."
```

---

## Task 10: Fetch comment bodies on demand

**Files:**
- Modify: `internal/model/model.go` (`refreshDetailCmd`, the `Update` switch), `internal/model/messages.go`, `internal/view/detail.go:199-210`
- Test: `internal/model/detail_test.go` (create or extend)

**Interfaces:**
- Consumes: `fetch.FetchReviewComments` (Task 9).
- Produces: `PRCommentsMsg{Branch string; Comments []session.ReviewComment}`, `Model.reviewComments map[string][]session.ReviewComment` keyed by branch.

- [ ] **Step 1: Write the failing test**

Create `internal/model/detail_test.go`:

```go
package model

import (
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/view"
)

func commentSession() *session.Session {
	return &session.Session{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "feature/x", GitRoot: "/repo"},
		PR: &session.PRStatus{
			Number: 42, State: "OPEN",
			UnresolvedComments: 2,
		},
	}
}

// TestCommentsModeFetchesBodiesOnDemand is the consequence of the polling trim:
// nothing carries the bodies any more, so opening the mode has to go and get
// them.
func TestCommentsModeFetchesBodiesOnDemand(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode

	cmd := m.refreshDetailCmd()
	if cmd == nil {
		t.Fatal("comments mode produced no fetch command")
	}
	if _, ok := cmd().(PRCommentsMsg); !ok {
		t.Fatalf("got %T, want PRCommentsMsg", cmd())
	}
}

func TestPRCommentsMsgIsStoredByBranch(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}

	next, _ := m.Update(PRCommentsMsg{
		Branch:   "feature/x",
		Comments: []session.ReviewComment{{Author: "reviewer", Body: "needs a test"}},
	})

	got := next.(Model).reviewComments["feature/x"]
	if len(got) != 1 || got[0].Author != "reviewer" {
		t.Fatalf("got %+v, want the reviewer's comment stored under its branch", got)
	}
}

// TestCommentsModeDoesNotRefetchWhatItHas keeps the mode from spending a gh
// call on every render tick while the panel is open.
func TestCommentsModeDoesNotRefetchWhatItHas(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode
	m.reviewComments = map[string][]session.ReviewComment{
		"feature/x": {{Author: "reviewer", Body: "needs a test"}},
	}

	if cmd := m.refreshDetailCmd(); cmd != nil {
		t.Error("refetched comments that are already loaded")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/model/ -run 'TestCommentsMode|TestPRCommentsMsg' -v`
Expected: FAIL, `undefined: PRCommentsMsg`.

- [ ] **Step 3: Add the message and the store**

In `internal/model/messages.go`:

```go
// PRCommentsMsg carries review comment bodies fetched on demand for one branch.
// Polling stopped fetching these: they are read by one detail panel for one
// session, and requesting them for every open PR every cycle was the bulk of
// the review-threads query's cost.
type PRCommentsMsg struct {
	Branch   string
	Comments []session.ReviewComment
}
```

In `internal/model/model.go`, add to `Model`:

```go
	reviewComments map[string][]session.ReviewComment
```

and to `newModel`'s literal:

```go
		reviewComments: make(map[string][]session.ReviewComment),
```

Also add the initialiser to `newTestModel` in `internal/model/client_test.go`.

- [ ] **Step 4: Extend `refreshDetailCmd`**

Replace `refreshDetailCmd` with:

```go
func (m Model) refreshDetailCmd() tea.Cmd {
	s := m.selectedSession()
	if s == nil {
		return nil
	}
	switch m.activeDetailMode() {
	case view.DetailPane:
		name := s.Name
		window := m.cfg.GetSetting("capture_window")
		return func() tea.Msg {
			return PaneCapturedMsg{
				SessionName: name,
				Content:     fetch.CapturePane(m.ctx, m.cmd, name, 20, window),
			}
		}
	case view.DetailPRComments:
		if s.PR == nil || s.Git.Branch == "" {
			return nil
		}
		if _, loaded := m.reviewComments[s.Git.Branch]; loaded {
			return nil
		}
		branch, gitRoot, number := s.Git.Branch, s.Git.GitRoot, s.PR.Number
		ctx, cmd := m.ctx, m.cmd
		return func() tea.Msg {
			return PRCommentsMsg{
				Branch:   branch,
				Comments: fetch.FetchReviewComments(ctx, cmd, gitRoot, branch, number),
			}
		}
	}
	return nil
}
```

A branch whose fetch returned nothing is still stored, as an empty non-nil slice, so the `loaded` check stops a retry loop. Handle that in Step 5.

- [ ] **Step 5: Handle the message**

Add to the `Update` switch, beside `PaneCapturedMsg`:

```go
	case PRCommentsMsg:
		comments := msg.Comments
		if comments == nil {
			comments = []session.ReviewComment{}
		}
		m.reviewComments[msg.Branch] = comments
		return m, nil
```

- [ ] **Step 6: Render from the store**

`renderPRComments` at `internal/view/detail.go:198` reads `s.PR.ReviewComments`, which polling no longer fills. Pass the comments in, so the view keeps knowing nothing about the model's cache.

In `internal/view/detail.go`, change `RenderDetail`'s signature and its one internal call:

```go
func RenderDetail(s *session.Session, mode DetailMode, paneContent string, comments []session.ReviewComment, staleThreshold int, width, height int) string {
```

```go
	case DetailPRComments:
		renderPRComments(&b, s, comments, height-3)
```

Replace the head of `renderPRComments` (lines 198-212) with:

```go
func renderPRComments(b *strings.Builder, s *session.Session, comments []session.ReviewComment, maxLines int) {
	if s.PR == nil {
		b.WriteString(DimStyle.Render("  No review comments"))
		return
	}
	// nil means not fetched yet; the model stores an empty non-nil slice once a
	// fetch has answered, so an in-flight fetch does not read as "none".
	if comments == nil {
		if s.PR.UnresolvedComments > 0 {
			b.WriteString(DimStyle.Render("  Loading comments…"))
		} else {
			b.WriteString(DimStyle.Render("  No review comments"))
		}
		return
	}
	if len(comments) == 0 {
		b.WriteString(DimStyle.Render("  No review comments"))
		return
	}
	var unresolved []session.ReviewComment
	for _, c := range comments {
		if !c.Resolved {
			unresolved = append(unresolved, c)
		}
	}
	if len(unresolved) == 0 {
		b.WriteString(DimStyle.Render("  All comments resolved"))
		return
	}
```

The rest of the function, from `lines := 0`, is unchanged.

In `internal/model/model.go:372`, pass the store:

```go
		var comments []session.ReviewComment
		if s.Git.Branch != "" {
			comments = m.reviewComments[s.Git.Branch]
		}
		detail = view.RenderDetail(s, mode, m.paneContent, comments, staleThreshold, m.width, detailHeight)
```

- [ ] **Step 6b: Pin the loading state**

Add to `internal/view/detail_test.go` (create it if absent, `package view`):

```go
// TestPRCommentsShowsLoadingBeforeTheFetchAnswers separates "not fetched" from
// "none", which polling used to make indistinguishable because it always
// carried an answer.
func TestPRCommentsShowsLoadingBeforeTheFetchAnswers(t *testing.T) {
	s := &session.Session{PR: &session.PRStatus{Number: 1, UnresolvedComments: 2}}

	var b strings.Builder
	renderPRComments(&b, s, nil, 10)
	if !strings.Contains(b.String(), "Loading") {
		t.Errorf("got %q, want a loading state for an unfetched thread list", b.String())
	}

	b.Reset()
	renderPRComments(&b, s, []session.ReviewComment{}, 10)
	if strings.Contains(b.String(), "Loading") {
		t.Errorf("got %q, want a settled answer once the fetch returned empty", b.String())
	}
}
```

Run: `go test ./internal/view/ -run TestPRCommentsShowsLoading -v`
Expected: PASS.

- [ ] **Step 7: Run the suite**

Run: `make test && make lint`
Expected: PASS and clean.

- [ ] **Step 8: Mutation-verify**

| Mutation | Must fail |
|---|---|
| Delete the `loaded` early return | `TestCommentsModeDoesNotRefetchWhatItHas` |
| Store under `s.Name` instead of `msg.Branch` | `TestPRCommentsMsgIsStoredByBranch` |
| Return `nil` from the `DetailPRComments` case | `TestCommentsModeFetchesBodiesOnDemand` |
| Collapse the `comments == nil` branch into the `len(comments) == 0` one | `TestPRCommentsShowsLoadingBeforeTheFetchAnswers` |
| Drop the `comments == nil` normalisation in the `PRCommentsMsg` handler | `TestCommentsModeDoesNotRefetchWhatItHas` after a fetch that returned nothing - if this stays green, the retry loop is unpinned; add a case that stores an empty result and asserts no refetch |

- [ ] **Step 9: Commit**

```bash
git add internal/model/ internal/view/
git commit -m "feat(model): fetch review comment bodies on demand

Polling no longer carries them, so the detail panel's comments mode fetches
per selected branch and caches by branch, the way pane capture already
works."
```

---

## Task 11: Update the docs

**Files:**
- Modify: `CLAUDE.md`
- Create: `docs/superpowers/2026-07-28-phase-2-blockers-handoff.md`

- [ ] **Step 1: Update `CLAUDE.md`**

In the Architecture list, add:

```
- `internal/transition/` - state-change detection (`Detector`) and the side effects a change triggers (`Runner`: the `notify` hook and `auto_cleanup`). Shared, because side effects belong to whoever owns the poll loop - the daemon when a client is connected to one, a self-polling client otherwise - and detection must not be implemented twice
```

Replace the two Key Conventions bullets that describe the old polling shape:

- `Background polling: tmux every 1s, git every 3s, PR every 30s (parallel fetches)` becomes:
  `Both the daemon and a self-polling client run one Collector.Snapshot per tmux_interval; git and PR work is gated inside it by git_interval and pr_interval memos. The TUI has no separate tmux/git/PR tick cycles`
- Add: `State-transition side effects run once per event, in whichever process owns the poll loop. Toasts and auto-focus stay per-client. auto_cleanup failures go to the daemon log, not to a client`
- Add: `The review-threads poll fetches only the unresolved count. Comment bodies are fetched on demand for the selected branch and cached by branch`
- Amend the layout bullet: the tier selection widths are tuned constants (`tierFull` and friends), frozen at the values that were verified on real panes, not derived from the fixed costs

Remove the "In-flight design work" pointer to phase 3 being blocked on these items, and point at the new handoff.

- [ ] **Step 2: Write the handoff**

Create `docs/superpowers/2026-07-28-phase-2-blockers-handoff.md` covering: what landed; that `auto_cleanup` is now safe to enable with panels open and this is the first time that has been true; the two handoff claims this work corrected (review threads are not detail-panel-only, and the `notify` duplication was live rather than latent); the debt still open, carried from the spec's closing section; and anything found by using it after the merge.

Write this before deleting any workspace. The phase 2 retro's process note: git history did not hold the ledger, and the handoff only exists because it was reconstructed before the session ended.

- [ ] **Step 3: Verify the claims in the docs**

For every factual claim added to `CLAUDE.md`, confirm it against the code as it now stands. `CLAUDE.md` is loaded as authoritative agent context; the phase 2 retro records it being made to claim a script existed when it lived in no repository.

Run: `make test && make lint`

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/
git commit -m "docs: record the transition split, the collapsed poll path, and the blocker handoff"
```

---

## Verification before calling this done

Static tests do not cover the properties these fixes exist to protect. Run these on the real machine, with an isolated `XDG_RUNTIME_DIR` so nothing touches the live socket, following the phase 2 gate's method.

- [ ] **Hooks fire once with N panels.** Set `notify = "echo $(date +%s%N) >> /tmp/vigil-notify.log"` in the config. Open three panels against one daemon, force a state change, and confirm exactly one line per transition. This is the blocker; it has never been observed either way.
- [ ] **`auto_cleanup` runs once.** With `auto_cleanup = true` and three panels open, merge a PR and confirm one `git worktree remove`, no duplicate-failure noise in `vigild.log`, and that the session leaves every panel. Do this on a throwaway worktree.
- [ ] **The current session is never cleaned up.** With `auto_cleanup = true`, be attached to a session whose PR merges. It must survive.
- [ ] **Daemon-up versus daemon-down is still byte-identical.** Repeat the phase 2 gate check at 120x20: capture with a daemon, kill it, capture again. Git and PR columns must match. This is the collapse's whole claim.
- [ ] **Fallback survives a failing poll.** Break `tmux` on the path for a self-polling client and confirm it keeps polling and recovers, rather than going quiet forever.
- [ ] **Width 40 is unchanged.** Capture a 40-column panel before and after Task 8. The name column must be at least as wide as it was.
- [ ] **Comments mode still works.** Open a PR with unresolved review threads, switch to comments mode, and confirm the bodies arrive and that switching away and back does not refetch.
- [ ] **`make install` while a daemon runs.** Still the temp-file-and-rename path from phase 2. Confirm the new binary runs, since overwriting a running image's inode invalidates its signature and macOS then SIGKILLs it.

---

## Landmines

- **`cp` is aliased to `-i`.** Mutation-test by mutating and restoring with `git checkout --` or `git stash`, never a file copy. A leftover backup of the same name silently wins and gets written over the working file. This happened during phase 2.
- **`action.CleanupSession` calls `switchAwayIfCurrent`,** which runs `tmux display-message` and possibly `switch-client`. From the daemon there is no attached client of its own, so what tmux reports is the user's client. `Runner.Run` skips cleanup for the current session before reaching this, but if that guard is ever removed the daemon can move the user's client.
- **`session.Session` holds a `*PRStatus`,** so `==` on two sessions compares PR pointers. Task 3's identical-paths test is only valid because the fixture yields nil PRs. Compare field by field if that changes.
- **`config.RunHook` bypasses `fetch.Commander`** and shells out through `exec.CommandContext`. It cannot be observed with a `MockCommander`; use a hook that writes a file, or the `EffectRunner` seam.
- **`notify` has a default template** and `notifications_enabled` defaults to `"true"`, so hooks fire out of the box. A test asserting no hook ran must disable it explicitly.
- **`Collector.Snapshot` is not reentrant.** Its memos are owned by the calling goroutine. Exactly one `collectCmd` may be in flight; that is why the fallback self-schedules from its own result instead of running a ticker.
- **The daemon's `poll` is synchronous per tick.** Effects run in goroutines for this reason. A hook that blocks for its full timeout would otherwise delay every client's snapshot.
- **`visibleLen` counts runes, not display columns.** Out of scope here, but it means every width assertion in this plan, including the new `Total()` test, uses the same metric the renderer does and cannot see a double-width glyph overflow.

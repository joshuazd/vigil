# Session Hopping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tmux's session navigation agree with vigil's displayed order, add
digit-keyed hopping and PR opening as native tmux bindings, instrument the session-removal
path so its latency can be diagnosed, and fix the `notify` hook default that has never
produced a single successful fire.

**Architecture:** Four independent changes. vigil's `SortCreated` gains `session_id` as a
tie-break so its order equals the pure-`session_id` order tmux bindings can
compute unaided (**"provably" was wrong - see Step 1's correction**); the bindings move into one `tmux-hop` script in `~/dotfiles` with no
vigil dependency of any kind; the daemon gains one log line recording when a session left
its list, paired with temporary timestamps in the dotfiles cleanup path; and the default
`notify` hook is rewritten into the only quoting form that survives `ExpandHook`.

**Tech Stack:** Go 1.x (no new dependencies), Bubble Tea, POSIX sh / bash, tmux, `gh`.

## Global Constraints

- Spans two repositories: `/Users/joshua.zink-duda/vigil` and
  `/Users/joshua.zink-duda/dotfiles`. Tasks 1-4 and 8 are vigil; tasks 5-7 are dotfiles.
  Commit in the repository the task names, never across both.
- `make test` is `go test -race ./...`. `-race` is not optional.
- No new Go dependencies.
- **Every test in this plan requires a mutation check**: delete or revert the subject,
  run the test, confirm it FAILS, restore the subject, confirm it PASSES. Paste the
  failing output into the task report. This is mandated by `CLAUDE.md`'s standing warning
  — nineteen briefs in this repository have shipped tests that would pass with their
  subject deleted. A step that says "run the mutation check" is not optional and not
  satisfiable by reasoning about it.
- The tmux bindings must work on a machine with no vigil installed and no daemon running.
  No task may add a `vigil` invocation to `~/dotfiles/tmux/.tmux.conf` or to `tmux-hop`.
- Never type a heredoc into the Bash tool; create files with the Write tool instead. A
  heredoc *inside* a script file is fine and is the house style — `git-worktree-done`'s
  `usage` uses one.
- `cp` and `rm` are aliased to `-i`; use `\rm` and `\cp`.
- Design authority: `docs/superpowers/specs/2026-08-03-session-hopping-design.md`. Where
  this plan and that spec disagree, the spec wins; where this plan and the shipped code
  disagree, **the code wins**.

---

## File Structure

**vigil**

| File | Responsibility | Task |
|---|---|---|
| `internal/fetch/tmux.go` | `RawSession.ID`; `#{session_id}` in the `list-panes` format; parse index shift | 1 |
| `internal/fetch/tmux_test.go` | `ListSessions` fixtures updated to the new field count; ID parse test | 1 |
| `internal/daemon/slowpoll_test.go` | `OnArgs` key updated to the new format string | 1 |
| `internal/session/session.go` | `Session.ID` | 2 |
| `internal/collect/collect.go` | copy `ID` from `RawSession` into `Session` | 2 |
| `internal/session/sort.go` | `SortCreated` comparator becomes `(Created, ID)` | 2 |
| `internal/session/session_test.go` | tie-break and ID-zero degradation tests | 2 |
| `internal/collect/collect_test.go` | `Snapshot` carries `ID` through | 2 |
| `internal/daemon/daemon.go` | `prevSessions` field; `logDroppedSessions`; call from `poll` | 3 |
| `internal/daemon/dropped_test.go` | new: drop / no-change / growth / multi-drop | 3 |
| `internal/config/config.go` | `notify` default rewritten to adjacent-quoting form | 4 |
| `internal/config/config_test.go` | expanded hook reduces to one shell argument | 4 |
| `CLAUDE.md` | record what landed and what is still open | 8 |
| `docs/superpowers/2026-08-03-session-hopping-handoff.md` | new: handoff | 8 |

**dotfiles**

| File | Responsibility | Task |
|---|---|---|
| `scripts/scripts/tmux-hop` | new: the single ordered-session-list implementation and the three hop modes | 5 |
| `tmux/.tmux.conf` | `M-j`/`M-k` rebound to `tmux-hop`; `M-0`..`M-9`; `M-o` | 6 |
| `scripts/scripts/git-worktree-done` | temporary hi-res timestamps at four points | 7 |
| `scripts/scripts/git-worktree-cleanup` | temporary hi-res timestamps at three points | 7 |

---

## Task 1: `ListSessions` reads `#{session_id}`

**Files:**
- Modify: `/Users/joshua.zink-duda/vigil/internal/fetch/tmux.go:12-17` (struct), `:47-50`
  (doc comment and format string), `:56-103` (parse)
- Modify: `/Users/joshua.zink-duda/vigil/internal/fetch/tmux_test.go` (fixtures)
- Modify: `/Users/joshua.zink-duda/vigil/internal/daemon/slowpoll_test.go:23` (`OnArgs` key)

**Interfaces:**
- Consumes: nothing.
- Produces: `fetch.RawSession{Name string; PanePath string; Created int64; ID int}`. Task
  2 reads `.ID` off it. `ID` is 0 when the field is absent or unparseable.

**Context you need.** `ListSessions` runs one `tmux list-panes -a` and splits each line on
`|`. It deliberately does **not** use a fixed field count: a `pane_current_path`
containing a pipe would otherwise swallow the flags that follow it. The rule the existing
comment defends is "the flags are the last three fields, the path is everything between
the name and them". `#{session_id}` therefore goes between `session_created` and
`session_name` — not first, not appended — so that rule is untouched. tmux renders
`session_id` as `$7`, with a literal leading `$`.

`sort.Strings(lines)` stays. Its only job is making equal-preference panes for one session
resolve identically across polls; within one session name the ID is constant, so the new
field cannot change dedup behavior.

- [ ] **Step 1: Write the failing test**

Add to `/Users/joshua.zink-duda/vigil/internal/fetch/tmux_test.go`:

```go
func TestListSessionsParsesTheSessionID(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|$7|alpha|/home/alpha", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != 7 {
		t.Errorf("got ID %d, want 7", sessions[0].ID)
	}
	if sessions[0].Name != "alpha" {
		t.Errorf("got name %q, want alpha", sessions[0].Name)
	}
	if sessions[0].PanePath != "/home/alpha" {
		t.Errorf("got path %q, want /home/alpha", sessions[0].PanePath)
	}
}

// The pipe-in-path rule, re-pinned against the new field count. A path
// containing a pipe must not shift the three trailing flags, and the ID must
// still be read from the field before the name.
func TestListSessionsWithAPipeInThePathStillReadsTheIDAndFlags(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|$12|gamma|/home/we|rd/path|1|1|", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != 12 {
		t.Errorf("got ID %d, want 12", sessions[0].ID)
	}
	if sessions[0].PanePath != "/home/we|rd/path" {
		t.Errorf("got path %q, want /home/we|rd/path", sessions[0].PanePath)
	}
}

// A line with no ID field at all yields ID 0 rather than a dropped session.
// Task 2's comparator relies on 0 meaning "unknown", so this pins the value.
func TestListSessionsWithAnUnparseableIDYieldsZero(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|notanid|alpha|/home/alpha", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != 0 {
		t.Errorf("got ID %d, want 0", sessions[0].ID)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/fetch/ -run TestListSessions -v`

Expected: FAIL — `sessions[0].ID undefined (type RawSession has no field or method ID)`,
a compile error. That is the correct first failure.

- [ ] **Step 3: Add the `ID` field**

In `internal/fetch/tmux.go`, replace the struct:

```go
// RawSession is an intermediate struct from tmux list-panes output.
type RawSession struct {
	Name     string
	PanePath string
	Created  int64

	// ID is #{session_id} with its leading '$' stripped. tmux never reuses a
	// session id and issues them in increasing order, so it is a total order
	// equal to creation order - which is what the tmux keybindings in
	// ~/dotfiles sort by, and why Session.ID exists. 0 means the field was
	// absent or unparseable.
	ID int
}
```

- [ ] **Step 4: Change the format string and the parse**

In `internal/fetch/tmux.go`, replace the doc comment and the `cmd.Run` call:

```go
// ListSessions returns tmux sessions sorted by creation time, deduplicated by
// name, each carrying the path of the pane that best represents its work.
func ListSessions(ctx context.Context, cmd Commander) ([]RawSession, error) {
	out, err := cmd.Run(ctx, "", "tmux", "list-panes", "-a",
		"-F", "#{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}")
```

Then in the parse loop, apply exactly four index shifts. The short-line guard:

```go
		if len(parts) < 4 {
			continue
		}
		name := parts[2]
```

The path/flags split:

```go
		flagStart := len(parts) - 3
		var path string
		var isActive, isClaude, isPanel bool
		if flagStart > 3 {
			path = strings.Join(parts[3:flagStart], "|")
			isActive = parts[flagStart] == "1"
			isClaude = parts[flagStart+1] == "1"
			isPanel = parts[flagStart+2] == "1"
		} else {
			path = strings.Join(parts[3:], "|")
		}
```

And where `created` is parsed, add the ID parse and carry it into the struct:

```go
		created, _ := strconv.ParseInt(parts[0], 10, 64)
		id, _ := strconv.Atoi(strings.TrimPrefix(parts[1], "$"))
		index[name] = len(sessions)
		prefs[name] = pref
		sessions = append(sessions, RawSession{
			Name:     name,
			PanePath: path,
			Created:  created,
			ID:       id,
		})
```

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/fetch/ -run TestListSessions -v`

Expected: the three new tests PASS. The three pre-existing `TestListSessions*` tests will
now FAIL, because their fixtures have one field too few — that is expected and Step 6
fixes them.

- [ ] **Step 6: Update the pre-existing fixtures**

Three tests in `internal/fetch/tmux_test.go` build fixture lines without an ID field. Add
one. `TestListSessions`:

```go
	mock.On("tmux", "1000|$1|alpha|/home/alpha\n1000|$1|alpha|/home/alpha/pane2\n999|$2|beta|/home/beta", nil)
```

`TestListSessionsDeduplicates`:

```go
	mock.On("tmux", "1000|$1|session1|/path1\n1000|$1|session1|/path2", nil)
```

`TestListSessionsIgnoresAPanelPaneInAnotherDirectory`:

```go
	mock.On("tmux", strings.Join([]string{
		"1000|$1|SC-198799 Fix NoMethodError in|/Users/x/portal|0||1",
		"1000|$1|SC-198799 Fix NoMethodError in|/Users/x/sc-198799|1|1|",
		"1000|$1|SC-198799 Fix NoMethodError in|/Users/x/sc-198799|1||",
	}, "\n"), nil)
```

Note in `TestListSessions` that the comment "`1000` sorts before `999` lexicographically"
is still true and still the reason `alpha` precedes `beta`; leave it.

- [ ] **Step 7: Update the daemon test's `OnArgs` key**

`internal/daemon/slowpoll_test.go:23` registers a handler keyed on the **exact** command
line. It will silently stop matching and the slow-poll tests will fail on a nil session
list. Replace the key and the fixture:

```go
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|fast|/tmp/fast\n1700000001|$2|slow|/tmp/slow", nil)
```

- [ ] **Step 8: Run the full suite**

Run: `cd /Users/joshua.zink-duda/vigil && make test`

Expected: PASS. If any other test fails on a `list-panes` fixture, it is the same class —
add the `$N` field. Search for other occurrences before assuming there are none:

Run: `cd /Users/joshua.zink-duda/vigil && grep -rn "session_created" --include="*.go" .`

Every hit must contain `session_id`.

- [ ] **Step 9: Mutation check**

Revert the format string to omit `#{session_id}` (leave the parse and struct alone). Run
`go test ./internal/fetch/ -run TestListSessionsParsesTheSessionID -v`. Confirm FAIL.
Restore. Confirm PASS. Paste both outputs into the report.

Then revert only `id, _ := strconv.Atoi(...)` to `id := 0` and run
`TestListSessionsParsesTheSessionID` again. Confirm FAIL, restore, confirm PASS. Paste.

Two mutations, because the format string and the parse are separate ways for this to be
silently wrong.

- [ ] **Step 10: Commit**

```bash
cd /Users/joshua.zink-duda/vigil
git add internal/fetch/tmux.go internal/fetch/tmux_test.go internal/daemon/slowpoll_test.go
git commit -m "feat(fetch): read session_id in ListSessions"
```

---

## Task 2: `Session` carries `ID` and `SortCreated` breaks ties with it

**Files:**
- Modify: `/Users/joshua.zink-duda/vigil/internal/session/session.go:118-134` (struct)
- Modify: `/Users/joshua.zink-duda/vigil/internal/session/sort.go:31-47` (comparator)
- Modify: `/Users/joshua.zink-duda/vigil/internal/collect/collect.go:205-213` (populate)
- Modify: `/Users/joshua.zink-duda/vigil/internal/session/session_test.go` (tests)
- Modify: `/Users/joshua.zink-duda/vigil/internal/collect/collect_test.go` (test)

**Interfaces:**
- Consumes: `fetch.RawSession.ID` from Task 1.
- Produces: `session.Session.ID int` with JSON tag `id`. Nothing after this task reads it
  except `SortSessions`.

**Context you need, and the reason this is not a pure-ID sort.** The bug being fixed is a
tie-break disagreement, not a wrong key. `~/dotfiles/tmux/.tmux.conf` orders sessions by
`session_id`. vigil orders by `Session.Created`, which is `#{session_created}` in whole
**seconds**, so two sessions created in the same second tie; `sortBy` is a stable
insertion sort, so the tie falls through to the order `ListSessions` emitted, which is
alphabetical by name. Same primary key, different tie-break.

The comparator becomes `(Created, ID)` lexicographic rather than pure `ID`. Because
`session_created` is monotonic in `session_id` **in practice** (**corrected 2026-08-03:
this brief said "provably" and that is false - the two orders diverge if the wall clock
moves backwards between two creations while tmux's id counter climbs**), `(Created, ID)`
yields the same total order as pure `ID`, while degrading to exactly today's behavior when
`ID` is 0 —
the case of a session hydrated from a cache file written before this change. A pure-`ID`
comparator would sort every such session ahead of every real one until the first poll
landed. Do not "simplify" this to `return a.ID < b.ID`.

- [ ] **Step 1: Write the failing tests**

Add to `/Users/joshua.zink-duda/vigil/internal/session/session_test.go`:

```go
// The bug this fixes. Two sessions created in the same second tie on Created,
// and the stable insertion sort then preserved whatever order ListSessions
// emitted. The tmux keybindings order by session_id. Input is deliberately in
// the wrong id order, so a comparator that ignores ID leaves it untouched and
// this test fails.
func TestSortCreatedBreaksATieByID(t *testing.T) {
	sessions := []*Session{
		{Name: "zulu", Created: 1000, ID: 9},
		{Name: "alpha", Created: 1000, ID: 4},
	}

	SortSessions(sessions, SortCreated)

	if sessions[0].ID != 4 {
		t.Errorf("got ID %d first, want 4", sessions[0].ID)
	}
	if sessions[1].ID != 9 {
		t.Errorf("got ID %d second, want 9", sessions[1].ID)
	}
}

// Created still dominates: a later-created session with a lower ID must not
// jump ahead. This is what makes the comparator (Created, ID) rather than ID,
// and it is the assertion a pure-ID comparator fails.
func TestSortCreatedStillOrdersByCreatedFirst(t *testing.T) {
	sessions := []*Session{
		{Name: "later", Created: 2000, ID: 1},
		{Name: "earlier", Created: 1000, ID: 9},
	}

	SortSessions(sessions, SortCreated)

	if sessions[0].Name != "earlier" {
		t.Errorf("got %q first, want earlier", sessions[0].Name)
	}
}

// A session hydrated from a cache file written before ID existed has ID 0.
// It must not be hoisted ahead of every real session; the order degrades to
// Created, which is what shipped before this change and self-heals on the
// first poll.
func TestSortCreatedWithAZeroIDFallsBackToCreated(t *testing.T) {
	sessions := []*Session{
		{Name: "fromCache", Created: 3000, ID: 0},
		{Name: "live", Created: 1000, ID: 7},
	}

	SortSessions(sessions, SortCreated)

	if sessions[0].Name != "live" {
		t.Errorf("got %q first, want live", sessions[0].Name)
	}
}
```

Add to `/Users/joshua.zink-duda/vigil/internal/collect/collect_test.go`:

```go
// Snapshot must carry the id through from RawSession, or SortSessions has
// nothing to break ties with and the fix is inert on the real path.
func TestSnapshotCarriesTheSessionID(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$5|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.On("git", "", nil)

	c := collect.New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != 5 {
		t.Errorf("got ID %d, want 5", sessions[0].ID)
	}
}
```

Check `collect_test.go`'s existing imports and package clause before pasting — if the file
is `package collect` rather than `package collect_test`, drop the `collect.` qualifier on
`New`.

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```
cd /Users/joshua.zink-duda/vigil
go test ./internal/session/ -run TestSortCreated -v
go test ./internal/collect/ -run TestSnapshotCarriesTheSessionID -v
```

Expected: FAIL, compile error — `unknown field ID in struct literal of type Session`.

- [ ] **Step 3: Add `Session.ID`**

In `internal/session/session.go`, add the field to `Session` immediately after `Created`:

```go
type Session struct {
	Name     string `json:"name"`
	PanePath string `json:"pane_path"`
	Created  int64  `json:"created"`

	// ID is tmux's #{session_id}. It exists for one reason: #{session_created}
	// is whole seconds, so two sessions created in the same second tie, and
	// the tmux keybindings in ~/dotfiles order sessions by session_id. Sorting
	// (Created, ID) is the same total order as their pure-ID sort, because
	// session ids are issued in increasing order. 0 means unknown - a session
	// read from a cache file written before this field existed.
	ID int `json:"id"`

	IsCurrent bool      `json:"-"`
	IsLast    bool      `json:"-"`
	HasBell   bool      `json:"has_bell"`
	Git       GitStatus `json:"git"`
	PR        *PRStatus `json:"pr,omitempty"`
	// ... PRPending and its existing comment unchanged
}
```

Keep `PRPending` and its comment exactly as they are.

- [ ] **Step 4: Populate it in `Snapshot`**

In `internal/collect/collect.go`, in the loop that builds `sessions` from `raw`:

```go
	sessions := make([]*session.Session, len(raw))
	for i, r := range raw {
		sessions[i] = &session.Session{
			Name:     r.Name,
			PanePath: r.PanePath,
			Created:  r.Created,
			ID:       r.ID,
			HasBell:  bells[r.Name],
		}
	}
```

- [ ] **Step 5: Change the comparator**

In `internal/session/sort.go`, replace the `SortCreated` case:

```go
	case SortCreated:
		// (Created, ID), not ID alone. #{session_created} is whole seconds, so
		// ties are common and used to fall through the stable sort to
		// ListSessions' alphabetical order - while ~/dotfiles' M-j/M-k order by
		// session_id, which is why they disagreed. Session ids are issued in
		// increasing order, so (Created, ID) is the same total order as pure
		// ID, and unlike pure ID it degrades to Created when ID is 0 rather
		// than hoisting every cache-hydrated session to the front.
		sortBy(sessions, func(a, b *Session) bool {
			if a.Created != b.Created {
				return a.Created < b.Created
			}
			return a.ID < b.ID
		})
```

- [ ] **Step 6: Run the tests to verify they pass**

Run:
```
cd /Users/joshua.zink-duda/vigil
go test ./internal/session/ -run TestSortCreated -v
go test ./internal/collect/ -run TestSnapshotCarriesTheSessionID -v
```

Expected: all four PASS.

- [ ] **Step 7: Run the full suite**

Run: `cd /Users/joshua.zink-duda/vigil && make test && make lint`

Expected: PASS.

- [ ] **Step 8: Mutation check**

Three mutations, one per claim:

1. Revert the comparator to `return a.Created < b.Created`. Run
   `go test ./internal/session/ -run TestSortCreatedBreaksATieByID -v`. Confirm FAIL.
2. Change it to pure `return a.ID < b.ID`. Run
   `go test ./internal/session/ -run TestSortCreated -v`. Confirm
   `TestSortCreatedWithAZeroIDFallsBackToCreated` **and**
   `TestSortCreatedStillOrdersByCreatedFirst` FAIL — this is the check that proves the
   two-key comparator is doing work the one-key version cannot.
3. Remove `ID: r.ID` from the `Snapshot` loop. Run
   `go test ./internal/collect/ -run TestSnapshotCarriesTheSessionID -v`. Confirm FAIL.

Restore after each, confirm PASS, paste all three failing outputs into the report.

- [ ] **Step 9: Verify against real tmux**

The two orders agreeing is the entire point, and no unit test can check it. With at least
three real sessions open:

```bash
tmux list-sessions -F '#{session_id}|#{session_created}|#{session_name}' \
  | sed 's/^\$//' | sort -t'|' -k1,1n | cut -d'|' -f3-
```

Then open the vigil dashboard (`prefix v`) and compare top-to-bottom against that list.
They must match exactly, including the index column's digits. Paste both lists into the
report. If any two sessions share a `session_created` value, say so explicitly — that is
the case this task exists for and its presence makes the comparison meaningful.

- [ ] **Step 10: Commit**

```bash
cd /Users/joshua.zink-duda/vigil
git add internal/session/session.go internal/session/sort.go internal/session/session_test.go internal/collect/collect.go internal/collect/collect_test.go
git commit -m "fix(session): break a created-time tie by session id"
```

---

## Task 3: The daemon logs a dropped session

**Files:**
- Modify: `/Users/joshua.zink-duda/vigil/internal/daemon/daemon.go` (`Server` struct;
  new `logDroppedSessions`; one call in `poll`)
- Create: `/Users/joshua.zink-duda/vigil/internal/daemon/dropped_test.go`

**Interfaces:**
- Consumes: `session.Session.Name`.
- Produces: a daemon log line `session dropped: <name>`, one per departed session.
  Nothing in Go reads it; Task 7's diagnosis reads it by eye.

**Context you need.** This is the vigil half of the instrumentation for the unreproduced
"a few seconds to disappear" complaint. It is daemon-only and deliberately not
user-visible, following the `slow poll` precedent in the same file: a session leaving the
list is not a failure, and a self-polling client has no log to write to.

It inherits `poll`'s threading rule. `poll` is synchronous per tick and runs on `Run`'s
goroutine, so the previous poll's name set is a plain `Server` field needing no mutex —
the same argument that covers `gitMemo` in the collector. Do not add a lock.

Unlike `logSlowPoll` this needs **no rate limit**. `logSlowPoll`'s limit is a window
because a slow machine would otherwise log once and go quiet for hours while a diagnosis
wants a series; a dropped session is edge-triggered by a thing the user did, so one line
per event is exactly right.

Place the call **after** the `err != nil` return in `poll`, so a failed collection is not
read as every session vanishing at once.

- [ ] **Step 1: Write the failing tests**

Create `/Users/joshua.zink-duda/vigil/internal/daemon/dropped_test.go`:

```go
package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

const listPanesKey = "tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}"

// droppedServer builds a Server whose session list is whatever lines() returns
// at the moment of the call, so one test can poll twice with different sets.
func droppedServer(t *testing.T, buf *bytes.Buffer, lines func() string) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs[listPanesKey] = func(ctx context.Context, dir string, args []string) (string, error) {
		return lines(), nil
	}
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.On("git", "", nil)

	return &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Log:       log.New(buf, "", 0),
	}
}

func TestPollLogsADroppedSession(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha"
	s.poll(context.Background())

	got := buf.String()
	if !strings.Contains(got, "session dropped: beta") {
		t.Errorf("got log %q, want it to name the dropped session beta", got)
	}
	if strings.Contains(got, "alpha") {
		t.Errorf("got log %q, want nothing about the surviving session alpha", got)
	}
}

// The test that would pass with the feature deleted, which is why it is paired
// with the positive case above in the same run rather than standing alone.
func TestPollWithAnUnchangedSessionSetLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()
	s.poll(context.Background())

	if buf.String() != "" {
		t.Errorf("got log %q, want nothing for an unchanged session set", buf.String())
	}
}

func TestPollWithAGrowingSessionSetLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta"
	s.poll(context.Background())

	if buf.String() != "" {
		t.Errorf("got log %q, want nothing for a new session", buf.String())
	}
}

func TestPollLogsEverySessionDroppedInOnePoll(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta\n1700000002|$3|gamma|/tmp/gamma"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha"
	s.poll(context.Background())

	got := buf.String()
	if !strings.Contains(got, "session dropped: beta") {
		t.Errorf("got log %q, want beta", got)
	}
	if !strings.Contains(got, "session dropped: gamma") {
		t.Errorf("got log %q, want gamma", got)
	}
}

// The first poll of a process has no previous set to compare against. It must
// seed rather than report every session as dropped - or worse, report nothing
// ever because the seed was skipped.
func TestTheFirstPollLogsNoDrops(t *testing.T) {
	var buf bytes.Buffer
	s := droppedServer(t, &buf, func() string {
		return "1700000000|$1|alpha|/tmp/alpha"
	})

	s.poll(context.Background())

	if strings.Contains(buf.String(), "session dropped") {
		t.Errorf("got log %q, want no drops on the first poll", buf.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/daemon/ -run "TestPoll.*Dropped|TestPollWith|TestTheFirstPoll" -v`

Expected: `TestPollLogsADroppedSession` and `TestPollLogsEverySessionDroppedInOnePoll`
FAIL (no such log line). The three negative tests will PASS already — that is exactly why
they cannot stand alone, and Step 6's mutation check is what gives them meaning.

- [ ] **Step 3: Add the state field**

In `internal/daemon/daemon.go`, add to `Server`, immediately below the `clients`/`writers`
block and above `pendingEffects`:

```go
	// prevSessions is the previous poll's session names, for logDroppedSessions.
	// Owned by Run's goroutine like clients: poll is synchronous per tick and
	// nothing else touches it, which is the same argument that leaves the
	// collector's gitMemo unguarded. nil means no poll has succeeded yet.
	prevSessions map[string]bool
```

- [ ] **Step 4: Add `logDroppedSessions`**

In `internal/daemon/daemon.go`, next to `logSlowPoll`:

```go
// logDroppedSessions records each session that was in the previous poll and is
// not in this one. Daemon-only and not user-visible, for the same reason as
// logSlowPoll: a session going away is not a failure, and a self-polling client
// has no log. Unlike logSlowPoll this needs no rate limit - it is edge-triggered
// by something the user did, so one line per event is the right volume.
//
// The first successful poll seeds and reports nothing; there is no previous set
// to have dropped anything from.
func (s *Server) logDroppedSessions(sessions []*session.Session) {
	current := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		current[sess.Name] = true
	}
	if s.prevSessions != nil {
		for name := range s.prevSessions {
			if !current[name] {
				s.logf("session dropped: %s", name)
			}
		}
	}
	s.prevSessions = current
}
```

Add `"github.com/jzinkduda/vigil/internal/session"` to the file's imports if it is not
already there.

- [ ] **Step 5: Call it from `poll`**

In `internal/daemon/daemon.go`, in `poll`, immediately after the `if s.pollFailing`
recovery block and before `queue, queueHidden := s.Collector.Queue(sessions)`:

```go
	s.logDroppedSessions(sessions)
```

It must be after the `err != nil` early return. A failed collection returns before this
point, so a broken `gh` or `tmux` cannot be logged as every session vanishing.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/daemon/ -run "TestPoll.*Dropped|TestPollWith|TestTheFirstPoll" -v`

Expected: all five PASS.

- [ ] **Step 7: Run the full suite**

Run: `cd /Users/joshua.zink-duda/vigil && make test && make lint`

Expected: PASS. `-race` matters here specifically: `prevSessions` is claimed to need no
mutex, and the race detector is the only thing checking that claim.

- [ ] **Step 8: Mutation check**

1. Delete the `s.logDroppedSessions(sessions)` call from `poll`. Run all five tests.
   Confirm the two positive tests FAIL and paste the output.
2. Change the guard `if s.prevSessions != nil` to `if true` (i.e. drop the seed check) and
   run `TestTheFirstPollLogsNoDrops`. Confirm FAIL. This is what pins the first-poll
   behavior rather than leaving it to luck.
3. Move the call **above** the `err != nil` return, make the collector fail by pointing
   `listPanesKey`'s handler at an error, and confirm a failing poll logs no drops. If you
   cannot make this fail, say so in the report rather than claiming the placement is
   verified — the placement argument may be untestable through this harness, and an
   honest "not verified" is worth more than a green test that proves nothing.

Restore after each.

- [ ] **Step 9: Commit**

```bash
cd /Users/joshua.zink-duda/vigil
git add internal/daemon/daemon.go internal/daemon/dropped_test.go
git commit -m "feat(daemon): log when a session leaves the list"
```

---

## Task 4: Fix the `notify` hook default's quoting

**Files:**
- Modify: `/Users/joshua.zink-duda/vigil/internal/config/config.go:57`
- Modify: `/Users/joshua.zink-duda/vigil/internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.ExpandHook(template string, vars map[string]string) (string, error)`.
- Produces: nothing new. A corrected default string.

**Context you need.** The current default is:

```
tmux display-message -d 5000 "vigil: {session} → {new_state}"
```

`ExpandHook` shell-quotes every placeholder except `{flags}` (`rawPlaceholders`, and it
must not be widened). So `{session}` becomes a **single-quoted word placed inside the
hook's own double quotes**. Session names produced by dotfiles' `session_name_from_title`
contain literal double quotes — `SC-223374 Add bulk "Report Investigation" action` is a
live example from this machine's daemon log — and those close the outer double-quoted
string early. The result splits into two words, and `tmux display-message` takes at most
one:

```
command display-message: too many arguments (need at most 1)
```

Measured on 2026-08-03: dozens of these in `~/.local/state/vigil/vigild.log` and not one
successful fire. This hook has never worked.

The fix is adjacent quoting — closing the literal, letting the quoted placeholder stand on
its own, and reopening — so the shell concatenates the pieces into one word:

```
tmux display-message -d 5000 "vigil: "{session}" → "{new_state}
```

Verified: this yields the single argument
`vigil: SC-223374 Add bulk "Report Investigation" action → approved`.

Do not "fix" this by adding `session` to `rawPlaceholders`. That would pass an
unquoted session name to `sh` as syntax, which is what the quoting exists to prevent.

- [ ] **Step 1: Write the failing test**

Add to `/Users/joshua.zink-duda/vigil/internal/config/config_test.go`:

```go
// The default notify hook had never fired successfully: ExpandHook quotes each
// placeholder into its own shell word, the old default wrapped them in its own
// double quotes, and session names contain literal double quotes that closed
// that string early. tmux display-message then got two arguments and refused.
//
// The assertion runs the expanded string through a shell and counts the
// arguments it reduces to. Asserting on the expanded string itself would pass
// with the quoting still wrong, which is how this shipped in the first place.
func TestDefaultNotifyHookExpandsToOneShellArgument(t *testing.T) {
	cfg := &Config{}
	template := cfg.GetHook("notify")
	if template == "" {
		t.Fatal("no default notify hook")
	}

	const name = `SC-223374 Add bulk "Report Investigation" action`
	expanded, err := ExpandHook(template, map[string]string{
		"session":   name,
		"new_state": "approved",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Replace the tmux invocation with a printf that reports each argument it
	// received on its own line, then count the lines.
	script := strings.Replace(expanded, "tmux display-message -d 5000 ", `printf '%s\n' `, 1)
	if script == expanded {
		t.Fatalf("could not find the tmux prefix to substitute in %q", expanded)
	}

	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("running %q: %v", script, err)
	}
	args := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(args) != 1 {
		t.Fatalf("expanded to %d shell arguments, want 1: %q (from %q)", len(args), args, expanded)
	}
	want := "vigil: " + name + " → approved"
	if args[0] != want {
		t.Errorf("got argument %q, want %q", args[0], want)
	}
}
```

Add `"os/exec"` and `"strings"` to the test file's imports if absent.

If `GetHook` on a zero-valued `Config` does not return the default, read how the defaults
map is consulted and construct the `Config` the way the neighbouring tests in that file
do. Do not change `GetHook` to make the test pass.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/config/ -run TestDefaultNotifyHookExpandsToOneShellArgument -v`

Expected: FAIL with `expanded to 2 shell arguments, want 1`.

- [ ] **Step 3: Fix the default**

In `internal/config/config.go`, replace the `notify` default and add the comment that
stops the next reader "simplifying" it back:

```go
	// notify's quoting looks wrong and is not. ExpandHook substitutes each
	// placeholder as one shell-quoted word, so a placeholder inside a larger
	// double-quoted string lands as '...' within "..." - and a session name
	// containing a double quote (dotfiles' session_name_from_title produces
	// them) closes that string early, splitting the message into two arguments
	// that tmux display-message refuses. Closing the literal before each
	// placeholder and reopening after lets the shell concatenate the pieces
	// into the single argument tmux wants. Do not rewrite this as
	// "vigil: {session} → {new_state}"; that form has never worked.
	"notify":  `tmux display-message -d 5000 "vigil: "{session}" → "{new_state}`,
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/joshua.zink-duda/vigil && go test ./internal/config/ -run TestDefaultNotifyHookExpandsToOneShellArgument -v`

Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `cd /Users/joshua.zink-duda/vigil && make test && make lint`

Expected: PASS. If another test asserts the old default string verbatim, update it — and
note in the report that it existed, because a test pinning a broken default is worth
recording.

- [ ] **Step 6: Mutation check**

Restore the old default (`"vigil: {session} → {new_state}"`). Run the test. Confirm FAIL
with the argument count. Restore the fix. Confirm PASS. Paste both.

- [ ] **Step 7: Verify against the real daemon**

```bash
cd /Users/joshua.zink-duda/vigil && make build && make install
```

Then restart the daemon so it picks up the new binary — it never restarts itself by
design:

```bash
pkill -f 'vigil daemon'
```

A client will respawn it. Then drive a real transition (or wait for one) and check:

```bash
grep 'notify hook' ~/.local/state/vigil/vigild.log | tail -5
```

Expected: no new `too many arguments` lines, and a tmux message visible on screen. If no
transition happens within a few minutes, say so rather than claiming the verification
passed. Paste the tail either way.

- [ ] **Step 8: Commit**

```bash
cd /Users/joshua.zink-duda/vigil
git add internal/config/config.go internal/config/config_test.go
git commit -m "fix(config): make the default notify hook survive ExpandHook's quoting"
```

---

## Task 5: `tmux-hop`

**Files:**
- Create: `/Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop`

**Interfaces:**
- Consumes: `tmux`, `sed`, `sort`, `cut`, `awk`. **Not vigil.**
- Produces: the executable `tmux-hop`, accepting `next` | `prev` | a non-negative integer.
  Task 6 binds it.

**Context you need.** This replaces the two inline `run-shell` bodies at
`~/dotfiles/tmux/.tmux.conf:44-45`. Those bodies have three latent defects beyond the
ordering bug, and this script must not reproduce any of them:

1. `grep "^${current}$"` treats the session name as a **regex**. Names contain
   metacharacters — `SC-223374 Add bulk "Report Investigation" action` is live on this
   machine. `awk` compares as strings *because the operands are coerced* -
   `if ($0 "" == cur "")` - which removes the problem rather than escaping around it, and
   also removes the `grep -A1`/`-B1` context trick and both `[ "$x" = "$current" ]`
   wrap-around special cases. **Corrected after the fact: the brief below wrote
   `if ($0 == cur)`, and `==` on its own is not exact.** Both operands are strnums, and awk
   compares two strnums numerically when both look numeric, so `7` matched `007` and `1`
   matched `1.0`. The shipped script coerces with `""`.
2. `switch-client -t "$name"` is **not an exact match**. Without a `=` prefix tmux may
   resolve `SC-223477` against `SC-2234770`. This is the same load-bearing-prefix hazard
   already documented for `session.QueueItem.SessionPrefix()`'s trailing space in vigil.
3. `cut -d: -f2` **truncates a name containing a colon**.

   **Corrected 2026-08-03 - this brief overstated the fix.** `cut -d'|' -f2-` fixes
   **extraction** only. A colon-named session is untargetable by name on *any* tmux build,
   because `:` is tmux's own session:window target separator - on a fresh, non-sanitizing
   server, `has-session -t '=a.b:c[d+e]'` fails with `can't find session: a.b`, and `=`
   does not change that. The value of the fix is that the old code would have silently
   switched to a **different real session** named `a.b`, where the new code attempts the
   true full name and fails loudly. Scope is also narrower: `lib/tmux.sh`'s
   `session_name_from_title` already strips `:` and `.` at creation (lines 110-111), so
   only a hand-named session can reach this. See
   `docs/superpowers/2026-08-03-session-hopping-handoff.md`.

Ordering is by `session_id`, numerically, which tmux issues in increasing order and never
reuses. That is a total order with no ties, which is why there is no tie-break rule here
to keep in sync with vigil — the point of Task 2.

Follow the conventions of the neighbouring scripts in that directory: `#!/usr/bin/env
bash`, the `set -o errexit -o nounset -o pipefail` trio, a `usage` function, and
`source "${SCRIPT_DIR}/common.sh"` for `error`. Read `git-worktree-done` first as the
reference for the house style.

- [ ] **Step 1: Read the reference script**

Run: `sed -n '1,30p' /Users/joshua.zink-duda/dotfiles/scripts/scripts/git-worktree-done`

Match its header, `SCRIPT_DIR` resolution, `usage`, and `help_wanted` handling. Confirm
`common.sh` exports `error` before relying on it.

- [ ] **Step 2: Write the script**

Create `/Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop` with the Write tool
(never a heredoc):

```bash
#!/usr/bin/env bash
#
# tmux-hop - Switch tmux sessions by position in a stable, vigil-independent order
#
# Usage: tmux-hop next|prev|<n>
#
# Sessions are ordered by #{session_id} numerically. tmux issues session ids in
# increasing order and never reuses one, so this is a total order equal to
# creation order with no ties - which is why it needs no tie-break rule and
# cannot drift from what vigil's session table draws. vigil sorts by
# (session_created, session_id) for the same reason; see
# vigil/docs/superpowers/specs/2026-08-03-session-hopping-design.md.
#
# This script must never depend on vigil. tmux navigation has to work on a
# machine where vigil is not installed and no daemon is running.
#
# Examples:
#   tmux-hop next   # following session, wrapping at the end
#   tmux-hop prev   # preceding session, wrapping at the start
#   tmux-hop 0      # first session (indices are 0-based, matching vigil's
#                   # index column)

set -o errexit
set -o nounset
set -o pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

usage() {
  cat <<'USAGE'
Usage: tmux-hop next|prev|<n>

Switch to another tmux session by position in session-id order.

Arguments:
  next    The following session, wrapping at the end
  prev    The preceding session, wrapping at the start
  <n>     The n-th session, 0-based

Examples:
  tmux-hop next
  tmux-hop 3
USAGE
}

#######################################
# List session names in session-id order, one per line
# Outputs:
#   Session names to stdout, ordered
#######################################
ordered_sessions() {
  tmux list-sessions -F '#{session_id}|#{session_name}' \
    | sed 's/^\$//' \
    | sort -t'|' -k1,1n \
    | cut -d'|' -f2-
}

#######################################
# Resolve the session offset delta positions from the current one, wrapping
# Arguments:
#   Delta: 1 for next, -1 for prev
# Outputs:
#   The target session name to stdout
#######################################
neighbour() {
  local delta="${1}"
  local current
  current="$(tmux display-message -p '#{session_name}')"

  ordered_sessions | awk -v cur="${current}" -v delta="${delta}" '
    { n++; name[n] = $0; if ($0 == cur) idx = n }
    END {
      if (n == 0) { exit 1 }
      if (idx == 0) { print name[1]; exit 0 }
      t = idx + delta
      if (t > n) { t = 1 }
      if (t < 1) { t = n }
      print name[t]
    }
  '
}

#######################################
# Resolve the n-th session, 0-based
# Arguments:
#   Index
# Outputs:
#   The target session name to stdout, empty if out of range
#######################################
nth() {
  local n="${1}"
  ordered_sessions | sed -n "$((n + 1))p"
}

main() {
  if [[ "${#}" -ne 1 ]]; then
    usage >&2
    return 1
  fi

  local target
  case "${1}" in
    next) target="$(neighbour 1)" ;;
    prev) target="$(neighbour -1)" ;;
    *[!0-9]* | '')
      error "Not next, prev, or a non-negative integer: ${1}"
      return 1
      ;;
    *) target="$(nth "${1}")" ;;
  esac

  if [[ -z "${target}" ]]; then
    return 0
  fi

  # "=" makes this an exact match. Without it tmux may resolve SC-223477
  # against SC-2234770 - the same prefix hazard vigil documents for
  # QueueItem.SessionPrefix()'s trailing space.
  tmux switch-client -t "=${target}"
}

if help_wanted "${@}"; then
  usage
  exit 0
fi

main "${@}"
```

**The `awk` body above is wrong as written and was corrected on the branch.** It says
`if ($0 == cur)`; the shipped script says `if ($0 "" == cur "")`. Both operands are
strnums and awk compares two strnums numerically when both look numeric, so `7` matched
`007` and `1` matched `1.0`, `+1`, `" 1"`. Do not copy the body from here.

Note the `awk` program uses `idx == 0` for "current session not found" rather than
`!idx`, because an unset awk variable compares equal to 0 and 0 is never a valid
1-based index — the current session missing from the list means the caller is not in
tmux or the session was just killed, and starting from the first session is the safe
answer. Note also that an out-of-range `<n>` exits 0 silently: pressing `M-7` with four
sessions open should do nothing, not raise an error in a keybinding with nowhere to
display it.

- [ ] **Step 3: Make it executable**

```bash
chmod +x /Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop
```

- [ ] **Step 4: Verify the ordering matches vigil**

With at least three real sessions open:

```bash
/Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop --help
/Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop 0
tmux display-message -p '#{session_name}'
```

Then compare `ordered_sessions`' output against vigil's dashboard top-to-bottom:

```bash
tmux list-sessions -F '#{session_id}|#{session_name}' | sed 's/^\$//' | sort -t'|' -k1,1n | cut -d'|' -f2-
```

Paste both lists into the report. This requires Task 2 to have landed; if it has not, say
so and note that a mismatch on tied creation seconds is expected until it does.

- [ ] **Step 5: Verify the wrap and the awkward names**

```bash
tmux new-session -d -s 'hop.test:one "quoted"'
tmux new-session -d -s 'hop-prefix'
tmux new-session -d -s 'hop-prefix-longer'
```

Then, by hand from a tmux client:

- `tmux-hop next` repeatedly, once per session plus one, and confirm it returns to where
  it started rather than stopping or erroring.
- `tmux-hop prev` likewise in the other direction.
- From `hop-prefix`, confirm `tmux-hop next` lands somewhere real and that switching to
  `hop-prefix` never lands on `hop-prefix-longer`. This is defect 2.
- Confirm the session named `hop.test:one "quoted"` is reachable — it exercises the regex
  metacharacter (defect 1) and the colon (defect 3) at once. **Corrected: a colon-named
  session is not reachable by `-t` on any tmux build (see defect 3 above), so the
  observable outcome for the colon half is that it fails loudly with tmux's own
  `can't find session:` rather than switching to the wrong session.**

Clean up:

```bash
tmux kill-session -t '=hop.test:one "quoted"'
tmux kill-session -t '=hop-prefix'
tmux kill-session -t '=hop-prefix-longer'
```

Paste the sequence of session names you landed on into the report. If any step misbehaves,
fix the script — do not record it as a limitation.

- [ ] **Step 6: Commit**

```bash
cd /Users/joshua.zink-duda/dotfiles
git add scripts/scripts/tmux-hop
git commit -m "feat(tmux): add tmux-hop for session-id ordered session switching"
```

---

## Task 6: Bind the keys

**Files:**
- Modify: `/Users/joshua.zink-duda/dotfiles/tmux/.tmux.conf:44-45` (replace) and the
  surrounding `# Session` block (add)

**Interfaces:**
- Consumes: `tmux-hop` from Task 5.
- Produces: `M-j`, `M-k`, `M-0`..`M-9`, `M-o`.

**Context you need.** `M-0`..`M-9` are **0-based**, matching vigil's index column
(`view.indexCol` renders the loop index, blank past 9), so `M-0` is the first row. Ten
literal bindings rather than a loop, because `.tmux.conf` has no loop construct.

No `M-<digit>` binding exists today — the file's only `M-` bindings are `M-C-h`, `M-C-l`,
`M-h`, `M-l`, `M-Space`, `M-j` and `M-k` — so there is nothing to collide with. Confirm
that yourself in Step 1 rather than trusting this paragraph.

`M-o` covers the **current session only**, by design. Opening another session's PR is
`M-3 M-o`, two keystrokes now that hopping is one, and it keeps the binding stateless with
no index resolution and no vigil data. `gh pr view --web` in the pane's own directory is
all it needs.

- [ ] **Step 1: Confirm there is nothing to collide with**

Run: `grep -n 'bind.*M-' /Users/joshua.zink-duda/dotfiles/tmux/.tmux.conf`

Expected: no `M-` followed by a digit. If there is one, stop and report it rather than
overwriting a binding.

- [ ] **Step 2: Replace the two inline bindings**

In `/Users/joshua.zink-duda/dotfiles/tmux/.tmux.conf`, delete the two long `run-shell`
lines at 44-45 and put this in their place:

```
# Session hopping. Order is #{session_id}, which vigil's session table also
# sorts by (as the tie-break under session_created) so the panel's index column
# and M-<n> always agree. tmux-hop never invokes vigil: this has to work with
# vigil uninstalled. See vigil/docs/superpowers/specs/2026-08-03-session-hopping-design.md
bind -n M-j run-shell -b '$HOME/scripts/tmux-hop next'
bind -n M-k run-shell -b '$HOME/scripts/tmux-hop prev'

# Jump straight to a row of vigil's session table. 0-based, matching its index
# column, which is blank past 9 - so is this.
bind -n M-0 run-shell -b '$HOME/scripts/tmux-hop 0'
bind -n M-1 run-shell -b '$HOME/scripts/tmux-hop 1'
bind -n M-2 run-shell -b '$HOME/scripts/tmux-hop 2'
bind -n M-3 run-shell -b '$HOME/scripts/tmux-hop 3'
bind -n M-4 run-shell -b '$HOME/scripts/tmux-hop 4'
bind -n M-5 run-shell -b '$HOME/scripts/tmux-hop 5'
bind -n M-6 run-shell -b '$HOME/scripts/tmux-hop 6'
bind -n M-7 run-shell -b '$HOME/scripts/tmux-hop 7'
bind -n M-8 run-shell -b '$HOME/scripts/tmux-hop 8'
bind -n M-9 run-shell -b '$HOME/scripts/tmux-hop 9'

# Open the current session's PR. Current session only, on purpose: another
# session's PR is M-3 M-o, which keeps this binding stateless and needs no
# vigil data. The braces are so a failed cd also reports rather than silently
# doing nothing.
bind -n M-o run-shell -b '{ cd "#{pane_current_path}" && gh pr view --web; } || tmux display-message "no PR for this branch"'
```

Verify `$HOME/scripts` is the right path for that directory before committing — the
existing bindings in this file use `$HOME/scripts/git-worktree-done` and
`$HOME/scripts/vigil-panel`, so it should be, but confirm the symlink or copy that
`install.sh` creates actually puts `tmux-hop` there:

```bash
ls -l "$HOME/scripts/tmux-hop"
```

If it is absent, find how `install.sh` links that directory and follow the same mechanism
rather than hand-copying the file.

- [ ] **Step 3: Reload and smoke-test**

```bash
tmux source-file ~/.tmux.conf
```

Then, by hand:

- `M-j` and `M-k` move one session at a time and wrap.
- `M-0` through however many sessions you have each land on the row vigil's panel draws at
  that index. Keep the panel visible while checking.
- A digit past the session count does nothing, silently.
- `M-o` in a session with a PR opens it in a browser.
- `M-o` in a session with no PR shows `no PR for this branch` in the tmux status line
  rather than doing nothing.

  **Corrected 2026-08-03: that fallback is not unconditional.** A literal double quote in
  `#{pane_current_path}` - not a single quote, which is inert inside `sh`'s double quotes -
  makes the expanded command an `sh` parse error, and a parse error swallows both sides of
  the `||`, so the message never appears either. Do not describe the `{ ...; } ||` grouping
  as a guarantee anywhere.

- [ ] **Step 4: Confirm no vigil dependency**

The claim is that this works with vigil absent, and it is worth actually checking rather
than reading the script:

```bash
grep -n vigil /Users/joshua.zink-duda/dotfiles/scripts/scripts/tmux-hop
```

Expected: only comment lines. Then confirm the bindings themselves name no vigil binary:

```bash
grep -n 'M-[0-9jko]' /Users/joshua.zink-duda/dotfiles/tmux/.tmux.conf | grep -c vigil
```

Expected: `0`. Paste both results.

- [ ] **Step 5: Commit**

```bash
cd /Users/joshua.zink-duda/dotfiles
git add tmux/.tmux.conf
git commit -m "feat(tmux): bind M-j/M-k, M-0..M-9 and M-o for session hopping"
```

---

## Task 7: Temporary timing instrumentation

**Files:**
- Modify: `/Users/joshua.zink-duda/dotfiles/scripts/scripts/git-worktree-done`
  (`main`, four points)
- Modify: `/Users/joshua.zink-duda/dotfiles/scripts/scripts/git-worktree-cleanup`
  (`main`, three points)

**Interfaces:**
- Consumes: nothing.
- Produces: appended lines in `/tmp/vigil-hop-timing.log`, format
  `<epoch-seconds.millis> <label>`.

**Context you need, including that this is meant to be removed.** Complaint 1 — a session
taking "a few seconds" to leave vigil's list after `prefix d` — has **no confirmed cause**.
Every component measures fast:

| gap | measured 2026-08-03 |
|---|---|
| ~~`slow poll` lines in the entire daemon log~~ | ~~0~~ **WRONG - it was at least nine, one of them 11.1s** |
| `tmux display-popup -E` startup | 0.01-0.02s |
| cleanup invoke → session gone from `tmux list-sessions` | 0.272s |
| tmux drops the session → daemon snapshot drops the row | 0.968s (the 1s-cadence floor) |

The first row is false and was false when it was written; the four timing rows stand. The
same error is corrected at Task 8 Step 2 below, and in full in the handoff's "Complaint 1".

That sums to ~1.3s, not "a few seconds". This task adds the timestamps that turn one real
`prefix d` into a timeline. **It fixes nothing.** The fix gets its own spec, written
against what this measures.

The leading untested hypothesis is popup occlusion: `kill_tmux_session` runs early in
cleanup's `main` (`git-worktree-cleanup:206`, before any worktree work), but the popup
stays up for the *remainder* of cleanup — `git worktree remove`, mise cleanup, branch
deletion — so the row may already be gone by the first moment it can be looked at. If the
largest gap in the timeline sits between the daemon's `session dropped` line and
`cleanup finished`, that hypothesis is confirmed and the fix belongs in dotfiles.

macOS `date` has no `%N`, so use `perl -MTime::HiRes`. `perl` is already a dependency of
`git-worktree-cleanup`'s path resolution, so this adds nothing new.

- [ ] **Step 1: Add the stamp helper to `git-worktree-done`**

In `/Users/joshua.zink-duda/dotfiles/scripts/scripts/git-worktree-done`, after the
`source "${SCRIPT_DIR}/common.sh"` line:

```bash
# TEMPORARY - diagnostic instrumentation for vigil's session-removal latency.
# Remove once docs/superpowers/specs/2026-08-03-session-hopping-design.md part C
# has a timeline. See that spec before deleting, in case it is still open.
readonly HOP_TIMING_LOG="/tmp/vigil-hop-timing.log"
stamp() {
  printf '%s %s\n' \
    "$(perl -MTime::HiRes -e 'printf "%.3f", Time::HiRes::time')" \
    "${1}" >> "${HOP_TIMING_LOG}"
}
```

- [ ] **Step 2: Add the four call sites in `git-worktree-done`**

In its `main`, place `stamp` calls at exactly these points:

- as the first statement of `main`: `stamp 'done: main entry'`
- immediately after the `tmux switch-client -c "${current_client}" -t "=${target_session}"`
  line: `stamp 'done: switch-client returned'`
- immediately before the `tmux display-popup` invocation:
  `stamp 'done: opening popup'`
- immediately after the `display-popup` invocation, before `return 0`:
  `stamp 'done: popup closed'`

Read the file first and place them against what is actually there; the line numbers are
not quoted here because Step 1 shifts them.

- [ ] **Step 3: Add the helper and three call sites in `git-worktree-cleanup`**

Same `stamp` helper, added after that script's `source "${SCRIPT_DIR}/common.sh"` line
with the same TEMPORARY comment. Then in its `main`:

- as the first statement of `main`: `stamp 'cleanup: main entry'`
- immediately after `kill_tmux_session "${session_name}"`:
  `stamp 'cleanup: kill-session returned'`
- immediately before each `return 0` in `main` — there are two, the
  directory-already-gone early return and the final one:
  `stamp 'cleanup: finished'`

Both returns need it, or a run that takes the early exit produces a timeline with no end.

- [ ] **Step 4: Verify the stamps land without breaking anything**

Use the same safe invocation the design's measurement used — a throwaway session and a
directory that does not exist, so cleanup kills the session and takes its early return
without touching a real worktree:

```bash
\rm -f /tmp/vigil-hop-timing.log
tmux new-session -d -s vigil-timing-check
/Users/joshua.zink-duda/dotfiles/scripts/scripts/git-worktree-cleanup \
  --session vigil-timing-check /tmp/vigil-definitely-not-a-dir
cat /tmp/vigil-hop-timing.log
```

Expected: three lines — `cleanup: main entry`, `cleanup: kill-session returned`,
`cleanup: finished` — with monotonically increasing timestamps. Confirm the session is
gone:

```bash
tmux has-session -t '=vigil-timing-check' 2>&1 || echo "gone, as expected"
```

Paste the log contents into the report.

- [ ] **Step 5: Commit**

```bash
cd /Users/joshua.zink-duda/dotfiles
git add scripts/scripts/git-worktree-done scripts/scripts/git-worktree-cleanup
git commit -m "chore(scripts): add temporary timing stamps for session-removal latency"
```

- [ ] **Step 6: Collect one real timeline**

This step needs the user and cannot be done for them. Ask them to press `prefix d` in a
real worktree session once, then collect both halves:

```bash
cat /tmp/vigil-hop-timing.log
grep 'session dropped' ~/.local/state/vigil/vigild.log | tail -5
```

Task 3 must be built and installed for the second half to produce anything
(`make install` and a daemon restart). Interleave the two by timestamp — the daemon log
uses wall-clock `2026/08/03 14:02:42`, the stamps use epoch seconds, so convert one:

```bash
perl -e 'printf "%s\n", scalar localtime($ARGV[0])' <epoch>
```

Report the assembled timeline and name the largest gap. **Do not fix it in this task.**
Write down which side owns it and stop.

---

## Task 8: Documentation

**Files:**
- Create: `/Users/joshua.zink-duda/vigil/docs/superpowers/2026-08-03-session-hopping-handoff.md`
- Modify: `/Users/joshua.zink-duda/vigil/CLAUDE.md`

**Interfaces:** none.

**Context you need.** `CLAUDE.md`'s own standing warning is that phase 6's whole-branch
review returned eight findings and **all eight were in the prose about the changes rather
than in a change**. Two mattered, and both had been "verified" by a reviewer who
re-derived the claim and stopped one call frame early. So: every mechanism claim in this
documentation must be traced to the line that decides it, and the handoff must record
what was *not* verified as plainly as what was.

- [ ] **Step 1: Write the handoff**

Create `docs/superpowers/2026-08-03-session-hopping-handoff.md` covering:

- What landed, per task, with merge SHAs in both repositories.
- **The measurements**, verbatim from the reports: the `session_id` ordering comparison
  against a real dashboard, the awkward-name and prefix checks from Task 5, and the real
  `notify` hook fire from Task 4.
- **A verification-limits section.** At minimum: the two orders agreeing is checked by
  hand and by nothing automated, and it silently breaks if the user presses `s` or `f` in
  vigil; `M-<n>` past the session count is silent by design and indistinguishable from a
  broken binding; `tmux-hop`'s correctness rests on `session_id` being monotonic in
  creation time, which is true of tmux and asserted nowhere.
- **Complaint 1's status**: unreproduced, instrumented, not fixed. Include the
  measurement table and whatever timeline Task 7 Step 6 produced. If it produced none,
  say that.
- The `git-worktree-done` / `git-worktree-cleanup` stamps are **temporary** and where the
  instruction to remove them lives.

- [ ] **Step 2: Update `CLAUDE.md`**

Three edits, no more:

1. In **"What is open"**, remove nothing and add the two items this work leaves behind:
   that vigil's order and the tmux bindings' order agree only while the user does not
   press `s` or `f`, with nothing detecting a divergence; and that complaint 1 is
   instrumented but uncaused.
2. In **"Key Conventions"**, add a bullet for the `(Created, ID)` comparator: why it is
   two keys and not one, that `ID` 0 means a cache-hydrated session, and that
   `~/dotfiles/scripts/scripts/tmux-hop` is the other half of the contract and must never
   invoke vigil. Add a second bullet for the `notify` hook's adjacent quoting, pointing at
   `ExpandHook`'s one-word-per-placeholder guarantee as the reason the readable form
   cannot work.
3. In the merge-record table's surrounding prose, note that this work is **not** a seventh
   phase — the file says plainly that there is no phase 7, and this is a defect-and-feature
   batch, not a continuation of that design.

Do not restructure the file. Do not touch the `fillGit` demotion note.

**Corrected after the fact - this instruction was wrong and must not be followed.** It
originally said to add, in one sentence, "that 2026-08-03's zero `slow poll` lines over a
full day is a third data point consistent with the demotion." **There were never zero.**
Re-running the grep found **nine** lines in that day's log, including
`14:30:27 slow poll: 11.1s total, 11.085s in git`, which predates the design file by seven
minutes. So the number was false when the design asserted it, and it is not a data point
for the demotion in either direction. The design and the handoff have been corrected; this
was the last copy on the branch still stating it as fact. See "Complaint 1" in
`docs/superpowers/2026-08-03-session-hopping-handoff.md`.

- [ ] **Step 3: Verify the claims you just wrote**

For each mechanism claim in both documents, open the file and line that decides it and
confirm. Specifically:

- `view.indexCol` renders the loop index over the sorted list, so the index column really
  does follow `SortCreated` — check `internal/view/table.go`, `renderRow`'s `index`
  parameter and `RenderTable`'s loop, not just `indexCol` itself.
- `rawPlaceholders` still contains only `flags` after Task 4.
- `tmux-hop` contains no `vigil` invocation.

Paste the confirming lines. This step is the one phase 6 skipped.

- [ ] **Step 4: Commit**

```bash
cd /Users/joshua.zink-duda/vigil
git add CLAUDE.md docs/superpowers/2026-08-03-session-hopping-handoff.md
git commit -m "docs: record session hopping, the order contract, and what is unverified"
```

---

## Final: whole-branch review

Per `CLAUDE.md`, per-task reviews are good and cannot see a seam. Budget a whole-branch
review that **explicitly distrusts the suite** and samples tests nobody flagged. The seams
this work creates, in priority order:

1. **`fetch.ListSessions`' field positions.** Task 1 shifts four indices in a parse whose
   whole design is "do not use a fixed field count". A reviewer should re-derive the
   `flagStart` arithmetic from scratch against a line with a piped path *and* all three
   flags, not read the diff.
2. **The order contract across two repositories.** vigil sorts `(Created, ID)`; `tmux-hop`
   sorts `ID`. The claim that these are the same order rests on `session_created` being
   monotonic in `session_id`. A reviewer should try to construct a case where they differ
   and report whether they could.
3. **`prevSessions` without a mutex.** The claim is that `poll` is the only writer. A
   reviewer should find every caller of `poll` and confirm none is on another goroutine.
4. **The `notify` default under a session name containing a single quote**, not just a
   double quote — `shellQuote`'s `'\''` escape is the untested half.

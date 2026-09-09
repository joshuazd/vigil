# Dirty counts off the publication path - Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `git status --porcelain` off `Collector.Snapshot` into a poller, so a slow
worktree can no longer delay the daemon's poll loop and, through it, a poke rebroadcast.

**Architecture:** `internal/collect`'s tickerless scheduler is relocated and renamed from
`remote` to `scheduler`, and `Collector` holds two of them: `remote` (PR, story, review
pollers) and `local` (a new `dirtyPoller`). `fetch.FetchGitStatus` splits into
`FetchGitMeta` - root, branch, unpushed, rebase age, all measured at or under 30ms and all
still synchronous inside `Snapshot` - and `FetchDirtyCounts`, which the new poller runs on its
own goroutines, keyed by git root rather than pane path.

**Tech Stack:** Go 1.x, no new dependencies. `go test -race ./...` via `make test`.

**Spec:** `docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md`

## Global Constraints

- `make test` is `go test -race ./...`. `-race` is not optional here: the whole change is a
  concurrency claim.
- `make lint` is `golangci-lint` and must pass.
- **Every test below gets its subject deleted, the failure watched, and the output pasted into
  the task report.** Nineteen briefs across three plans in this repository have contained tests
  that would pass with their subject deleted. This plan's author is not exempt.
- Where this plan and the shipped code disagree, **the code is right**. Re-derive, do not read.
- No code comments except where the meaning cannot be inferred from the code.
- Never use the em dash. Plain `-`.
- Commit after each task's tests are green.

## Corrections this plan makes to the spec

The spec was written 2026-08-03 and shelved. Three points do not survive contact:

1. **Spec test 5 ("`fillGit` runs before `dirty.track`") cannot "fail only itself."** The spec
   asks for a test that fails when the two lines are swapped and demands no other test fail
   with it. That is unachievable, and the reason is worth stating rather than papering over:
   `Snapshot` builds fresh `*session.Session` values every call, so with the lines reversed
   `s.Git.GitRoot` is empty at `track` time on *every* poll, not just the first. The working set
   is then permanently empty and no count ever lands, so tests 1-4 and 6-8 fail too. Test 5's
   value is that it names the invariant in one line, so the failure is legible; it is a
   diagnostic, not an isolator. Task 3 keeps the test and drops the claim.
2. **A branchless worktree gains dirty counts, and that is a deliberate behaviour change.**
   `FetchGitStatus` returns early when `git branch --show-current` is empty, so a detached HEAD
   - which is what an in-progress rebase looks like - shows no `~N +N -N` today. The spec's
   working set is "the deduped set of `s.Git.GitRoot`", which includes such a root. Task 2 makes
   the change explicitly, with a test, rather than letting task 3 smuggle it in.
3. **The spec's "Had the `dirtyPoller` been built, its last pass duration would belong here
   too"** is promoted to a task. Once git is off the poll loop the `slow poll` line goes quiet,
   and with it the only number anyone has ever been able to read off this machine. Task 5
   replaces it with `slow dirty pass`.

Spec test 10 - a poll exceeding `Interval` logs once with the breakdown, and a second slow poll
inside the rate-limit window logs nothing - **already ships**, as
`internal/daemon/slowpoll_test.go`'s four tests. Nothing to do for it; task 5 gives the dirty
pass the same three.

---

### Task 1: Relocate and rename the scheduler

Pure refactor. No behaviour changes, no new tests. It exists as its own task because a
reviewer can reject the rename while approving everything after it, and because doing it
first keeps task 3 a diff about the poller rather than a diff about both.

The struct is renamed because after task 3 it schedules local subprocess pollers as well as
off-box ones, and a type called `remote` scheduling a `git status` loop is a lie. `newRemote`
becomes `newScheduler`. `Collector.RefreshRemote` is **not** renamed: it has 46 test call sites
across three packages and is load-bearing vocabulary in six documents, and after task 3 it
still refreshes exactly the remote scheduler, so the name stays true.

**Files:**
- Create: `internal/collect/scheduler.go`
- Create: `internal/collect/scheduler_test.go`
- Modify: `internal/collect/remote.go` (remove the `poller` interface and the `remote` struct)
- Modify: `internal/collect/collect.go:71` (field type), `internal/collect/collect.go:145`
- Modify: `internal/collect/remote_test.go` (move the scheduler tests and `countingPoller` out)
- Modify: `CLAUDE.md` (one test name)

**Interfaces:**
- Consumes: nothing.
- Produces: `type poller interface { pass(ctx context.Context); invalidate() }`,
  `type scheduler struct{...}`, `func newScheduler(pollers ...poller) *scheduler`, and the
  methods `(*scheduler).start(ctx context.Context)`, `.wait()`, `.nudge()`, `.invalidate()`,
  `.refresh(ctx context.Context)`.

- [ ] **Step 1: Create `internal/collect/scheduler.go`**

Move the `poller` interface and the `remote` struct out of `remote.go` verbatim, renaming the
struct to `scheduler`, the constructor to `newScheduler` and the receiver from `r` to `s`. The
doc comments are widened to cover both instances - that widening is the only prose change.

```go
package collect

import (
	"context"
	"sync"
)

// A poller owns one class of data fetched off the publication path: its own
// store, its own locking, and its own idea of what is due. Nothing it does can
// block Snapshot.
//
// pass runs one fetch cycle and returns immediately when nothing is due.
// invalidate drops due-ness so the next pass refetches.
type poller interface {
	pass(ctx context.Context)
	invalidate()
}

// scheduler runs pollers, one goroutine each so a slow poller cannot delay
// another.
//
// It has no ticker, and that is load-bearing rather than a simplification.
// Cadence comes from whoever calls Snapshot, which nudges at the end of every
// call. A daemon-fed client never calls Snapshot - startPoll refuses while a
// daemon is connected - so its workers block for the life of the process and
// spend nothing. On the remote scheduler that is what "one daemon means one gh
// budget" rests on. On the local one it is what stops every open panel running
// its own git status loop against the monorepo, which is the worse of the two
// because no rate limit would ever notice it happening.
type scheduler struct {
	pollers []poller
	wakes   []chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
}

func newScheduler(pollers ...poller) *scheduler {
	s := &scheduler{pollers: pollers}
	for range pollers {
		s.wakes = append(s.wakes, make(chan struct{}, 1))
	}
	return s
}

// start is idempotent rather than fatal on a second call: a client that loses
// and regains a daemon can reach it more than once, and a second set of
// workers would double the fetch rate for one collector. sync.Once, not a
// bool: that path is reachable from more than one goroutine, and an
// unsynchronized bool would race the read/write on itself.
//
// The Once makes ctx sticky: the first caller's context is the only one the
// workers ever select on, so it must outlive the process's use of this
// scheduler. Starting with an already-cancelled one leaves the workers
// permanently stopped and every later start a no-op.
func (s *scheduler) start(ctx context.Context) {
	s.once.Do(func() {
		for i, p := range s.pollers {
			p, wake := p, s.wakes[i]
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-wake:
					}
					p.pass(ctx)
				}
			}()
		}
	})
}

func (s *scheduler) wait() { s.wg.Wait() }

// nudge wakes every worker without blocking. The channels are cap-1, so a
// nudge that arrives while a pass is running coalesces into the one already
// queued and the worker re-checks the moment it finishes.
func (s *scheduler) nudge() {
	for _, wake := range s.wakes {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *scheduler) invalidate() {
	for _, p := range s.pollers {
		p.invalidate()
	}
}

// refresh runs one pass of every poller on the caller's goroutine. The workers
// are a scheduler over this, and it is the seam a test drives so it never has
// to race a goroutine. Production reaches a pass only through start.
func (s *scheduler) refresh(ctx context.Context) {
	for _, p := range s.pollers {
		p.pass(ctx)
	}
}
```

- [ ] **Step 2: Delete the moved code from `internal/collect/remote.go`**

Remove the `poller` interface block and the whole `remote` struct with its `newRemote`,
`start`, `wait`, `nudge`, `invalidate` and `refresh` methods. `remote.go` keeps `prWorkers`,
`prPoller`, `prEntry`, `branchKey`, `dueBranch`, `newPRPoller` and prPoller's methods. Leave
its imports alone: `context`, `sync`, `time`, `fetch` and `session` are all still used by
`prPoller`.

- [ ] **Step 3: Point `Collector` at the new name**

In `internal/collect/collect.go`, the field:

```go
	// remote schedules prs and its siblings.
	remote *scheduler
```

and in `New`:

```go
	c.remote = newScheduler(pollers...)
```

- [ ] **Step 4: Move the scheduler tests into `internal/collect/scheduler_test.go`**

Cut `countingPoller`, `newCountingPoller`, its `pass`/`invalidate`/`count`/`invalidateCount`
methods, `waitForPass`, and the seven tests `TestRemoteNudgeWakesEveryWorker`,
`TestRemoteRunsNothingWithoutANudge`, `TestRemoteStartIsIdempotent`,
`TestRemoteRefreshRunsEveryPollerSynchronously`, `TestRemoteInvalidateReachesEveryPoller`,
`TestRemoteWaitBlocksUntilAnInFlightPassFinishes`,
`TestRemoteNudgeDoesNotBlockWithNoWorkerDraining` and
`TestRemoteNudgeDuringAPassCoalescesIntoTheNext` out of `remote_test.go` and into
`scheduler_test.go`, unchanged except that `newRemote` becomes `newScheduler`, the local
variable `r` becomes `s`, and each test's `TestRemote` prefix becomes `TestScheduler`.

The file needs `context`, `sync`, `testing` and `time`. `remote_test.go` keeps `config`,
`fetch` and `session` for the prPoller tests; drop any import that goes unused.

`TestSchedulerRunsNothingWithoutANudge` keeps its existing comment verbatim - the caveat that
its 200ms window only catches a fast ticker is exactly as true for the local scheduler.

- [ ] **Step 5: Update the one document that names a moved test**

`CLAUDE.md` names `TestRemoteRunsNothingWithoutANudge` in the "remote pollers have no ticker"
convention bullet. Change that occurrence to `TestSchedulerRunsNothingWithoutANudge`. Leave
`TestADaemonFedClientSpendsNoGhBudget` alone - it did not move.

```bash
grep -rn "TestRemoteRunsNothingWithoutANudge" . --include='*.md' --include='*.go'
```

Expected after the edit: no hits.

- [ ] **Step 6: Verify**

```bash
make test && make lint
```

Expected: PASS, with the same test count as before the task minus zero - every moved test still
runs, under its new name.

- [ ] **Step 7: Commit**

```bash
git add internal/collect/scheduler.go internal/collect/scheduler_test.go internal/collect/remote.go internal/collect/remote_test.go internal/collect/collect.go CLAUDE.md
git commit -m "refactor(collect): rename remote to scheduler and relocate it

It is about to schedule a local subprocess poller as well as the off-box
ones, and a type called remote doing that is a lie. No behaviour change.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Split `FetchGitStatus` into `FetchGitMeta` and `FetchDirtyCounts`

Still one synchronous call site: `fillGit` calls both, back to back, so the product behaves as
it does today. The split is renamed rather than quietly narrowed because a `FetchGitStatus`
that returns a struct with three fields silently left at zero is a trap, and making the one
production caller a compile error is the forcing function.

One deliberate behaviour change lands here (correction 2 above): a worktree whose
`git branch --show-current` is empty - a detached HEAD, which is what an in-progress rebase
looks like - gets dirty counts now, where `FetchGitStatus`'s early return gave it none. It is
the state where the counts matter most, and after task 3 the extra subprocess is off the
publication path anyway.

**Files:**
- Modify: `internal/fetch/git.go:17-45` (`FetchGitStatus`), `internal/fetch/git.go:60-89`
  (`parsePorcelain`)
- Modify: `internal/collect/collect.go:266` (inside `fillGit`'s fan-out)
- Test: `internal/fetch/git_test.go:10-67`
- Test: `internal/collect/collect_test.go` (one new test)

**Interfaces:**
- Consumes: nothing from task 1.
- Produces:
  - `func FetchGitMeta(ctx context.Context, cmd Commander, panePath string) session.GitStatus` -
    fills `Branch`, `GitRoot`, `Unpushed`, `RebaseAgeSecs`; leaves `Modified`, `Added`,
    `Deleted` at zero.
  - `func FetchDirtyCounts(ctx context.Context, cmd Commander, gitRoot string) (modified, added, deleted int)`
  - `FetchGitStatus` and `parsePorcelain` no longer exist.

- [ ] **Step 1: Write the failing tests**

Replace `TestFetchGitStatusBasic`, `TestFetchGitStatusNoGitRoot` and
`TestParsePorcelainStatuses` in `internal/fetch/git_test.go` with these four. The XY-code
assertions are unchanged from `TestParsePorcelainStatuses`; only the call moves.

```go
func TestFetchGitMetaResolvesBranchAndUnpushed(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "/repo", nil)
	mock.OnArgs("git branch --show-current", "feat-branch", nil)
	mock.OnArgs("git rev-parse --verify origin/feat-branch", "abc123", nil)
	mock.OnArgs("git rev-list origin/feat-branch..HEAD --count", "2", nil)
	mock.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	mock.OnArgs("git merge-base origin/main HEAD", "def456", nil)
	mock.OnArgs("git log -1 --format=%ct def456", fmt.Sprintf("%d", 1000000000), nil)

	status := FetchGitMeta(context.Background(), mock, "/repo")
	if status.Branch != "feat-branch" {
		t.Errorf("branch: got %q", status.Branch)
	}
	if status.GitRoot != "/repo" {
		t.Errorf("gitRoot: got %q, want /repo", status.GitRoot)
	}
	if status.Unpushed != 2 {
		t.Errorf("unpushed: got %d, want 2", status.Unpushed)
	}
	if status.RebaseAgeSecs == nil {
		t.Error("rebase age: got nil, want a value")
	}
}

// The counts are the poller's job from here on. A FetchGitMeta that quietly
// filled them would put status --porcelain back on the publication path with
// nothing else in the suite noticing.
func TestFetchGitMetaRunsNoStatusCall(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "/repo", nil)
	mock.OnArgs("git branch --show-current", "feat-branch", nil)
	mock.OnArgs("git --no-optional-locks status --porcelain", " M file1.go\n?? file2.go", nil)

	status := FetchGitMeta(context.Background(), mock, "/repo")
	if status.Modified != 0 || status.Added != 0 || status.Deleted != 0 {
		t.Errorf("got counts ~%d +%d -%d, want all zero", status.Modified, status.Added, status.Deleted)
	}
	n := mock.CallCountFunc(func(c MockCall) bool {
		return c.Name == "git" && len(c.Args) > 1 && c.Args[1] == "status"
	})
	if n != 0 {
		t.Errorf("got %d status calls, want 0", n)
	}
}

func TestFetchGitMetaNoGitRoot(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "", fmt.Errorf("not a git repo"))

	status := FetchGitMeta(context.Background(), mock, "/not-a-repo")
	if status.Branch != "" {
		t.Errorf("expected empty branch, got %q", status.Branch)
	}
	if status.GitRoot != "" {
		t.Errorf("expected empty gitRoot, got %q", status.GitRoot)
	}
}

func TestFetchDirtyCountsEveryXYCode(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git --no-optional-locks status --porcelain",
		"M  file1\n M file2\nMM file3\nA  file4\n?? file5\nD  file6\n D file7\nRM file8\nC  file9", nil)

	m, a, d := FetchDirtyCounts(context.Background(), mock, "/repo")
	// M, " M", MM = 3 modified; RM x='R' adds 1, y='M' adds 1 = 5
	if m != 5 {
		t.Errorf("modified: got %d, want 5", m)
	}
	// A, ??, C = 3 added
	if a != 3 {
		t.Errorf("added: got %d, want 3", a)
	}
	// D, " D" = 2 deleted
	if d != 2 {
		t.Errorf("deleted: got %d, want 2", d)
	}
}

// A detached HEAD - which is what an in-progress rebase looks like - has no
// branch, and FetchGitStatus's early return meant it showed no counts at all.
// FetchDirtyCounts only needs a root, so that gap closes. Pinned because it is
// a deliberate behaviour change, not a side effect.
func TestFetchDirtyCountsNeedsNoBranch(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git --no-optional-locks status --porcelain", " M file1.go\n?? file2.go", nil)

	m, a, _ := FetchDirtyCounts(context.Background(), mock, "/repo")
	if m != 1 || a != 1 {
		t.Errorf("got ~%d +%d, want ~1 +1", m, a)
	}
}
```

Add one test to `internal/collect/collect_test.go` pinning that the two halves still meet on
the publication path in this task - it is what makes task 3's removal of the second call a
visible diff rather than an invisible one:

```go
// fillGit still runs both halves in this task, so a Snapshot alone fills the
// counts. Task 3 moves the second half to a poller and this test moves with
// it; until then it is what says the split did not lose anything.
func TestSnapshotStillFillsDirtyCountsSynchronously(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)

	c := New(noQueueConfig(), cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := sessions[0].Git
	if got.Modified != 1 || got.Added != 1 || got.Deleted != 1 {
		t.Errorf("got ~%d +%d -%d, want ~1 +1 -1", got.Modified, got.Added, got.Deleted)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/fetch/ ./internal/collect/ -run 'FetchGitMeta|FetchDirtyCounts|SnapshotStillFillsDirtyCounts' -v
```

Expected: the `internal/fetch` package fails to build with `undefined: FetchGitMeta` and
`undefined: FetchDirtyCounts`. `TestSnapshotStillFillsDirtyCountsSynchronously` passes already
- it describes today's behaviour, and that is the point of it.

- [ ] **Step 3: Split the function**

In `internal/fetch/git.go`, replace `FetchGitStatus` with:

```go
// FetchGitMeta resolves everything about panePath's repository except the
// dirty counts: the root, the branch, how many commits are unpushed, and how
// stale the branch's merge base is.
//
// These stay on the publication path because they are cheap and because
// Snapshot's later stages need them: groupByBranchRoot feeds prPoller's
// working set from Branch and GitRoot, dirtyPoller keys its working set on
// GitRoot, and transition.Detect reads both off the event. Timed across five
// real polls on 2026-08-03, none of the calls below ever crossed 30ms.
func FetchGitMeta(ctx context.Context, cmd Commander, panePath string) session.GitStatus {
	gitRoot := gitRoot(ctx, cmd, panePath)
	if gitRoot == "" {
		return session.GitStatus{}
	}

	branch := currentBranch(ctx, cmd, gitRoot)
	if branch == "" {
		return session.GitStatus{GitRoot: gitRoot}
	}

	return session.GitStatus{
		Branch:        branch,
		GitRoot:       gitRoot,
		Unpushed:      unpushedCount(ctx, cmd, gitRoot, branch),
		RebaseAgeSecs: rebaseAge(ctx, cmd, gitRoot, branch),
	}
}
```

and rename `parsePorcelain` to `FetchDirtyCounts`, body unchanged, with this doc comment:

```go
// FetchDirtyCounts counts modified, added and deleted paths in gitRoot. It is
// effectively the whole cost of collecting git state: 0.7-1.6s per worktree on
// 2026-07-31, 1348ms cold at ~/sc-231121 on 2026-09-09, against under 30ms for
// every call FetchGitMeta makes. How slow it is is a property of the
// worktree's index rather than of vigil - core.untrackedCache and
// core.fsmonitor put it under 40ms warm - which is why it is a poller's job
// and not Snapshot's.
//
// It takes a git root rather than a pane path because that is where the
// command runs, and because two sessions in one worktree then share an answer.
//
// A failed call returns zeros, indistinguishable from a clean tree. That is
// what it has always done and nothing gates on the difference.
func FetchDirtyCounts(ctx context.Context, cmd Commander, gitRoot string) (modified, added, deleted int) {
```

- [ ] **Step 4: Fix the one production caller**

In `internal/collect/collect.go`, inside `fillGit`'s fan-out:

```go
	runParallel(due, c.GitWorkers, func(d *dueGit) {
		fetchStart := time.Now()
		d.s.Git = fetch.FetchGitMeta(ctx, c.Cmd, d.s.PanePath)
		if d.s.Git.GitRoot != "" {
			d.s.Git.Modified, d.s.Git.Added, d.s.Git.Deleted = fetch.FetchDirtyCounts(ctx, c.Cmd, d.s.Git.GitRoot)
		}
		d.took = time.Since(fetchStart)
	})
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
make test
```

Expected: PASS across every package.

- [ ] **Step 6: Mutation-check each new test**

Required, and paste the output into the task report. For each of the five new tests, break its
subject, watch it fail, restore:

| Test | Mutation |
|---|---|
| `TestFetchGitMetaResolvesBranchAndUnpushed` | return `session.GitStatus{GitRoot: gitRoot}` unconditionally |
| `TestFetchGitMetaRunsNoStatusCall` | add the `FetchDirtyCounts` call back inside `FetchGitMeta` |
| `TestFetchGitMetaNoGitRoot` | drop the `if gitRoot == ""` early return |
| `TestFetchDirtyCountsEveryXYCode` | delete the `case 'R', 'U':` arm |
| `TestFetchDirtyCountsNeedsNoBranch` | make `FetchDirtyCounts` return early when the root has no branch |
| `TestSnapshotStillFillsDirtyCountsSynchronously` | delete the `FetchDirtyCounts` line from `fillGit` |

- [ ] **Step 7: Commit**

```bash
git add internal/fetch/git.go internal/fetch/git_test.go internal/collect/collect.go internal/collect/collect_test.go
git commit -m "refactor(fetch): split FetchGitStatus into FetchGitMeta and FetchDirtyCounts

Renamed rather than narrowed: a FetchGitStatus with three fields silently
left at zero is a trap, and the compile error is the forcing function.
Both halves still run synchronously in fillGit.

A branchless worktree - a detached HEAD, ie an in-progress rebase - now
gets dirty counts, where the old early return gave it none.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `dirtyPoller`, and take the counts off the publication path

The change itself.

**Files:**
- Create: `internal/collect/dirty.go`
- Create: `internal/collect/dirty_test.go`
- Modify: `internal/collect/collect.go` - the `Collector` struct, `New`, `Start`, `Wait`,
  `Invalidate`, `Snapshot`; add `RefreshDirty`
- Modify: `internal/collect/collect_test.go` - `TestSnapshotStillFillsDirtyCountsSynchronously`
  is deleted here, replaced by dirty_test.go's tests 1 and 2

**Interfaces:**
- Consumes: `newScheduler` (task 1), `fetch.FetchDirtyCounts` (task 2).
- Produces:
  - `func (c *Collector) RefreshDirty(ctx context.Context)` - one pass of the local scheduler
    on the caller's goroutine, the test seam mirroring `RefreshRemote`.
  - unexported: `type dirtyPoller`, `func newDirtyPoller(c *Collector) *dirtyPoller`, and its
    `track(sessions []*session.Session)`, `fill(sessions []*session.Session)`, `pass`,
    `invalidate`.
  - `Collector.dirty *dirtyPoller` and `Collector.local *scheduler`.
- Unchanged and relied on by later tasks: `Collector.Start`, `.Wait`, `.Invalidate`,
  `.RefreshRemote` keep their signatures.

- [ ] **Step 1: Write the failing tests**

Create `internal/collect/dirty_test.go`. These are spec tests 1-8. Test 5 is the invariant
diagnostic described in "Corrections" above - it is expected to fail alongside its neighbours
when the ordering is broken, and the plan does not pretend otherwise.

```go
package collect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
)

// countStatusCalls counts `git --no-optional-locks status --porcelain`, the
// one subprocess this whole change exists to move.
func countStatusCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "git" && len(c.Args) == 3 &&
			c.Args[0] == "--no-optional-locks" && c.Args[1] == "status" && c.Args[2] == "--porcelain"
	})
}

// twoSessionsOneRootCommander puts two tmux sessions inside one git root, so a
// store keyed by root costs one status call where one keyed by pane path costs
// two.
func twoSessionsOneRootCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|alpha|/repo/shared\n1700000001|$2|beta|/repo/shared/sub", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/shared", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)
	return cmd
}

// Test 1. The core claim: publication no longer waits on status --porcelain.
func TestSnapshotIssuesNoStatusCalls(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := countStatusCalls(cmd); got != 0 {
		t.Errorf("got %d status calls from a Snapshot, want 0: dirty counts must not block publication", got)
	}
}

// Test 2. And the counts still arrive, one pass later.
func TestCountsLandOnTheSnapshotAfterAPass(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if g := first[0].Git; g.Modified != 0 || g.Added != 0 || g.Deleted != 0 {
		t.Errorf("first Snapshot: got ~%d +%d -%d, want all zero before any pass", g.Modified, g.Added, g.Deleted)
	}

	c.RefreshDirty(ctx)

	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	g := second[0].Git
	if g.Modified != 1 || g.Added != 1 || g.Deleted != 1 {
		t.Errorf("second Snapshot: got ~%d +%d -%d, want ~1 +1 -1", g.Modified, g.Added, g.Deleted)
	}
}

// Test 3. Pins what did NOT move. Relocating unpushedCount or rebaseAge into
// the poller must fail this.
func TestOneSnapshotStillFillsGitMetaWithNoPass(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git rev-parse --verify origin/feature", "abc123", nil)
	cmd.OnArgs("git rev-list origin/feature..HEAD --count", "3", nil)
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.OnArgs("git merge-base origin/main HEAD", "def456", nil)
	cmd.OnArgs("git log -1 --format=%ct def456", "1000000000", nil)

	c := New(noQueueConfig(), cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	g := sessions[0].Git
	if g.Branch != "feature" {
		t.Errorf("branch: got %q, want feature", g.Branch)
	}
	if g.GitRoot != "/repo/alpha" {
		t.Errorf("gitRoot: got %q, want /repo/alpha", g.GitRoot)
	}
	if g.Unpushed != 3 {
		t.Errorf("unpushed: got %d, want 3", g.Unpushed)
	}
	if g.RebaseAgeSecs == nil {
		t.Error("rebase age: got nil, want a value")
	}
}

// Test 4. Keyed by git root, not pane path.
func TestTwoSessionsInOneRootCostOneStatusCall(t *testing.T) {
	cmd := twoSessionsOneRootCommander()

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	if got := countStatusCalls(cmd); got != 1 {
		t.Errorf("got %d status calls for two sessions in one root, want 1", got)
	}

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	for _, s := range sessions {
		if s.Git.Modified != 1 || s.Git.Added != 1 || s.Git.Deleted != 1 {
			t.Errorf("%s: got ~%d +%d -%d, want ~1 +1 -1 - both sessions share the root's answer",
				s.Name, s.Git.Modified, s.Git.Added, s.Git.Deleted)
		}
	}
}

// Test 5. The ordering invariant, named so a failure is legible.
//
// The spec asked for a test that fails alone when fillGit and dirty.track are
// swapped. That is not achievable and the plan says so: Snapshot builds fresh
// sessions every call, so with the lines reversed s.Git.GitRoot is empty at
// track time on every poll, the working set is permanently empty, and every
// other test in this file fails too. This one exists to say which line moved.
func TestFillGitRunsBeforeDirtyTrack(t *testing.T) {
	cmd := singleBranchCommander()

	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	c.dirty.mu.Lock()
	working := append([]string(nil), c.dirty.working...)
	c.dirty.mu.Unlock()

	if len(working) != 1 || working[0] != "/repo/alpha" {
		t.Errorf("got working set %v, want [/repo/alpha]: dirty.track must run after fillGit, which is what puts GitRoot on the session", working)
	}
}

// Test 6. A root that left the working set is pruned at write-back, including
// on a pass where nothing was due.
func TestDirtyPassPrunesEvenWhenNothingWasDue(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	c.dirty.mu.Lock()
	_, present := c.dirty.entries["/repo/alpha"]
	c.dirty.mu.Unlock()
	if !present {
		t.Fatal("fixture is broken: the first pass stored nothing to prune")
	}

	c.dirty.track(nil)
	c.RefreshDirty(ctx)

	c.dirty.mu.Lock()
	n := len(c.dirty.entries)
	c.dirty.mu.Unlock()
	if n != 0 {
		t.Errorf("got %d entries after the root left the working set, want 0", n)
	}
}

// Test 7. An invalidate landing between a pass reading the working set and
// writing it back leaves the entry due, so a forced refresh is not satisfied
// by an answer that may predate it. Mirrors prPoller's gen test.
func TestDirtyPassDoesNotSatisfyAnInvalidateThatLandsDuringIt(t *testing.T) {
	cmd := singleBranchCommander()
	entered := make(chan struct{})
	release := make(chan struct{})
	cmd.HandlerFuncs["git --no-optional-locks status --porcelain"] = func(context.Context, string, []string) (string, error) {
		close(entered)
		<-release
		return " M a.go", nil
	}

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.dirty.pass(ctx)
	}()

	<-entered
	c.dirty.invalidate()
	close(release)
	<-done

	c.dirty.mu.Lock()
	e, ok := c.dirty.entries["/repo/alpha"]
	c.dirty.mu.Unlock()
	if !ok {
		t.Fatal("the entry was dropped, not left due")
	}
	if !e.fetchedAt.IsZero() {
		t.Errorf("got fetchedAt %v, want the zero time: an invalidate during the fetch must leave the entry due", e.fetchedAt)
	}
}

// Test 8. -race. Snapshot reads the dirty store while a pass writes it.
func TestSnapshotAndRefreshDirtyAreRaceFree(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = 0

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := c.Snapshot(ctx); err != nil {
				t.Errorf("Snapshot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			c.RefreshDirty(ctx)
		}
	}()
	wg.Wait()
}
```

`noQueueConfig()`, `singleBranchCommander()` and `New` come from `collect_test.go` in the same
package, which is why `config` is not imported here.

Also delete `TestSnapshotStillFillsDirtyCountsSynchronously` from
`internal/collect/collect_test.go` - test 1 is its inverse and they cannot both be true.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/collect/ -run 'Dirty|StatusCalls|CountsLand|FillGitRunsBefore|OneSnapshotStillFillsGitMeta|TwoSessionsInOneRoot' -v
```

Expected: build failure - `c.RefreshDirty undefined`, `c.dirty undefined`.

- [ ] **Step 3: Create `internal/collect/dirty.go`**

```go
package collect

import (
	"context"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// dirtyPoller owns exactly three fields - Modified, Added, Deleted - and
// fetches them off the publication path.
//
// The name is the boundary: it is not a git poller. Branch, GitRoot, Unpushed
// and RebaseAgeSecs stay synchronous inside Snapshot, because groupByBranchRoot
// feeds prPoller's working set from the first two, this poller keys its own
// working set on GitRoot, and transition.Detect reads both off the event.
// Moving them would delay PR resolution by a tick and blank the branch on the
// first poll, for no gain: they are not the latency source. If either of the
// other two ever measures slow it can join this poller without anything here
// changing shape.
//
// Keyed by git root, not by pane path: status --porcelain runs in the root, so
// two sessions inside one worktree ask the same question and one subprocess
// answers both.
//
// Structurally a sibling of prPoller, deliberately, down to the gen counter and
// the write-back prune.
type dirtyPoller struct {
	c *Collector

	// passMu makes a pass single-flight. The scheduler gives one goroutine per
	// poller, but RefreshDirty can be called from another, and two concurrent
	// passes would spend two status calls per root for one result. Held across
	// the fetch; mu is not.
	passMu sync.Mutex

	// mu guards entries, working and gen, and is held only for the map work at
	// either end of a pass.
	mu      sync.Mutex
	entries map[string]dirtyEntry
	working []string

	// gen counts invalidations. A pass reads it alongside the working set and
	// compares again at write-back: if it moved, an invalidate landed while the
	// fetch was in flight and the fetch's answer may predate it.
	gen uint64
}

type dirtyCounts struct{ modified, added, deleted int }

type dirtyEntry struct {
	counts    dirtyCounts
	fetchedAt time.Time
}

type dueRoot struct {
	root   string
	counts dirtyCounts
}

func newDirtyPoller(c *Collector) *dirtyPoller {
	return &dirtyPoller{c: c, entries: make(map[string]dirtyEntry)}
}

// track posts the working set: the deduped git roots the most recent Snapshot
// saw. Latest wins, as with prPoller.
//
// A session with no git root - not a repository, or git unreachable - is
// excluded and gets zero counts, which is what it gets today.
func (p *dirtyPoller) track(sessions []*session.Session) {
	seen := make(map[string]struct{}, len(sessions))
	working := make([]string, 0, len(sessions))
	for _, s := range sessions {
		root := s.Git.GitRoot
		if root == "" {
			continue
		}
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		working = append(working, root)
	}
	p.mu.Lock()
	p.working = working
	p.mu.Unlock()
}

// fill writes each session's counts from the store.
//
// There is no pending flag and that is a decision, not an omission. PRPending
// exists because transition.Detect must tell "never resolved" from "known to
// have no PR"; nothing gates on a dirty count - Session.State never reads one
// and GitStatus.IsClean has no non-test caller in the tree - so no transition,
// notify hook or auto_cleanup can be provoked by a count arriving a tick late.
//
// The accepted cost is a one-tick zero: at daemon start, and when a session
// first appears, the Git column renders unpushed and rebase age but no
// ~N +N -N until the first pass for that root lands.
func (p *dirtyPoller) fill(sessions []*session.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range sessions {
		e, ok := p.entries[s.Git.GitRoot]
		if !ok {
			continue
		}
		s.Git.Modified, s.Git.Added, s.Git.Deleted = e.counts.modified, e.counts.added, e.counts.deleted
	}
}

// invalidate makes every root due without dropping what is known, matching
// prPoller. Zeroing rather than dropping keeps the last known counts on screen
// through a forced refresh instead of blanking the column while the pass runs.
func (p *dirtyPoller) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	for k, e := range p.entries {
		e.fetchedAt = time.Time{}
		p.entries[k] = e
	}
}

func (p *dirtyPoller) pass(ctx context.Context) {
	p.passMu.Lock()
	defer p.passMu.Unlock()

	now := p.c.now()
	interval := p.c.GitInterval

	p.mu.Lock()
	startGen := p.gen
	var due []*dueRoot
	for _, root := range p.working {
		if prev, ok := p.entries[root]; ok && now.Sub(prev.fetchedAt) < interval {
			continue
		}
		due = append(due, &dueRoot{root: root})
	}
	p.mu.Unlock()

	if len(due) > 0 {
		// GitWorkers (8) rather than prWorkers (4): prWorkers is low because
		// each gh call spends a rate-limited quota, and a local subprocess has
		// no quota. 8 is what fillGit already fanned out to.
		runParallel(due, p.c.GitWorkers, func(d *dueRoot) {
			d.counts.modified, d.counts.added, d.counts.deleted = fetch.FetchDirtyCounts(ctx, p.c.Cmd, d.root)
		})
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Prune to the working set as it stands now, not as it stood when the
	// fetch started: a root that vanished mid-fetch must not survive, and its
	// result must not be written back. This runs even when nothing was due, or
	// a root dropped between two passes that both found nothing else to do
	// would linger indefinitely.
	live := make(map[string]struct{}, len(p.working))
	for _, root := range p.working {
		live[root] = struct{}{}
	}
	next := make(map[string]dirtyEntry, len(p.working))
	for root, e := range p.entries {
		if _, ok := live[root]; ok {
			next[root] = e
		}
	}

	invalidated := p.gen != startGen
	for _, d := range due {
		if _, ok := live[d.root]; !ok {
			continue
		}
		fetchedAt := now
		if invalidated {
			fetchedAt = time.Time{}
		}
		next[d.root] = dirtyEntry{counts: d.counts, fetchedAt: fetchedAt}
	}
	p.entries = next
}
```

- [ ] **Step 4: Wire it into `Collector`**

In `internal/collect/collect.go`, add two fields beside `remote`:

```go
	// dirty holds `git status --porcelain` counts per git root. Snapshot posts
	// its working set and reads it; the fetching happens on the local
	// scheduler's worker goroutine.
	dirty *dirtyPoller

	// remote schedules prs and its siblings; local schedules dirty.
	//
	// Two schedulers rather than one list because folding dirty into remote
	// would make RefreshRemote a lie, and that name has 46 test call sites
	// across three packages. Two of them cost one extra goroutine and keep
	// both names honest.
	remote *scheduler
	local  *scheduler
```

In `New`, after `c.remote = newScheduler(pollers...)`:

```go
	c.dirty = newDirtyPoller(c)
	c.local = newScheduler(c.dirty)
```

`Start`, `Wait` and a new `RefreshDirty`:

```go
func (c *Collector) Start(ctx context.Context) {
	c.remote.start(ctx)
	c.local.start(ctx)
}

func (c *Collector) Wait() {
	c.remote.wait()
	c.local.wait()
}

// RefreshDirty runs one dirtyPoller pass on the caller's goroutine, the local
// half of what RefreshRemote does. Same reason: a test drives a pass here
// instead of racing a goroutine, and production reaches one only through Start.
func (c *Collector) RefreshDirty(ctx context.Context) { c.local.refresh(ctx) }
```

`Start`'s doc comment currently says "runs the remote pollers' workers"; widen the first
sentence to "runs the pollers' workers - the remote ones and the local git one" and leave the
rest, including the sticky-context paragraph, unchanged.

`Wait`'s doc comment says "with a gh child still running"; make it "with a gh or git child
still running".

`Invalidate`, whose middle paragraph stops being true:

```go
// Invalidate drops the git memo and makes every poller entry due, so a caller
// that just changed state - a merge, a draft toggle, the Refresh key - does
// not have to wait out git_interval or pr_interval.
//
// Git metadata - root, branch, unpushed, rebase age - comes back inside the
// next Snapshot, because fillGit is still synchronous. Dirty counts and remote
// data come back a tick later, when the passes this nudges have landed.
//
// The git memo must only ever be dropped from the same goroutine as Snapshot:
// gitMemo is not guarded by a lock. Both poller halves are safe from anywhere.
func (c *Collector) Invalidate() {
	c.gitMemo = nil
	c.remote.invalidate()
	c.local.invalidate()
	c.remote.nudge()
	c.local.nudge()
}
```

- [ ] **Step 5: Take the counts off `Snapshot`**

Drop the `FetchDirtyCounts` call from `fillGit`'s fan-out, restoring it to:

```go
	runParallel(due, c.GitWorkers, func(d *dueGit) {
		fetchStart := time.Now()
		d.s.Git = fetch.FetchGitMeta(ctx, c.Cmd, d.s.PanePath)
		d.took = time.Since(fetchStart)
	})
```

and add the two poller lines to `Snapshot`, between `fillGit` and `groupByBranchRoot`:

```go
	c.fillGit(ctx, sessions)

	// dirty.track must stay below fillGit: the working set is derived from
	// s.Git.GitRoot, which only exists once meta has landed. Reversed, the
	// poller never has anything to do and every count renders zero forever -
	// on every poll, not just the first, because Snapshot builds fresh
	// sessions each call.
	c.dirty.track(sessions)
	c.dirty.fill(sessions)

	// Everything past here is local. The PR store is read as it stands and the
	// workers are nudged to refresh it; whatever they fetch is published by
	// the next Snapshot, at most one tick later. Nothing here blocks on the
	// network or on a subprocess, which is the whole contract.
	branches := groupByBranchRoot(sessions)
	c.prs.track(branches)
	c.prs.fill(branches)
	c.remote.nudge()
	c.local.nudge()
	return sessions, nil
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make test
```

Expected: PASS. If `internal/model` or `internal/daemon` tests that assert a git call count now
fail, read them before editing: a test that counted `git rev-parse --show-toplevel` still means
what it did (`countGitCalls`'s comment says so), but a test that counted every `git` call has
lost one per due path and its number is now wrong for a good reason.

- [ ] **Step 7: Mutation-check every test in `dirty_test.go`**

Required, output pasted into the task report.

| Test | Mutation | Expected |
|---|---|---|
| `TestSnapshotIssuesNoStatusCalls` | put `FetchDirtyCounts` back in `fillGit` | FAIL, `got 1 status calls` |
| `TestCountsLandOnTheSnapshotAfterAPass` | delete the `c.dirty.fill(sessions)` line | FAIL on the second Snapshot |
| `TestOneSnapshotStillFillsGitMetaWithNoPass` | move `unpushedCount` out of `FetchGitMeta` | FAIL, `unpushed: got 0` |
| `TestTwoSessionsInOneRootCostOneStatusCall` | key `entries`/`working` by `PanePath` | FAIL, `got 2 status calls` |
| `TestFillGitRunsBeforeDirtyTrack` | swap `c.fillGit` and `c.dirty.track` | FAIL - and note in the report which other tests fail with it |
| `TestDirtyPassPrunesEvenWhenNothingWasDue` | move the prune inside `if len(due) > 0` | FAIL, `got 1 entries` |
| `TestDirtyPassDoesNotSatisfyAnInvalidateThatLandsDuringIt` | delete the `invalidated` check, always write `now` | FAIL, non-zero fetchedAt |
| `TestSnapshotAndRefreshDirtyAreRaceFree` | drop `p.mu` from `fill` | FAIL under `-race` |

- [ ] **Step 8: Commit**

```bash
git add internal/collect/dirty.go internal/collect/dirty_test.go internal/collect/collect.go internal/collect/collect_test.go
git commit -m "perf(collect): fetch dirty counts off the publication path

git status --porcelain was effectively the whole cost of a poll - 1.3-2.0s
sustained at ~/sc-231121 on 2026-09-09, ~99% of each slow poll, across
25,145 slow poll lines. Because poll and handleRequest share Run's single
goroutine, that also delayed a poke rebroadcast, so the current-session
highlight took 1-2s to move after a session switch.

It now runs on a dirtyPoller behind the same tickerless scheduler the
remote pollers use, keyed by git root so two sessions in one worktree cost
one subprocess. Counts arrive a tick late; nothing gates on them, so no
transition, notify hook or auto_cleanup can fire from one being stale.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: A daemon-fed client spawns no git subprocesses

Spec test 9, and the spec's own landmine note about it: **this is the only thing standing
between the change and per-panel `git status` loops.** It has to go through a real
`Collector.Start` rather than `RefreshDirty` - per the collector-async-remote handoff, a nudge
that never reaches a worker leaves every `RefreshDirty`-driven test green. Do not weaken it to
"assert `Start` was called".

Its own task because it lives in a different package from task 3 and because the spec asks for
it to get a reviewer's gate of its own.

**Files:**
- Test: `internal/model/collect_cmd_test.go` (append)

**Interfaces:**
- Consumes: everything task 3 produced. Adds nothing.

- [ ] **Step 1: Write the failing test**

Append to `internal/model/collect_cmd_test.go`. It is the shape of
`TestADaemonFedClientSpendsNoGhBudget`, which sits directly above it, for the same reasons:
self-poll once so there is a working set at all, then attach a daemon and assert nothing more
is spent.

```go
// countStatusCalls counts `git --no-optional-locks status --porcelain`.
func countStatusCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "git" && len(c.Args) == 3 &&
			c.Args[0] == "--no-optional-locks" && c.Args[1] == "status" && c.Args[2] == "--porcelain"
	})
}

// TestADaemonFedClientSpawnsNoGitSubprocesses is to the local scheduler what
// TestADaemonFedClientSpendsNoGhBudget is to the remote one, and it matters
// more: a ticker on the remote side burns a quota somebody eventually notices,
// while a ticker here gives every open panel its own git status loop against
// the monorepo with no rate limit to notice it at all.
//
// It has to go through a real Collector.Start. A nudge that never reaches a
// worker leaves every RefreshDirty-driven test in internal/collect green, so
// those cannot stand in for this one.
//
// GitInterval is zeroed for the same reason PRInterval is zeroed above: at the
// default 3s a woken pass inside the assertion window would find nothing due
// and spend nothing, and a ticker would sail through.
func TestADaemonFedClientSpawnsNoGitSubprocesses(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.collector.GitInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.collector.Start(ctx)

	if _, err := m.collector.Snapshot(ctx); err != nil {
		t.Fatalf("the self-polling snapshot failed: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for countStatusCalls(cmd) < 1 {
		select {
		case <-deadline:
			t.Fatal("the self-polling client never ran a dirty pass: this fixture cannot detect a ticker")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Hardcoded rather than latched off the count so far, following the gh
	// test above. One Snapshot posts one root, so one pass spends exactly one
	// status call, and unlike gh nothing else in this binary shares a
	// package-level cache that could change that number.
	const selfPolledStatus = 1
	if got := countStatusCalls(cmd); got != selfPolledStatus {
		t.Fatalf("got %d status calls from the self-polling client, want %d", got, selfPolledStatus)
	}

	m.daemonConn = &fakeConn{}
	if got := m.startPoll(false); got != nil {
		t.Fatal("a daemon-fed client issued a poll of its own")
	}
	time.Sleep(300 * time.Millisecond)

	if got := countStatusCalls(cmd); got != selfPolledStatus {
		t.Errorf("got %d status calls once a daemon was feeding this client, want %d: only Snapshot may wake a poller, and the local scheduler must have no ticker",
			got, selfPolledStatus)
	}
}
```

- [ ] **Step 2: Run it to verify it passes, and verify it can fail**

```bash
go test ./internal/model/ -race -run TestADaemonFedClientSpawnsNoGitSubprocesses -v
```

Expected: PASS.

Then the mutation, which is the whole point of the test. In `internal/collect/scheduler.go`,
give `start`'s worker loop a ticker:

```go
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-wake:
				}
				p.pass(ctx)
			}
```

Re-run. Expected: FAIL with `got N status calls once a daemon was feeding this client, want 1`.
Paste the output into the task report, then revert the ticker.

Also confirm the second failure mode: comment out `m.collector.Start(ctx)` and re-run.
Expected: FAIL at `the self-polling client never ran a dirty pass`, which is what proves the
test goes through the real worker rather than a synchronous refresh. Restore.

- [ ] **Step 3: Full suite**

```bash
make test && make lint
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/model/collect_cmd_test.go
git commit -m "test(model): a daemon-fed client spawns no git subprocesses

The local scheduler's missing ticker is what stops every open panel
running its own git status loop against the monorepo. Nothing else in the
suite would notice one being added.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Keep the machine measurable, and update the documents

Once git is off the poll loop the `slow poll` line goes quiet, and with it the only number
anyone has ever been able to read off the machine where this happens. The spec anticipated
this: "Had the `dirtyPoller` been built, its last pass duration would belong here too, since a
pass exceeding `git_interval` means the counts are staler than intended even though it no
longer blocks publication."

`DirtyStats` is mutex-guarded rather than goroutine-owned, unlike `GitStats`: the pass that
writes it runs on the scheduler's worker while `daemon.poll` reads it from Run's goroutine.

**Files:**
- Modify: `internal/collect/dirty.go` (a `stats` field, written in `pass`)
- Modify: `internal/collect/collect.go` (`DirtyStats` type and accessor)
- Modify: `internal/daemon/daemon.go` (a `lastSlowDirtyLog` field, `logSlowDirtyPass`, one call
  in `poll`)
- Test: `internal/collect/dirty_test.go`, `internal/daemon/slowpoll_test.go`
- Modify: `CLAUDE.md`, `docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md`

**Interfaces:**
- Consumes: `dirtyPoller` (task 3).
- Produces: `type DirtyStats struct { Total, Slowest time.Duration; SlowestRoot string }` and
  `func (c *Collector) DirtyStats() DirtyStats`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/collect/dirty_test.go`:

```go
func TestDirtyPassReportsItsSlowestRoot(t *testing.T) {
	const delay = 60 * time.Millisecond
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|fast|/tmp/fast\n1700000001|$2|slow|/tmp/slow", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(_ context.Context, dir string, _ []string) (string, error) {
			return dir, nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
		"git --no-optional-locks status --porcelain": func(_ context.Context, dir string, _ []string) (string, error) {
			if dir == "/tmp/slow" {
				time.Sleep(delay)
			}
			return " M a.go", nil
		},
	}

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	stats := c.DirtyStats()
	if stats.SlowestRoot != "/tmp/slow" {
		t.Errorf("slowest root: got %q, want /tmp/slow", stats.SlowestRoot)
	}
	if stats.Slowest < delay {
		t.Errorf("slowest: got %s, want at least %s", stats.Slowest, delay)
	}
	if stats.Total < delay {
		t.Errorf("total: got %s, want at least %s", stats.Total, delay)
	}
}

// Zero means no work, matching GitStats. A pass that timed an empty fan-out
// and reported a few hundred nanoseconds would make that false, which is the
// exact bug GitStats shipped with and a test caught.
func TestDirtyStatsAreZeroWhenNothingWasDue(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)
	if c.DirtyStats().Total == 0 {
		t.Fatal("the first pass should have measured a fan-out")
	}

	c.RefreshDirty(ctx)
	if got := c.DirtyStats(); got != (DirtyStats{}) {
		t.Errorf("got %+v, want the zero value", got)
	}
}
```

Append to `internal/daemon/slowpoll_test.go`:

```go
// slowDirtyServer builds a Server whose dirty pass spends at least delay on one
// root. The pass runs on the caller's goroutine via RefreshDirty, so the test
// never races a worker.
func slowDirtyServer(t *testing.T, delay time.Duration, buf *bytes.Buffer) (*Server, func()) {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|slow|/tmp/slow", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(_ context.Context, dir string, _ []string) (string, error) {
			return dir, nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
		"git --no-optional-locks status --porcelain": func(context.Context, string, []string) (string, error) {
			time.Sleep(delay)
			return " M a.go", nil
		},
	}

	c := collect.New(&config.Config{}, cmd)
	c.GitInterval = delay / 4

	s := &Server{
		Collector: c,
		Interval:  time.Hour, // so the poll itself is never slow and only the dirty line can appear
		Log:       log.New(buf, "", 0),
	}
	return s, func() { c.RefreshDirty(context.Background()) }
}

func TestPollLogsASlowDirtyPass(t *testing.T) {
	var buf bytes.Buffer
	s, runPass := slowDirtyServer(t, 60*time.Millisecond, &buf)

	ctx := context.Background()
	s.poll(ctx) // posts the working set
	runPass()
	s.poll(ctx)

	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, "slow dirty pass") {
		t.Fatalf("got log %q, want a slow dirty pass line", line)
	}
	if !strings.Contains(line, "/tmp/slow") {
		t.Errorf("got log %q, want it to name the slowest root", line)
	}
}

func TestASecondSlowDirtyPassInsideTheRateLimitWindowLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	s, runPass := slowDirtyServer(t, 60*time.Millisecond, &buf)
	clock := time.Unix(1700000000, 0)
	s.clock = func() time.Time { return clock }

	ctx := context.Background()
	s.poll(ctx)
	runPass()
	s.poll(ctx)
	clock = clock.Add(slowPollLogInterval - time.Second)
	runPass()
	s.poll(ctx)

	if n := strings.Count(buf.String(), "slow dirty pass"); n != 1 {
		t.Errorf("got %d slow dirty pass lines in %q, want 1", n, buf.String())
	}
}

// Without this, a rate limit that only ever logged once would still satisfy
// the test above.
func TestASlowDirtyPassLogsAgainOnceTheRateLimitWindowHasPassed(t *testing.T) {
	var buf bytes.Buffer
	s, runPass := slowDirtyServer(t, 60*time.Millisecond, &buf)
	clock := time.Unix(1700000000, 0)
	s.clock = func() time.Time { return clock }

	ctx := context.Background()
	s.poll(ctx)
	runPass()
	s.poll(ctx)
	clock = clock.Add(slowPollLogInterval + time.Second)
	runPass()
	s.poll(ctx)

	if n := strings.Count(buf.String(), "slow dirty pass"); n != 2 {
		t.Errorf("got %d slow dirty pass lines in %q, want 2", n, buf.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

```bash
go test ./internal/collect/ ./internal/daemon/ -run 'DirtyStats|DirtyPassReports|SlowDirty' -v
```

Expected: build failure - `c.DirtyStats undefined`, `undefined: DirtyStats`.

- [ ] **Step 3: Record the pass measurement**

In `internal/collect/dirty.go`, add a `stats` field under `mu`:

```go
	// stats is the last pass's measurement. Guarded by mu rather than
	// goroutine-owned like GitStats, because the worker writes it while
	// daemon.poll reads it from Run's goroutine.
	stats DirtyStats
```

and in `pass`, replace the fan-out block and the top of the write-back with:

```go
	var stats DirtyStats
	if len(due) > 0 {
		// GitWorkers (8) rather than prWorkers (4): prWorkers is low because
		// each gh call spends a rate-limited quota, and a local subprocess has
		// no quota. 8 is what fillGit already fanned out to.
		start := time.Now()
		runParallel(due, p.c.GitWorkers, func(d *dueRoot) {
			fetchStart := time.Now()
			d.counts.modified, d.counts.added, d.counts.deleted = fetch.FetchDirtyCounts(ctx, p.c.Cmd, d.root)
			d.took = time.Since(fetchStart)
		})
		stats.Total = time.Since(start)
		for _, d := range due {
			if d.took > stats.Slowest {
				stats.Slowest, stats.SlowestRoot = d.took, d.root
			}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats = stats
```

and give `dueRoot` its `took` field:

```go
type dueRoot struct {
	root   string
	counts dirtyCounts
	took   time.Duration
}
```

- [ ] **Step 4: Expose it**

In `internal/collect/collect.go`, beside `GitStats`:

```go
// DirtyStats reports what the last dirtyPoller pass cost. Total is the
// fan-out's wall time and Slowest/SlowestRoot name the single worst worktree,
// because on a monorepo one worktree is usually all of it.
//
// A pass no longer blocks publication, so a slow one is a staleness problem
// rather than a latency one: a pass exceeding git_interval means the counts
// are older than they are meant to be.
//
// Zero when the last pass found every root inside git_interval, since then it
// issued no subprocesses at all.
type DirtyStats struct {
	Total       time.Duration
	Slowest     time.Duration
	SlowestRoot string
}

// DirtyStats returns the last pass's measurement. Unlike GitStats it is safe
// from any goroutine: a worker writes it while the poll loop reads it.
func (c *Collector) DirtyStats() DirtyStats {
	c.dirty.mu.Lock()
	defer c.dirty.mu.Unlock()
	return c.dirty.stats
}
```

- [ ] **Step 5: Log it from the daemon**

In `internal/daemon/daemon.go`, extend the field comment at line 87 to cover a third field and
add it:

```go
	pollFailing      bool
	lastSlowPollLog  time.Time
	lastSlowDirtyLog time.Time
```

Add the logger below `logSlowPoll`:

```go
// logSlowDirtyPass records a dirtyPoller pass that took longer than the
// interval its counts are meant to be fresh within.
//
// It exists because moving git off the poll loop makes logSlowPoll go quiet,
// and that line was the only number anybody could ever read off the machine
// where this happens. A slow pass is no longer a latency problem - publication
// does not wait for it - but it is a staleness one, and it is the same
// measurement the 2026-09-09 re-promotion rested on.
//
// Rate-limited on the same window as logSlowPoll and for the same reason: a
// diagnosis wants a series, not one line and then hours of silence.
func (s *Server) logSlowDirtyPass() {
	d := s.Collector.DirtyStats()
	if d.Total <= s.Collector.GitInterval {
		return
	}
	now := s.now()
	if !s.lastSlowDirtyLog.IsZero() && now.Sub(s.lastSlowDirtyLog) < slowPollLogInterval {
		return
	}
	s.lastSlowDirtyLog = now
	s.logf("slow dirty pass: %s total, slowest %s at %s",
		d.Total.Round(time.Millisecond), d.Slowest.Round(time.Millisecond), d.SlowestRoot)
}
```

and call it in `poll`, immediately after `logSlowPoll`:

```go
	start := time.Now()
	sessions, err := s.Collector.Snapshot(ctx)
	s.logSlowPoll(time.Since(start))
	s.logSlowDirtyPass()
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make test && make lint
```

Expected: PASS.

- [ ] **Step 7: Mutation-check the five new tests**

| Test | Mutation | Expected |
|---|---|---|
| `TestDirtyPassReportsItsSlowestRoot` | never assign `stats.SlowestRoot` | FAIL, `slowest root: got ""` |
| `TestDirtyStatsAreZeroWhenNothingWasDue` | move `start := time.Now()` above `if len(due) > 0` and always assign `stats.Total` | FAIL, non-zero `DirtyStats` |
| `TestPollLogsASlowDirtyPass` | delete the `s.logSlowDirtyPass()` call in `poll` | FAIL, no line |
| `TestASecondSlowDirtyPassInsideTheRateLimitWindowLogsNothing` | delete the `lastSlowDirtyLog` window check | FAIL, 2 lines |
| `TestASlowDirtyPassLogsAgainOnceTheRateLimitWindowHasPassed` | make the window check `if !s.lastSlowDirtyLog.IsZero()` alone | FAIL, 1 line |

- [ ] **Step 8: Update the design doc's status**

`docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md`'s "Status"
section says "not implemented, and that was the conclusion". Replace the whole section with an
implemented note that keeps the history rather than erasing it:

```markdown
## Status: implemented 2026-09-09

Shelved on 2026-08-03 and built on 2026-09-09, unchanged in substance. The 2026-08-03 decision
was correct on the numbers it had; what changed is the numbers and the consequence.

**The numbers.** The same append-only daemon log holds 25,145 `slow poll` lines, the recent
ones sustained at 1.3-2.0s with ~99% in git, all at `~/sc-231121`, where a cold
`git status --porcelain` takes 1348ms against under 250ms for every other worktree. It is
intermittent - with `fsmonitor` and `core.untrackedCache` warm, 25 consecutive `poke` round
trips came back 0/25 over 250ms - so the "un-ruled-out" list in the problem section below is
still the right list, and a concurrently churning worktree is the leading suspect.

**The consequence.** `poll` and `handleRequest` share `Run`'s single goroutine, deliberately,
since that is what lets `broadcast` touch `s.clients` unguarded. The `poke` feature that landed
after this spec was shelved means a slow git poll now delays a `RequestPoke` rebroadcast, so
the current-session highlight took 1-2s to move after a session switch. Before `poke`, a slow
`fillGit` cost only publication latency, which is what made it affordable enough to shelve.

Three corrections the implementation made to what is written below, each argued in
`docs/superpowers/plans/2026-09-09-dirty-counts-off-publication-path.md`:

1. Testing item 5 cannot "fail only itself". `Snapshot` builds fresh sessions every call, so
   reversing `fillGit` and `dirty.track` empties the working set permanently and breaks every
   other test in the file. The test is a diagnostic that names the invariant, not an isolator.
2. A branchless worktree - a detached HEAD, ie an in-progress rebase - gains dirty counts.
   `FetchGitStatus` returned early on an empty branch; `FetchDirtyCounts` needs only a root.
   Deliberate, and pinned by `TestFetchDirtyCountsNeedsNoBranch`.
3. The "Instrumentation" section's aside about the pass duration belonging in the stats was
   built: `Collector.DirtyStats()` and a rate-limited `slow dirty pass` daemon line. Without it
   the change makes the machine go dark on the exact metric that justified it, because
   `slow poll` goes quiet once git is off the loop.
```

Then, everywhere below that says "Not built" in the Files table, change the four rows to
"Built."

- [ ] **Step 9: Update CLAUDE.md**

Five edits, all in the same commit:

1. **"What is open" item 1** - `fillGit` is no longer open. Remove the item, renumber the rest,
   and remove the two long paragraphs below the list that argue the 2026-09-09 re-promotion and
   the 2026-08-03 demotion. Replace them with a short record under Key Conventions (edit 3).
2. **The `fillGit` Key Conventions bullet** - it currently asserts `fillGit` is where a slow
   poll comes from. Rewrite the ending: `git status --porcelain` is no longer in `fillGit` at
   all as of 2026-09-09; `fillGit` is now root, branch, unpushed and rebase age, every one of
   them measured at or under 30ms, and the counts are `dirtyPoller`'s. Keep the untracked-cache
   finding and the "do not re-derive this a third time" instruction, and point them at the new
   log line.
3. **The `slow poll` bullet** - add its sibling: `slow dirty pass: <total> total, slowest <d>
   at <root>`, logged from `daemon.poll` when `DirtyStats().Total` exceeds `git_interval`, on
   the same one-minute window. Note that `DirtyStats` is mutex-guarded where `GitStats` is
   goroutine-owned, and why.
4. **The `Invalidate` bullet** - "Git comes back inside the next `Snapshot`" is now half true.
   Meta comes back inside the next `Snapshot`; counts come back a tick later, like remote data.
5. **The `internal/collect` architecture bullet** - `Snapshot` now has a third local stage, and
   `Collector` holds two schedulers. Say that the `poller` interface and the `scheduler` struct
   live in `scheduler.go`, that `remote` schedules `prPoller`/`storyPoller`/`reviewPoller` and
   `local` schedules `dirtyPoller`, that `dirtyPoller` is keyed by git root rather than pane
   path, and that `fillGit` must stay above `dirty.track` because the working set comes from
   `s.Git.GitRoot`.

Add one new "Do not finish these - they are deliberate" entry, since the tickerless argument
now has a second and stronger instance:

```markdown
- **The local scheduler has no ticker either, and it matters more than the remote one's.**
  A ticker on `remote` burns a `gh` quota somebody eventually notices. A ticker on `local`
  gives every open panel its own `git status --porcelain` loop against the monorepo, and there
  is no rate limit to notice it at all. `TestADaemonFedClientSpawnsNoGitSubprocesses` is the
  only test that catches it; it must go through a real `Collector.Start`.
```

- [ ] **Step 10: Commit**

```bash
git add internal/collect/dirty.go internal/collect/dirty_test.go internal/collect/collect.go internal/daemon/daemon.go internal/daemon/slowpoll_test.go CLAUDE.md docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md
git commit -m "feat(daemon): log a slow dirty pass, and record the change in the docs

Moving git off the poll loop makes the slow poll line go quiet, and that
line was the only number anybody could read off the machine where this
happens. A slow pass is now a staleness problem rather than a latency one,
and it is worth the same series of measurements.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Verification on the real machine

The suite cannot prove the thing this was built for. Do this after task 5, on the machine that
produced the 25,145 lines, and record the result in the handoff.

- [ ] `make install`, then **kill the running daemon**. It never restarts itself by design, so
  an install alone leaves the old binary serving every panel - and here the old binary is the
  one with the slow poll.

- [ ] Establish the before/after baseline from the log, which is append-only and never
  truncated, so count by timestamp rather than in total:

```bash
grep -c 'slow poll' ~/.local/state/vigil/vigild.log
grep 'slow poll' ~/.local/state/vigil/vigild.log | tail -5
grep 'slow dirty pass' ~/.local/state/vigil/vigild.log | tail -5
```

Expected after an hour with `~/sc-231121` open and churning: `slow poll` stops accruing new
lines, `slow dirty pass` starts. If `slow poll` keeps accruing, the remaining cost is
`FetchGitMeta`'s six calls, and the next move is either taking those off the loop too or
decoupling `poll` from `handleRequest` - **not** widening this change on a guess.

- [ ] Time a poke round trip while `~/sc-231121` is cold, since the user-visible symptom is the
  reason this was re-promoted. Switch sessions and watch how long the current-session highlight
  takes to move. It should be immediate. The 2026-09-09 baseline for the fast path was 0/25
  round trips over 250ms with the caches warm; the failing case was 1-2s with them cold, so
  invalidate them first:

```bash
git -C ~/sc-231121 -c core.untrackedCache=false --no-optional-locks status --porcelain >/dev/null
```

- [ ] Confirm the accepted one-tick zero is as small as the spec claims. Restart the daemon and
  watch a panel: the Git column should show unpushed and rebase age immediately and `~N +N -N`
  about a second later. If it is longer than a tick, `dirty.fill` or the nudge is wrong.

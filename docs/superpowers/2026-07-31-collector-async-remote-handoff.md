# Collector async remote: gh is off the publication path, and git is now the blocker

Written 2026-07-31, with `collector-async-remote` finished, `make test` green on all 14
packages, and the daemon half **verified on the real machine**. Head `2ccf6d4`, from `main`
at `86e1fdc`.

- Design: `docs/superpowers/specs/2026-07-31-collector-async-remote-design.md`.
- Executed plan: `docs/superpowers/plans/2026-07-31-collector-async-remote.md`. Four of its
  tests would have passed with their subject deleted; see "Process notes".
- Prior state: `docs/superpowers/2026-07-31-poll-latency-handoff.md`, which argued this
  change was worth more before phase 5 than after. It is superseded on its structural claim
  and **refuted on its latency prediction**; keep it for the `definitiveAnswer` history.

**The single most important thing on this page is the `fillGit` finding**, three sections
down. It is not what this branch changed. It is what this branch's instrumentation found,
and it changes what phase 5 should do.

## What changed

`Collector.Snapshot` is **local-only**. It runs `ListSessions`, `BellFlags` and `fillGit`,
then posts the working set to a PR store, reads the store, nudges a set of background
workers, and returns. No network call is on the publication path any more.

`internal/collect/remote.go` is new and holds the seam: a `poller` interface (`pass`,
`invalidate`), a `remote` scheduler with one goroutine per poller, and `prPoller`, the
mutex-guarded successor to `prMemo`. `Collector` gains `Start(ctx)`, `Wait()` and
`RefreshRemote(ctx)`. The daemon calls `Start` in `Run` before its ticker and `Wait` in the
shutdown arm after `pendingEffects.Wait`; the Model calls `Start` in `newModel`. That is one
production statement each.

`session.PRPending` marks a branch with **no store entry at all**, which is not the same as a
branch known to have no PR. `transition.Detect` skips a session where `PRPending && PR == nil`
entirely: no seed, no event, and deliberately not recorded in `next`, so the observation
carrying real data is a first sighting. Without that skip, async fill turns every daemon start
into a burst of `notify` hooks and an `auto_cleanup` run against every already-merged worktree.

## Verification, with the numbers

Run on the developer's machine against 8 live tmux sessions, 7 with open PRs in
`huntresslabs/portal`. Every daemon ran **isolated**: its own temp `HOME`, its own
`XDG_RUNTIME_DIR` and therefore its own socket, with `GH_TOKEN` supplied. The user's real
daemon was never stopped and nothing in the user's workspace was touched. Full record in
`.superpowers/sdd/2026-07-31-collector-async-remote/verification-results.md`.

Three binaries: `BASE` = `86e1fdc` (synchronous `Snapshot`), `NEW` = `2ccf6d4`, and `NOSKIP`
= `2ccf6d4` with `Detect`'s `PRPending && PR == nil` guard replaced by `if false`, i.e. the
async change **without** the mechanism that makes it safe.

### The cold-start burst, reproduced and then shown absent

`notifications_enabled = true`, `notify` pointed at a script appending to a file, each daemon
run 35s with the file read **before** the daemon was killed.

| | notify hooks in 35s |
|---|---|
| `NOSKIP` | **6**, all at t+4.9s, all `idle -> <real state>` |
| `NEW` (shipped) | **0** |

All six fired in the same instant, which is the signature of a seed against PR-less state
being contradicted by the first fill rather than of real activity. This is the failure
`PRPending` exists to prevent, produced on demand and then eliminated.

### Publication no longer waits for gh

A client connected at t+0.06s and every frame it received was timestamped.

| | first frame | contents |
|---|---|---|
| `BASE` `86e1fdc` | **t+9.18s** | already complete: `withPR=7 pending=0` |
| `NEW` `2ccf6d4` | **t+6.84s** | `withPR=0 pending=8` |
| `NEW`, second frame | t+9.71s | `withPR=7 pending=0` |

The structural claim holds: BASE publishes **nothing at all** until every `gh` call has
returned; NEW publishes tmux and git state first and fills PR data in a later frame.

### And the part that did not improve

**Absolute cold-start latency did not improve.** 6.84s is not a good number, and it is not
much better than 9.18s. The first frame is no longer gated on `gh`, but it is still gated on
`fillGit`, which is ~3s. The "1.5 to 2 seconds" in the poll-latency handoff is **arithmetic
that this measurement refutes for cold start on this machine**.

## The `fillGit` finding, which is the consequential one

A binary instrumented to time each stage of `Snapshot`, against the same 8 sessions:

```
TIMING listSessions=5.4ms   n=8
TIMING bellFlags=4.6ms
TIMING fillGit=3.086s
TIMING snapshot total=3.096s
```

and `fillGit=3.472s` and `fillGit=2.858s` on the next two polls. **tmux is 10ms of a 3.1s
`Snapshot`. `fillGit` is 99.7% of it.**

Timing every git command `FetchGitStatus` runs, across all nine worktrees:

| command | cost |
|---|---|
| `git status --porcelain` | **0.055s to 1.590s** |
| `git rev-parse --show-toplevel` | 0.030s |
| `git branch --show-current` | 0.030s |
| `git rev-parse --verify origin/<branch>` | 0.030s |
| `git rev-list --count origin/<branch>..HEAD` | 0.035s |
| `git merge-base HEAD origin/main` | 0.032s to 0.135s |
| `git log -1 --format=%ct origin/main` | 0.035s |

`git status --porcelain` is the entire cost, and it is expensive **exactly on the `portal`
monorepo worktrees that have an active Claude session in them** (0.69s, 0.83s, 1.32s, 1.59s)
and cheap on the quiet ones (0.055s to 0.12s). The worktrees a user cares about are the
expensive ones, by construction.

Two consequences, and the second is worse.

**First, the design's stated reason for leaving `fillGit` synchronous is wrong on this
machine.** "They are local subprocesses", "not the latency source". It is now the whole
latency.

**Second, and this was not previously known: `fillGit` (~3s) is greater than or equal to
`git_interval` (3s), so the memo can never skip.** By the time `fillGit` returns, every entry
it just wrote is already due again. The daemon therefore runs `git status` across every
worktree **continuously**, and the real publication cadence is **~3s, not the 1s
`tmux_interval` the design assumes**. Bell highlighting is up to 3s stale, not one tick.

**This is pre-existing, present identically in the merge base, and not a regression.** BASE
has the same `fillGit`, and its own 9.18s first frame includes it. This branch neither caused
it nor made it worse.

But it means the shape that motivated this whole plan - "one slow thing blocks publication" -
has been **relocated from `gh` to `git`, not removed**. Anyone who reads this branch as
"publication latency is solved" has read it wrong.

## What was NOT verified

- **New-session dispatch latency.** Dispatching a story creates a real worktree and a real
  tmux session in the user's workspace, so it was left to the user. The poll-latency
  handoff's 1.5-2s prediction remains **unmeasured** for the warm-daemon case, and the
  `fillGit` finding gives reason to doubt it: a new session's first `fillGit` still has to
  run `git status` on a fresh portal worktree.
- **`idle -> done` specifically.** No session with a `MERGED` PR happened to be present
  during the runs, so the one event that runs `auto_cleanup` was not directly observed. It is
  the same code path as the six that were - `Done` is just another arm of `State()` - and
  `TestColdStartRunsNoEffects` covers it deterministically at the daemon level. But it was
  not seen on the real machine, and that is the case with a real worktree at stake.
- **The client (panel) half.** Only the daemon was exercised. `Collector.Start` in `newModel`
  is covered by `TestNewStartsTheRemoteWorkers` and `TestADaemonFedClientSpendsNoGhBudget`
  and by nothing on a real machine.

`make install` was run and the user's daemon restarted, so the machine is on `2ccf6d4`
(`v1.2.3-189-g2ccf6d4`). A new daemon came up on its own, as designed.

## Landmines

- **The remote layer has no ticker, and that is load-bearing rather than a simplification.**
  Workers are woken only by `Snapshot`'s nudge. A daemon-fed client never calls `Snapshot` -
  `startPoll` refuses while `daemonConn != nil` - so its workers block for the life of the
  process and spend no `gh` budget. **That is now the entire basis of "one daemon means one
  `gh` rate-limit budget."** Adding a ticker restores per-panel polling for every open panel,
  silently.
- **The tests only catch a *fast* ticker.** `TestRemoteRunsNothingWithoutANudge` and
  `TestADaemonFedClientSpendsNoGhBudget` use windows of 200-300ms. A
  `time.NewTicker(c.PRInterval)` regression at 30s - arguably the most natural naive
  implementation - **passes both silently**. Catching it needs an injectable clock in
  `remote`, which does not exist. Until it does, the doc comment on `remote` and code review
  are the real defence, not the suite.
- **The `RefreshRemote` gap.** Production reaches a pass **only through the worker**.
  `RefreshRemote` is the synchronous seam tests drive so they do not race a goroutine, so a
  `nudge` that never reached a worker would leave **every `RefreshRemote`-driven test green**.
  `TestRunStartsTheRemoteWorkers`, `TestNewStartsTheRemoteWorkers` and
  `TestADaemonFedClientSpendsNoGhBudget` go through a real `Start` and would catch it. Do not
  weaken any of the three into "assert `Start` was called".
- **The pending skip costs at most one `notify` hook per session, at daemon start.** A session
  whose state genuinely changes inside the pending window loses its notification. If a missing
  notification is reported right after a restart, this is why. It is the deliberate trade
  against six hooks and an `auto_cleanup` run on every restart, and it is not close.
- **An old panel against a new daemon bursts toasts on daemon start.** It does not know
  `pr_pending`, so it seeds PR-less and reacts to the fill. Self-limiting now that panels
  re-exec, except that per the binary-refresh handoff **the first install after this lands
  cannot re-exec anything**: a panel must already be running the feature to notice. So expect
  one round of spurious toasts on the panels that were open when this shipped.
- **`Collector.Wait()` inherits `ExecCommander.Run`'s grandchild-holds-the-pipe defect.** The
  defect is pre-existing and phase 4 left it deliberately, and this branch narrows its blast
  radius rather than widening it. Before this branch `poll` ran inline in `Run`'s select loop
  and `fillPRs` ran inside `poll`, so a `cmd.Wait` wedged on a grandchild-held pipe blocked the
  goroutine inside `poll` and `Run` never reached its `ctx.Done()` arm at all: the flock was
  never released, the socket never unlinked, and the daemon also stopped publishing and
  stopped accepting connections for the whole wedge. Now the wedge sits on a worker goroutine,
  and only `Collector.Wait` waits on it - publication and connection handling no longer stop
  too. The flock-and-socket failure mode survives: a wedged `gh` still prevents `Run` from
  returning, so no daemon can start again after one. `RunStream` was fixed in phase 4; `Run`,
  used by the `notify` and `cleanup` hooks and by `FetchPRStatus`, was not. Phase 5 still
  inherits it.
- **`remote.start`'s `sync.Once` makes the first context permanent, and `Run` is its sole
  caller.** A `Server` whose `Run` is invoked twice gets dead pollers plus a `Wait` that
  returns instantly: a daemon that publishes tmux and git forever and never a PR, with no
  error anywhere. Never done today, because `main` constructs and runs one. Starting with an
  already-cancelled context has the same effect and the later call this tolerates becomes a
  silent no-op.
- **A first-ever `gh` failure on a branch resolves it with `pr == nil` and clears
  `PRPending`**, so `Detect` reads a rate-limited branch as "has no PR" and seeds it `Idle`.
  Pre-existing - the old `prMemo` memoized `nil` identically - but **newly load-bearing** now
  that `Detect` gates on `PRPending`. The distinction between "never resolved" and "resolved
  to nothing" is real and the failure path collapses it.
- **`Invalidate` zeroes `fetchedAt` rather than dropping entries**, on purpose. Dropping them
  would re-mark every branch pending, and `Detect` skips a pending session, so a forced
  refresh would swallow the very transition it was pressed to find. Also: `Invalidate`'s git
  half is still goroutine-owned and unguarded, the remote half is safe from anywhere, and
  remote data now comes back **a tick later** rather than inside the same call.
- **`pr_pending` is written into the session cache.** Additive, `omitempty`, overwritten a
  tick later; the Python-era cache reader ignores unknown keys.
- **`internal/daemon/daemon.go`'s `poll` saves the session cache on every pass, including the
  first, where every session is `PRPending` with `PR == nil`.** A panel that starts and loads
  the cache within that first pass paints a blank PR column until the next pass lands.
  Self-correcting within one pass and cosmetic, not fixed here. The realistic way a user hits
  it is a `make install` fleet re-exec, where every panel restarts against a freshly restarted
  daemon at once.

## Deferred

Everything here was found during execution and consciously not fixed.

- **`TestPollingContinuesWhileAJobIsRunning` is flaky, pre-existing, and not ours.** Measured
  at `-count=50` x3 on both sides: 2/2/1 failures with the change, 0/6/0 without. Comparable,
  and that test's collector has no sessions at all so the new worker cannot affect it. The
  correct diagnosis, which the first report got slightly wrong: `jobs.run` sets `JobRunning`
  **before** `RunHookStream`, so `stream.started` fires after the state write. The real cause
  is that the write is not published until the next tick while the socket already holds older
  frames, and a frame already in the socket buffer is no longer collapsible by the one-deep
  latest-wins queue. Fix is to read to a deadline rather than a fixed three frames.
- **`Start` in `newModel` leaks one idle worker goroutine per `New`/`NewPanel`** in the 8
  existing test sites that never cancel their context. Harmless, but it **forecloses adding
  `goleak` to `internal/model`** without first sweeping those sites.
- **`gofmt -l internal/` lists 7 pre-existing files**, verified unchanged at `bcea847`.
  Struct-tag realignment from a newer gofmt (Go 1.26.3) than the repo was last formatted with.
  `make lint` passes. Wants its own commit, not a drive-by.
- **No test catches `Update`'s `DelayedPRRefreshMsg` arm losing its force flag.** Changing
  `m.startPoll(true)` to `startPoll(false)` there is silent. Pre-existing.
- Test-rigor nits recorded by the reviews and left:
  - `TestSlowRemoteDoesNotStallNewConnections` dials without confirming a worker is actually
    parked in the `gh` stub, so its "while a pass was blocked" claim is only probabilistically
    true at the moment of the read. It still catches its target mutation deterministically. An
    `entered` handshake would close it.
  - `waitForSeeded(t, s, want)` treats `want` as a minimum but reads as equality.
  - `TestPollIssuesNoGhCalls` is an absence assertion with no in-test positive control; it
    goes vacuous if `mergedPRCommander`'s tmux format string drifts. Partly mitigated by
    `TestColdStartStillReportsALaterTransition` sharing the fixture.
  - `selfPolled` in `TestADaemonFedClientSpendsNoGhBudget` latches against a possibly
    in-flight pass. Deterministic today only because the fixture makes `getNWO` fail, capping
    a pass at one `gh` call; a future fixture adding a `git remote get-url origin` handler
    makes it spuriously fail. Hardcoding `want 1` with a comment would immunise it.
  - `TestNewStartsTheRemoteWorkers` uses `t.TempDir()` where the package convention is
    `shortTempDir(t)` (macOS 104-byte `sun_path` cap). Safe here because it dials rather than
    listens, but the inline comment's stated reason may not be the real one.

## What phase 5 does now

The seam exists so phase 5 does not have to touch `Snapshot` at all:

1. Add a `poller` implementation in `internal/collect/remote.go` (a `storyPoller`, a
   `reviewRequestPoller`). Each owns its own store and its own idea of what is due.
2. Pass it to `newRemote` in `collect.New`. It gets its own goroutine, so a slow Shortcut API
   cannot delay `gh` and vice versa.
3. Read its store from wherever the work queue renders.

Phase 5's pollers are global lists, not per-branch, so they will have no `track`/`fill`
equivalent. `Collector` holds the `remote` runner for scheduling and a typed `prs *prPoller`
field for the session-facing calls; the interface carries only what scheduling needs. Keep
that split.

**And phase 5 should weigh the `fillGit` finding.** Adding pollers no longer makes the session
list slower - that was the point of this branch. But `git status --porcelain` already does,
by 3 seconds a poll, and the git memo cannot skip because the work takes longer than its own
interval. If phase 5 wants a responsive list, that is where the time is now. The obvious
directions, none designed: move `fillGit` behind the same seam (it is the last lock-free
goroutine-owned memo, which is the cost), raise `git_interval` above the measured `fillGit`
cost so the memo can actually skip, or stop running `git status --porcelain` on every
worktree every interval.

## Process notes

**Four separate tests on this plan would have passed with their subject deleted**, and all
four were caught by review rather than by writing them:

- `TestRemoteStartIsIdempotent` (task 2a, review round 1).
- `Snapshot`'s error path, unpinned (task 2b). Confirmed by mutation: under "check the error
  below the tail", `TestSnapshotReturnsErrorWhenTmuxFails` still passed, so the data-loss half
  of the error contract had **zero** coverage. The failure it hid is real: a regression that
  tracks before the error check posts an empty working set, the unconditional prune wipes
  every entry, every session goes `PRPending`, and `Detect` skips them - so one transient tmux
  failure silently swallows a `Done`.
- The one-poll `TestPollIssuesNoGhCalls` (task 2b, carried into task 3 as a plan-level
  requirement, because task 3 could have turned all five red daemon tests green by putting
  `RefreshRemote` inside `Server.poll`, restoring the synchronous `gh` call this whole plan
  exists to remove, with nothing outside `internal/collect` catching it).
- `TestADaemonFedClientSpendsNoGhBudget` (task 4). A tautology as briefed: a 50ms ticker
  mutation in `remote.start` passed it, because the fixture never called `Snapshot` so
  `prPoller.working` was empty and a ticked pass found nothing due. Rewritten to `Snapshot`
  first and then attach a daemon, which is the real lose-then-regain path.

**Three of the four were written by the plan's author.** The phase 3 handoff records six
briefs with author-written defects, four of them tests that would have passed with their
subject deleted. Two plans in a row is a pattern, not an incident: **on this repo, treat a
plan's tests as unverified claims until each has been watched to fail.**

One more, worth recording separately. The plan asserted that `internal/daemon`'s
`transition_test.go` was entirely unaffected. It was wrong: five tests use
`newDoneToggleCommander`, which returns a real git root, branch and `MERGED` PR, so those
sessions are `PRPending` on the first `Snapshot` and `Detect` skips them. The fix was in the
fixtures (a poll / `RefreshRemote` / poll prime), **not** a second `Snapshot` inside one
`poll`, which was measured and breaks the `bellSwitch` tests because the bell toggle is
consumed twice.

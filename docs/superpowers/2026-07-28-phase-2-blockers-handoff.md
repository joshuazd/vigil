# Phase 2 blockers: state after the branch

Written 2026-07-28 and updated 2026-07-29 with the verification results, at the point the
`phase-2-blockers` branch was finished (48 commits, HEAD at the verification update). Suite
green under `-race`, `golangci-lint` clean. Read this plus the specs before phase 3.

- Design for this work: `docs/superpowers/specs/2026-07-27-phase-2-blockers-design.md`.
  Amended in three places during execution; the amendments are marked inline.
- Executed plan: `docs/superpowers/plans/2026-07-28-phase-2-blockers.md`. Its "Landmines"
  section is stale on one point, noted below.
- The state phase 2 merged at: `docs/superpowers/2026-07-27-phase-2-handoff.md`. Still the
  best account of the daemon and panel work, superseded on the three blockers.
- The whole 6-phase design: `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`.
  Its "Still open after phase 2" list is no longer the debt list; this document is.

This closes the three items the phase 2 handoff filed under "Must be resolved before phase
3 ships", plus the gh-budget reduction that lives in the same code. Nothing here is a
visible feature. Phase 3 makes the panel the default for new sessions, which is what turns
N attached clients from exotic into normal, and every fix here exists because of that.

## What landed

**Blocker 1: transition ownership.** New package `internal/transition`, holding a
`Detector` (pure state comparison, priming silently on its first call) and a `Runner` (the
`notify` hook and `auto_cleanup`). Both the daemon and `Model` hold one of each. Side
effects belong to whoever owns the poll loop: the daemon when a client is connected to one,
a self-polling client otherwise. `SnapshotMsg.Local` carries which case a client is in
rather than the code inspecting `m.daemonConn` at the point of use. Toasts and auto-focus
stay per-client - each panel has its own screen and its own cursor. `EffectRunner` is an
interface, not a package function, because `config.RunHook` shells out and a counting stub
is the only way to assert "fired once, not N times". `initialLoad`, threaded through four
call sites, is gone: the `Detector`'s priming replaces it.

**Blocker 2: the self-polling path collapsed onto `collect`.** `internal/model` no longer
has its own tmux/git/PR fetch cycle. `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd`,
`TmuxUpdatedMsg`, `GitUpdatedMsg`, `PRUpdatedMsg`, `gitTickCmd`, `prTickCmd`,
`initialPRDone` and `gitCache` are all deleted. A self-polling client runs one
`collect.Collector.Snapshot` per `tmux_interval`, with git and PR work gated inside it by
the same `git_interval` and `pr_interval` memos the daemon uses. `prCache` and `warmCaches`
stay, as the client-side PR backstop. `annotateClientFlags` moved out of `listenDaemonCmd`
so both paths set `IsCurrent`/`IsLast` the same way - that is the one thing the daemon
cannot know.

The shape of the loop matters and took four rounds to get right (see the process notes).
`Collector.Snapshot` is not reentrant: its memos are owned by the calling goroutine.
`Model.startPoll` is the only issuer of `collectCmd` and refuses while `pollInFlight` is
set or a daemon is connected. Pacing is a self-rescheduling one-shot `CollectTickMsg`,
created at exactly two sites (`Init`'s fallback branch, `handleDaemonLost`) and continued
at exactly one (the tick handler), unconditionally. `pollInFlight` is cleared by any local
snapshot before the epoch check, because it tracks a running goroutine rather than a
generation. `Collector.Invalidate` exists so `r` and post-action refetches can force a
poll, and is called from inside the poll goroutine so the memos are never touched
cross-goroutine.

**Blocker 3: the layout constants.** `colIndex` and `colState` are 1, matching what
`indexCol` and `StateIndicatorWithBg` actually render. The five fixed costs and the five
tier selection widths are named package constants, and the thresholds are frozen at the
values that were verified on real panes rather than recomputed from the corrected costs -
deriving them would move `tierNoGit` to 39 and width 40, the landscape panel's default,
would stop choosing the compact tier. The name column gains 2 columns at the two widest
tiers and 1 at the rest. `TableLayout.Total()` is now exact against what `renderRow`
emits, which required adding `padRight` to the PR cell on the non-cursor path.

**Blocker 4: the polling query.** `reviewThreadsQuery` lost its inner
`comments(first: 5)` connection and keeps `isResolved` and `isOutdated`.
`fetchReviewThreads` returns only the unresolved count. Bodies come from a new
`FetchReviewComments`, called on demand for the selected session and cached by branch in
`Model.reviewComments`. Call count per poll is unchanged; nodes requested per query drop by
roughly six times.

**Out of band, not from any task.** Four fixes the run turned up and that are worth
knowing about:

- `make test` opened real browser windows, twice. Root cause was
  `action.OpenPRInBrowser` using a bare `exec.Command("open", url)`. The durable fix was
  routing it, and then `config.RunHook`, through `fetch.Commander`. `RunHook` had default
  hook templates, so a test calling `MergePR` with a mock commander would still have really
  merged and deleted a branch - and two tests in the suite were already shelling out for
  real. The only direct `exec` sites left are `internal/fetch/cmd.go` and the daemon spawn
  in `internal/model/client.go`.
- The test suite was overwriting the developer's real session cache. Measured:
  `~/.local/share/vigil/cache.json` went from 10917 bytes of live state to 236 bytes
  containing the fixture name "alpha". `applySnapshot` resolved `cache.CachePath()` at the
  call site inside a goroutine that can outlive a `t.Setenv` restore. Fixed with an
  explicit `Model.cachePath`. Note carefully: the `m.cachePath == ""` guard is *not* what
  protects the cache. `newTestModel` leaving the field empty is.
- The self-polling path had no per-session cleanup gate after the daemon got one, so the
  two-cleanups-racing-one-worktree hazard - the plan's headline bug - was still live
  client-side. A plain `vigil` dashboard never spawns a daemon, so a dashboard user with
  `auto_cleanup = true` was exposed. Closed with an `inFlightEffects` map on `Model`,
  cleared by an `EffectDoneMsg` so every access stays on the UI goroutine.
- Four stray `cache-*.json` files were removed from the developer's real
  `~/.local/share/vigil/`, one containing `fixtureSessions()` data verbatim.

## `auto_cleanup` is now safe to enable with panels open

This is the first time that has been true. The phase 2 handoff said "do not enable
`auto_cleanup` while panels are open" and it was right to. State precisely what changed,
because "safe" here is four separate properties and not a general assurance:

1. **Side effects fire once per event, not once per attached client.** Exactly one process
   runs them. Measured on the daemon at 0, 1, 3 and 10 connected clients: exactly one
   effect run every time, and zero on the priming poll. Under the deliberately mutated
   per-client version the same fixture produced 0, 1, 3 and 10.
2. **A session with any client attached is never cleaned up.** `Runner.Run` resolves this
   itself with `fetch.AttachedSessions` at effect time, rather than trusting an
   `IsCurrent` field the daemon cannot populate. `session_attached` is a client *count*,
   so the test is `!= "0"`, which is correct at any client count and independent of the
   `TMUX_PANE` the daemon inherited.
3. **A malformed event is refused and logged.** An `Event` with an empty `Session`,
   `PanePath` or `GitRoot` never reaches the destructive path. `tmux kill-session -t ''`
   kills a real session, verified on an isolated tmux server, and nothing upstream
   validated those fields.
4. **Cleanup is serialized per session, on both paths.** `session.State()` checks
   `HasBell` before the merged check, so a merged session that gets a bell oscillates
   `Done -> Attention -> Done` and emits two `New == Done` events. Measured at four
   concurrent `CleanupSession` goroutines for one session on the daemon, and three to four
   on a self-polling client. Both are now gated by an `inFlightEffects` map keyed on session
   name, and only `Done`-bound effects are gated.

**The guard fails closed.** If `tmux list-sessions` cannot be reached, cleanup is
skipped and logged, not attempted. If the session is absent from the map - which is what a
session that ended between the poll and the effect looks like - cleanup is skipped. Any
`session_attached` value other than `"0"` reads as attached. `auto_cleanup` itself fails
closed too: `GetSettingBool` is `== "true"`, so `"TRUE"`, `"1"`, `"yes"` and a missing
value all read as false.

The fail-closed direction was not free, and the first shipped version had it backwards.
`fetch.CurrentSession` returns `""` on any tmux error, `""` never equals a session name, so
a tmux failure *disabled* the guard while `action.builtinCleanup` went on to
`git worktree remove --force` - the worktree of the session the user was sitting in, with
uncommitted work in it. A reviewer proved the code was indifferent to the guard's polarity
by flipping it to the safe form and watching the whole suite stay green.

The four load-bearing checks are now run and passed, including the counterfactual against `main`. See "Verification status".

## Corrections to the previous handoff and the design doc

Three claims that were wrong, and mattered.

**Review-thread data was not detail-panel-only.** The phase 2 handoff and the parent spec
both said the review-thread GraphQL call is "only consumed by the detail panel's
review-comments mode" and that making it lazy would "roughly halve the daemon's cost".
Both false. The call returns two things: `UnresolvedComments`, a count, which drives
`session.State() == Unresolved`, the `☐ N` badge, the state dot, auto-focus and the
transition notifications - needed for every session on every cycle - and `ReviewComments`,
the bodies, read only by `internal/view/detail.go`. The call therefore could not be made
lazy at all, only trimmed. Dropping the inner `comments` connection leaves the call count
unchanged and cuts nodes requested per query by roughly six times. GitHub scores the
GraphQL limit on nodes requested, so the budget does fall, but this is not a halving, and
anyone reading the old text will over-estimate what was available here. Halving the call
count would mean hand-writing a GraphQL query that reproduces what `gh pr view --json`
returns, including `mergeable` and `statusCheckRollup` handling. Considered and declined.

**The `notify` duplication was live, not latent.** The phase 2 handoff called the
duplicate-side-effect bug latent because "`auto_cleanup` defaults to false and no `notify`
hook is configured". The `auto_cleanup` half is right. The `notify` half is not:
`notify` has a default template (`internal/config/config.go:48`,
`tmux display-message -d 5000 "vigil: {session} → {new_state}"`) and
`notifications_enabled` defaults to `"true"`, so N open panels were already firing N
`tmux display-message` calls per transition. Benign, because each overwrites the last,
which is why nobody noticed. But only the `auto_cleanup` half was waiting on a config
change; the rest was running.

This also has a testing consequence that bit twice during the run: a test asserting no
hook fired must disable `notifications_enabled` explicitly, and a test that means to
override the `notify` hook and forgets will resolve the default and fire
`tmux display-message` into the user's live session.

**`Runner.Run` uses `fetch.AttachedSessions`, not `fetch.CurrentSession`.** The design doc
specified `CurrentSession`. **The design doc has been reconciled**: the "Blocker 1" section
now carries a marked amendment stating what shipped and why, and the testing table row was
updated. Two other sections of that doc were amended the same way, because they were also
falsified by execution: "Double cleanup cannot happen by construction" (it can, via the
bell oscillation above), and the `threshold >= fixed + nameMin` invariant test, which
shipped, was deleted, and was then restored - see the layout note below.

The executed plan's "Landmines" section still says "`Runner.Run` skips cleanup for the
current session before reaching this". That is stale in the same way. The concern behind it
is not: `action.CleanupSession` calls `switchAwayIfCurrent`, which runs
`tmux display-message` and possibly `switch-client`, and from the daemon there is no client
of its own, so what tmux reports is the user's client. The attached-session guard stops
`Runner.Run` reaching it. If that guard is ever removed, the daemon can move the user's
client.

## The path-parity gap, measured

`CLAUDE.md` requires the daemon-fed and self-polling paths to behave identically. There is
a real, quantified exception, and the earlier measurement below was superseded. Stating the
current one rather than glossing it:

**Re-measured at HEAD `76d7779` (the final whole-branch review): seven transitions on one
session, with the first `Done`-bound effect blocked, produced 5 `notify` hook invocations on
the daemon path and 5 on the model path. Toasts are 7 of 7 on both.** The gap that used to
separate the two paths is closed. The earlier figure (5 on the daemon path, 7 on the model
path) was measured at `ebbdc83`, before the client-side gate landed at `dc558fd`, and was
never re-run until this review - do not cite "model: 7" as current.

The surviving deviation is not between the two paths, it is from "the `notify` hook fires
once per real transition": a repeat `Done` arriving during its own session's slow cleanup
loses its hook, on both paths, exactly the two repeat-`Done` events out of seven transitions.
Each is logged by the daemon (`transition effects for %s still running, skipping a repeat
Done`). Only the hook is suppressed - **the toast still fires**, because
`checkStateTransitions` adds the notification before the `!local` check and before the gate.
An earlier version of this ledger claimed the toast was lost too; that was wrong, and the
measurement above (7 of 7) is what corrects it. The two remaining path differences are that
the daemon logs each skip and a client cannot (its `Runner` has a nil `Logf`), and that the
daemon holds a mutex where the model relies on everything happening on the UI goroutine.

The comment at `internal/daemon/daemon.go:59-61` was fixed at `76d7779` and no longer makes
the "fires once per real transition on both paths" claim; it now describes the gate and the
repeat-`Done` skip directly. No further action needed here.

## Tripwire: the next early return in `Runner.Run`

**Four consecutive fix rounds on `Runner.Run` hit the same class of defect: each newly
added early-return guard silently disarmed tests whose fixtures depended on an earlier
guard firing.** A `Done`-check test whose fixture had no `GitRoot` stopped testing the
`Done` check the moment the malformed-event guard was added in front of it, and the
mutation for it came back "no test failed" across all twelve packages. That happened, was
fixed, happened again from the presence-check fix, twice, and then again. Four stacked
negative guards is the wrong shape for a destructive decision.

**Do not refactor it now.** The path is currently verified by 27 mutations plus a measured
exit-point audit: every exit in `Run` was instrumented and each record tagged with the
calling `Test` frame via `runtime.Stack`, and all 14 `Run` tests were confirmed to reach
exactly the guard their name claims, with no short-circuits. That verification is worth
more than the better shape.

**But the next change that adds an early return to `Runner.Run` must first land as a
refactor**, before the new guard: collapse the guards into a single function returning the
reason cleanup is refused - `func (r Runner) cleanupBlockedBy(ctx, ev) reason`, empty
meaning proceed - so tests assert a *reason* and a newly added guard changes a reason
instead of silently intercepting the fixture of an older test.

## Verification status: the four load-bearing checks are RUN and PASSED

Run 2026-07-29, before merge, on the real machine. Method: a `tmux` shim on `PATH`
forwarding every call to an isolated server (`tmux -L vigilverify`), an isolated `HOME` and
`XDG_RUNTIME_DIR`, a branch binary built to a temp path, and a `gh` shim reading its PR
state from a file so `OPEN -> MERGED` produces a real transition. The developer's tmux
server, daemon, cache and installed binary were never touched, and `make install` was
deliberately not run - see the landmine about replacing a running daemon's image.

- [x] **Hooks fire once with N panels. PASSED, with the counterfactual.** Three panels
  against one daemon, one `idle -> attention` transition: **1** hook invocation. The same
  experiment against `main` at `7c6cc5a`, same three panels, same single transition: **3**.
  This is blocker 1, reproduced and then fixed, observed rather than inferred.
- [x] **`auto_cleanup` runs once. PASSED.** `auto_cleanup = true`, two panels attached, four
  sessions transitioning `review -> done` at once: **4** hook invocations, one per session,
  not per session per panel. Under the previous behaviour that would have been 8. The daemon
  log stayed empty, so no cleanup failed.
- [x] **A session with a client attached is never cleaned up. PASSED.** Two throwaway git
  worktrees, both reaching `Done`. The unattached session was killed and its worktree
  removed. The session with a client attached survived, and so did its worktree. This is the
  fail-closed guard that replaced the broken `CurrentSession` check, holding against real
  tmux.
- [x] **Daemon-up versus daemon-down is byte-identical. PASSED.** Dashboard captured at
  120x20 with a daemon, then with the daemon killed and the client fallen back to
  self-polling: the data rows `diff` clean. This is the collapse's whole claim.

Still not observed, and none of them is a claim this branch makes:

- [ ] **Fallback survives a failing poll.** Break `tmux` on the path for a self-polling
  client; confirm it keeps polling and recovers rather than going quiet forever.
- [ ] **Width 40 is unchanged.** Capture a 40-column panel before and after the layout
  change. The name column must be at least as wide as it was. Pinned by
  `TestPanelWidthStillPicksTheCompactTier`, so this is confirmation rather than discovery.
- [ ] **Comments mode still works.** Open a PR with real unresolved review threads, switch
  to comments mode, confirm the bodies arrive and that switching away and back does not
  refetch. The `gh` shim used above returns no threads, so this is untested against real
  GraphQL.
- [ ] **`make install` while a daemon runs.** Confirm the new binary runs: overwriting a
  running image's inode invalidates its code signature and macOS then SIGKILLs every later
  exec of that path. **Also restart the daemon.** This branch makes the daemon an owner of
  transition effects for the first time; the code-signing check only proves the new binary
  launches, not that the *running* daemon (the old image, with no transition wiring) has
  been replaced. Skipping the restart leaves every daemon-fed panel's `notify` hook firing
  zero times. See the landmines.

One thing the verification exposed about method, worth keeping: the first two attempts
produced zero hook invocations and both times the harness was at fault, not the code. Once
because a bell flag left set from a previous run meant the `Detector` primed at `Attention`
so no transition occurred, and once because the `gh` shim reported `MERGED` from the very
first poll, so the sessions primed at `Done`. A test that produces no event proves nothing
about a system that only acts on events, and "nothing happened" looks identical to "the
feature is broken". Prime deliberately, then transition.

## Deferred minors, by area

None of these is a live defect. Each is a place where the code is right and the
verification is not, or where something is dead, or where a comment is false.

**`internal/transition` and the cleanup path**

- `action.builtinCleanup` still runs `git worktree remove --force` after a *failed*
  `tmux kill-session`. The session survives and its worktree does not. The attached-session
  guard makes this hard to reach on the daemon and self-polling routes, but that guard does
  not run on the interactive/batch `x` path (see the landmines), so the mitigation is
  partial, not general.
- `internal/fetch/tmux.go`'s `TrimRight(line, "\r")` is dead: the per-value `TrimSpace`
  already strips CR. Proved by a 400k-sample differential run with zero diffs. Worse, the
  function's doc comment reads as though that line is what fixed the leading-whitespace
  bug, when the real fix was *removing* the outer `TrimSpace(out)`. Delete the line or
  reword the comment.
- A whitespace-padded zero (`"| 0"`, `"|\t0"`) reads as unattached and so permits cleanup,
  because the value is `TrimSpace`d before the `!= "0"` test. tmux emits bare digits, so
  unreachable through tmux; only the trailing-space form is pinned.
- Two contradictory lines for one session are last-write-wins, so line order decides.
  Unreachable through tmux, which enforces unique session names.
- `fetch.BellFlags` still has both weaknesses `AttachedSessions` was fixed for: it splits
  on the *first* pipe and trims the whole output. **This is on the destructive path, not
  off it** - `session.State()` checks `HasBell` before the merged check, so a bell mis-keyed
  by the first-pipe split, or lost to the leading-whitespace trim on tmux's first-sorted
  session, turns `Attention` into `Done`, and `Done` is the only state that reaches
  `CleanupSession`. Deferred for merge anyway, because it needs a session name containing
  `|` or a leading space and the attached-session guard still holds, but the earlier framing
  of this as a non-destructive path was wrong and should not carry forward as written.
- `return` instead of `continue` on a skip in the daemon's effect loop survives the whole
  suite, and that mutant permanently drops every later event in the same `Detect` batch,
  including another session's notify *and* cleanup. A ~30-line two-session test catches it.
- Deleting the `inFlightEffects` entry before `Done()` in the daemon is unpinned. It is
  pinnable, with an in-package assertion that `inFlightEffects` is empty the instant
  `Wait()` returns; 5/5 at 2000 iterations, deterministic on correct code. Not reproducible
  at `GOMAXPROCS=1`.
- No `recover()` around `Effects.Run` on either path. See the landmines.

**`internal/model` and the self-poll loop**

- The straggler restart in `handleSnapshot` ignores `pollInterval`. Deliberate: you want
  data immediately after breaking a wedge.
- Pacing is tick-to-tick, so a poll slower than `pollInterval` gets zero cooldown. The
  rate ceiling is one `Snapshot` per poll duration, not per interval.
- The identical-paths test compares sessions with `==`, which is only valid because
  `session.Session` holds a `*PRStatus` and the fixture yields nil PRs on both sides.
  Compare field by field if that fixture ever gains PR data.
- The `daemonConn` guard in the `CollectTickMsg` handler is unreachable today. Kept
  deliberately: four lines make a three-site invariant true by construction, and the
  failure mode otherwise is the silent double-chain this task shipped three times.
- Three of the smaller transition tests call `checkStateTransitions` directly rather than
  through `Update`. Fine for the properties they assert, which do not depend on which call
  site supplied `local`.
- The `notifSeverity` test table omits `Unresolved` and the default warning case.
  Pre-existing, narrow.
- `internal/model` calls `action.CleanupSession` with `context.Background()` and no
  attached-session guard on the interactive `x` path and on the batch path. The
  `context.Background()` and missing-attached-guard halves are defensible - the user named a
  session and confirmed twice. What is not defensible, and is omitted from that framing: `x`
  and batch `x` do not participate in `inFlightEffects` at all, so either can start a second
  `CleanupSession` against a worktree an automatic route is already cleaning up. See the
  landmines.

**Review comments and the gh budget**

- **Nothing invalidates the per-branch comment cache automatically.** Not new comments, not
  a merged-and-reopened PR, not a PR state change. `r` clears it, and that is the only
  escape hatch. A TTL and invalidation on PR state change were deliberately left out of
  scope. The sharper half, omitted from that framing: a *failed* fetch is cached as a
  settled empty result, not merely a stale one - `FetchReviewComments` returns nil on error,
  `Update` normalises nil to an empty slice, and `refreshDetailCmd`'s `loaded` check then
  suppresses every retry. Measured: three failing `gh` retries, then the panel renders
  `☐ 3` next to "No review comments" indefinitely, until `r`. See the landmines.
- Rapid revisits to a branch before an in-flight fetch answers can duplicate the `gh` call.
  Worst case is otherwise N calls for N `Unresolved` sessions on N distinct branches,
  visited once each; shared branches and revisits are free.
- Dropping the `state == "OPEN"` gate in `FetchPRStatus` is uncaught by any test.
  Pre-existing, and an optimisation rather than a correctness property.

**`internal/view` layout**

- `TestTierBoundariesAreFrozen`'s name-width assertion is computed from the tier's own
  fixed cost, so it is self-referential and vacuous on its own. Harmless: the real coverage
  for fixed-cost drift is `TestTableNeverExceedsItsWidth`, which catches a one-column drift
  because a wider name pushes the row past the pane.
- `internal/view/layout.go`'s comment names `TestFrozenThresholdsAdmitAUsefulName`. That
  test was deleted at `15357ec` but restored at `76d7779`; at HEAD it exists, passes, and is
  what the comment correctly refers to. Lowering `nameMin` is still caught by nothing -
  `nameMin` never binds, so nothing breaks today, but the invariant test exists to assert the
  floor the frozen thresholds must respect.
- Removing the git cell's `padRight` is uncaught. Pre-existing. Catching it needs a
  narrower fixture, which would weaken the truncation coverage the wide fixture provides -
  a deliberate trade.

**Hooks and actions**

- No test pins that `ExecCommander`'s 10s default timeout does not clamp `RunHook`'s longer
  hook timeouts. `ExecCommander.Run` only applies its default when the context has no
  deadline, and `RunHook` sets one, so this is correct by reading and untested.
- `MergePR`'s string-match recovery branch - a failed hook whose output contains "merged" -
  has zero coverage; `go tool cover -func` shows count 0. Pre-existing since the original
  Go rewrite (`8773b3a`), confirmed not a regression. Worth a test the next time
  `internal/action/action_test.go` is touched.
- `action.switchAwayIfCurrent` still uses `fetch.CurrentSession`, so the "wrong client
  dropped out of tmux" half of the multi-client problem is untouched.
- `action.isWorktree` bypasses the `Commander` seam and stats the filesystem directly,
  which is why worktree removal is hard to unit-test.
- `fetch.nwoCache` and `defaultBranchCache` are package-level mutable state, against the
  project's no-global-mutable-state convention.

**Combinations worse than their parts**

Each deferred item above is individually true and individually narrow. Three combinations of
them are not narrow, and each is invisible if you only read the per-item list:

1. **`BellFlags` mis-key, plus `HasBell` checked before merged, plus force-remove after a
   failed kill.** A session whose bell is mis-attributed reads `Done` instead of `Attention`,
   enters the destructive path, and if `kill-session` then fails, the worktree is
   force-removed anyway. A bell the user is waiting on is ignored and the worktree goes.
2. **Route 3's (`x`, batch `x`) missing `inFlightEffects` gate, plus force-remove after a
   failed kill, plus `context.Background()`.** Two concurrent cleanups can run against one
   worktree, either able to force-remove while the other is mid-kill, neither cancelled by
   quitting.
3. **No automatic comment-cache invalidation, plus a failed fetch cached as a settled empty
   result.** The documented item alone is "data can be stale". The compound is "data can be
   wrong and look settled", with the panel showing `☐ 3` beside "No review comments".

**Repository hygiene**

- `.gitignore` does not cover `*.tags`. This repo has a ctags `post-checkout` hook that
  writes transient `NNNNN.tags` files, and the harness reacts to them. See the landmines.

**Carried forward unchanged from phase 2**

- `visibleLen` counts runes, not display columns.
- A permanently failing `gh` still shows the last known PR indefinitely with no staleness
  marker on either path. The `daemon stale Ns` marker covers a silent daemon, not a silent
  `gh`.
- The daemon only prunes dead clients after a successful poll.
- Panel notifications can be dropped when no padding row is left over.
- `cache.Save` uses `os.CreateTemp`, so a hard-killed writer can leave `cache-*.json`
  litter in `~/.local/share/vigil/`. `Load` only reads `cache.json`, so it is inert.
- `internal/view`'s tests prove less than they look about styling: under `go test` there is
  no tty and no forced colour profile, so lipgloss emits zero ANSI bytes and every "styled"
  cell is a plain string.

## Landmines and sharp edges

- **The comment cache is never invalidated automatically, and a failed fetch is cached as a
  settled empty result, forever.** Stale review comment bodies persist until `r` or a
  restart - that alone is the "data can be stale" version. The sharper version, confirmed by
  a review with a `MockCommander` whose `gh` handler errors: after three failing retries,
  `FetchReviewComments` returns nil, `Update`'s `PRCommentsMsg` case normalises that nil to
  an empty slice, and `refreshDetailCmd`'s `loaded` check then suppresses every further
  attempt. The panel renders `☐ 3` next to "No review comments" - the header says there are
  unresolved comments, the body says there are none - indefinitely, until `r`. This is the
  sharpest edge in the branch, because the data does not merely age, it looks settled and
  wrong at the same time.
- **There is no `recover()` around `Effects.Run`, on either path.** A panicking hook kills
  the daemon. Verified on both paths, and a wedge table confirmed the failure mode is a
  crash rather than a silent hang, which is the better of the two but is still a crash.
- **Worst-case SIGTERM latency with a real `Runner` mid-effect is about 65 seconds**, from
  the constants: `hookTimeout` 5s plus `cleanupTimeout` 60s. The ledger carries this figure
  and it is what the constants say. Set against it, a review *measured* worst-case shutdown
  delay at 0.85 ms and judged the 65s ceiling unreachable because every subprocess context
  derives from the cancelled one, so `exec.CommandContext` kills the child on SIGTERM.
  Those two claims were never reconciled. The mechanism that could still reach the ceiling
  is a hook whose grandchild inherits stdout and outlives the killed `sh`, since
  `cmd.Output()` waits for the pipe. Treat 65s as the bound and 0.85ms as the typical case,
  and do not quote either as settled.
- **A self-polling client's auto-cleanup produces no toast at all, success or failure, and no
  follow-up forced refresh.** It constructs its `Runner` with a nil `Logf`. The design routes
  failures to the daemon log and a client has no log; writing to stderr would corrupt the
  TUI, and `Run` cannot call `addNotification` safely from a `tea.Cmd` goroutine. Accepted,
  because a client only owns effects when no daemon exists (the degraded mode) and a failed
  cleanup leaves the session visible in the next snapshot, which is itself the feedback. Note
  precisely what regressed, corrected from an earlier framing here that understated it: on
  `main`, the automatic cleanup path itself emitted `ActionResultMsg` for both outcomes, not
  only the interactive `x` path. The toast that is missing now is one `main`'s own automatic
  route used to give, not merely one borrowed from the manual route.
- **`internal/model` still calls `action.CleanupSession` with `context.Background()` and no
  attached-session guard on the interactive `x` path** (and on the batch path). The
  `context.Background()` and missing-attached-guard halves are defensible - the user named a
  session and confirmed twice, and a cleanup the user explicitly asked for arguably should
  finish even if they quit. **What is not defensible: `x` and batch `x` do not participate in
  `inFlightEffects` at all**, the one guard whose entire purpose is "never run two cleanups
  against one worktree" - `grep -n inFlightEffects internal/model/model.go` never shows up
  inside `cleanupCmd` or `batchCleanupCmd`. With `auto_cleanup = true`, pressing `x` on a
  session whose automatic Done-bound cleanup is already in flight starts a second
  `CleanupSession` against the same worktree. Acceptable for this merge because
  `auto_cleanup` is off by default and `x` needs two keypresses, but it is the last live
  instance of this branch's headline bug and should be first on the phase 3 list, ahead of
  the ownership-handoff item below.
- **The daemon/client ownership handoff is not atomic during a daemon's startup window.**
  Measured with two independent owners over one shared worktree, running two real
  transitions: `notify=4` (want 2), `kill-session=2` (want 1). Reachable because `newModel`
  spawns a daemon and starts self-polling immediately, while the client's reconnect probe
  only lands at `daemonProbeInterval` later - a window of roughly 1 to 2.3 seconds (the probe
  interval plus the ~300ms dial) opens every time `vigil --panel` starts with no daemon
  running, during which both the freshly spawned daemon and the still-self-polling client can
  run effects for the same event. This is new with this branch: on `main` the daemon never
  ran effects, so a self-polling client was always the only owner; making the daemon an owner
  is what creates the overlap. Not a merge blocker - the window is short, needs a real
  transition inside it, and the pre-branch behaviour (N clients, every event) was strictly
  worse - but it belongs here, and the cheap phase 3 fix is for a client to stop owning
  effects the moment it has spawned a daemon, rather than the moment it reconnects.
  **Deployment corollary, worth a line on its own:** after `make install` without restarting
  the already-running daemon, that daemon is the old image with no transition wiring at all.
  Panels are daemon-fed, so the `notify` hook fires **zero** times - the exact condition the
  project's merge gate was set against. The `make install` checklist item in "Verification
  status" above now says so.
- **`internal/protocol` treats a version mismatch as a permanent per-snapshot error**
  (`protocol.go:70-72`), which gives the ownership-handoff window above an unbounded form the
  first time `Version` changes: a client dialing a daemon of a different `Version` reconnects
  every 2s and falls back every time, self-polling in each gap while the daemon also owns
  effects, forever rather than for a bounded startup window. `Version` is 1 on both sides
  today, so this is latent, and it is explicitly out of scope for this branch - but phase 3
  is where a version bump would plausibly happen, and a permanent decode error should
  probably stop the client re-dialing and say so rather than oscillating.
- **`action.builtinCleanup` still force-removes a worktree after a failed
  `kill-session`.** The kill error is discarded. With the attached-session guard in front
  of it this is hard to reach automatically, but the interactive path has no such guard.
- **`visibleLen` counts runes, not display columns.** Every width assertion in this branch,
  including the new exact-`Total()` test, uses the same metric the renderer does, so none
  of them can see a double-width glyph overflow. A CJK or emoji session name still
  overflows a pane. First thing to suspect if a panel ever wraps.
- **`tmux kill-session -t <name>` needs the `=` exact-match prefix**, which
  `action.builtinCleanup` now uses. Without it `-t` falls back to prefix and fnmatch
  matching: verified on a throwaway server that `kill-session -t al` destroys `al|pha`.
  `AttachedSessions` splits on the *last* pipe for the same family of reason. Any new tmux
  target should use `=`.
- **`Collector.Snapshot` is not reentrant** and its memos are unsynchronized. Exactly one
  `collectCmd` may be in flight per client. This is why the fallback self-schedules and why
  `Collector.Invalidate` is called from inside the poll goroutine. Two concurrent
  `Snapshot` calls were reproduced under `-race` during this work; the fix has a loud
  regression test that reproduces the actual `WARNING: DATA RACE`.
- **The daemon's `poll` is synchronous per tick.** Effects therefore run in goroutines. A
  hook that blocked for its full timeout inline would delay every client's snapshot.
- **`config.RunHook` used to bypass `fetch.Commander` and no longer does**, so a
  `MockCommander` now sees hook invocations. But hooks have *defaults*: a test that means
  to override `notify` or `merge` and forgets will resolve the default template and act on
  the user's real tmux server or real repository.
- **`cp` is aliased to `-i` on this machine.** Mutate and restore with `git checkout --` or
  `git stash`, never a file copy: a leftover backup of the same name silently wins and gets
  written over the working file. Also insufficient on its own - see the process notes for
  how a committed fix was still lost this way.
- **The repo's ctags `post-checkout` hook drops `NNNNN.tags` files that `.gitignore` does
  not cover.** Two agents reported `<system-reminder>`-shaped text after a
  `git checkout --`, claiming a file had been modified intentionally and saying not to tell
  the user. That is the harness's standard linter-modified-file reminder reacting to those
  temp files, not an injection. Both agents correctly treated it as data and disregarded the
  non-disclosure clause. Benign; a `*.tags` entry in `.gitignore` removes it.

## Process notes

How this ran: one implementation plan of eleven tasks, one implementer subagent per task,
each followed by a task review that mutated the production code rather than reading the
tests, then a fix round per finding, then a re-review scoped to the fix. Eleven tasks,
seventeen fix rounds, six out-of-band fixes, and a working ledger
(`.superpowers/sdd/2026-07-28-phase-2-blockers/progress.md`) that is where all of this came
from.

**Eleven of eleven task briefs contained a defect written by the plan's author, and two of
the author's mid-flight fix designs did too.** That is the headline and it should not be
softened. The two fix-design defects are the serious ones:

- The first attempt at the cleanup guard specified `fetch.CurrentSession` and then, when
  that was found to fail open, specified an attached-check that tested
  `session_attached == "1"`. `session_attached` is a client count. Two panels on one
  session - this design's stated normal case - read as *not attached*, and the session gets
  destroyed. **The ordered fix was worse than the bug it replaced**, since `CurrentSession`
  at least protected the current session at any client count. Verified on an isolated
  server: two clients report `canary|2`, and `kill-session` on a three-client session
  returns 0.
- The first attempt at serializing effects used a buffered channel with a "this send can
  never block" argument. The argument was false. At 257 distinct in-flight sessions the
  send blocks, `pendingEffects.Wait()` never returns, and the drain only runs *after*
  `Wait()`, so nothing rescues it. `signal.NotifyContext`'s `stop()` is deferred behind
  `Run`, so the wedged daemon also stops honouring SIGTERM. A buffer regression would have
  surfaced as a 2m30s CI *timeout* rather than a failure. The channel was replaced with a
  mutex-guarded map; `Run` now returns 1.11 ms after release at 300 sessions, 3.37 ms at
  1000, 68.9 ms at 5000.

The recurring brief defect, nine instances of it, was **code that cannot affect behaviour**:
a stale-`last` guard whose two versions no input could distinguish, a `primed` field that
was write-only, a `pollInFlight` reset described as generation-scoped when the flag tracks a
goroutine, a mutation-table row that was unfalsifiable, a claim that a nil-normalisation was
load-bearing when Go's comma-ok returns true for a key holding nil, a `nameMin` that never
binds. Two brief defects would have caused real harm: a missing nil guard that a reviewer
reproduced as a panic at `model.go:473`, and the fail-open cleanup guard above.

**Every one of these was caught by mutation testing or adversarial review. None was caught
by reading a diff.** Self-review caught none of them either; the author's own verification
of one implementer's correction missed the race a reviewer then reproduced under `-race`, by
trying to break the invariant instead of confirming it.

Three things worth recording about the agents:

- **Subagents pushed back on a brief nine times and were right every time.** They reported
  a brief-mandated test as vacuous rather than banking a green suite; they refused to
  implement a dead field silently; they flagged that a mutation-table row could not fail;
  one added a `pending()` fixture helper unprompted because `Attention` is `SessionState`'s
  zero value and the brief's fixture would have masked the mutation via a zero-value
  coincidence. An implementer that says "I kept the brief's test verbatim but it is
  vacuous" is doing the job correctly.
- **One implementer's report claimed a fix that had not landed.** A reviewer caught it by
  re-running the mutation rather than trusting the report. The mechanism is worth knowing:
  the edit was made after the commit, never committed on its own, then wiped by a
  `git checkout --` used to restore an unrelated deliberate test mutation. "Commit before
  mutating" is not sufficient - **a fix discovered mid-mutation must be committed before the
  next mutation runs.**
- **One reviewer overturned an implementer's "unobservable, no test possible" judgement by
  writing the test.** Two of them, in fact: one wrote the ~30-line timeout test the
  implementer had deferred for want of a seam that turned out to already exist
  (`MockCommander.HandlerFuncs` receives the live context), and one wrote the assertion for
  a mutation the implementer had reported as unpinnable, at 5/5 over 2000 iterations.
  Declining to write a test on the grounds that it cannot be written should be treated as a
  claim to verify, not a verdict.

Two more lessons that generalise:

- **A stub that ignores its input caps how much any mutation can prove.** The critical gap
  in the query-trimming task existed because `MockCommander` returned canned JSON
  regardless of the query actually sent, so a mutant that sent the *wrong* query still
  received the *right* response and passed. Making the mock's answer depend on its
  arguments is what turns such a test from a shape-check into a real one. That applies to
  every `MockCommander` fixture in this codebase.
- **Verify a reviewer's severity claim before acting on it.** Twice this run a finding was
  overstated and amplified before being checked - once a "nondeterministic test" that was
  already sandboxed three ways out of four, once the ctags text framed as adversarial
  injection. Both corrections came from going and looking.

One thing about this document. The phase 2 retro recorded that its working ledger held the
defect list, the adjudications and the verification results, that the finishing workflow
deletes the workspace on merge because "git history is the record now", and that git history
held none of it - the phase 2 handoff exists only because someone reconstructed it before
the session ended. This document was originally written from the ledger at `a012fa2`, before
the workspace was touched, with every factual claim in it and in `CLAUDE.md` checked against
the code as it stood at that commit rather than against the ledger. That check is correct
only up to the commit it was run at: the very next commit, `76d7779`, restored
`TestFrozenThresholdsAdmitAUsefulName` and fixed the `internal/daemon/daemon.go` comment,
which falsified this document's claim that the layout comment "names a test that no longer
exists" and the deferred-minor entry asking for the daemon.go fix. Both are corrected above,
by a later review at HEAD `76d7779`, which also re-ran the 5-versus-7 parity measurement and
found the gap closed rather than merely predating a fix. Write the handoff before deleting
the workspace, not after - and re-check it against HEAD if any commit lands after it does.

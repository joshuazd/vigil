# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across sessions.

## Status: the six-phase design is complete

**All six phases are merged. There is no phase 7 and no in-flight design.** The approved
design that made the session list the primary surface is finished; the next piece of work is
a choice from "What is open" below, not a continuation.

Merge record, newest first. Each handoff is the authority on its own phase:

| Phase | Merged | vigil | `~/dotfiles` |
|---|---|---|---|
| 6 - deletions | 2026-08-03 | `393aa8f` | `46d844b` |
| 5 - work queue | 2026-08-03 | `b48979a` | not touched |
| 4 - dispatch through `vigild` | 2026-07-30 | `352b254` | phase 4 merge |
| 3 - panel by default | 2026-07-29 | `a785fb1` | `fefeeb1` |
| 2 blockers | - | `31721d4` | - |
| 0-2 | - | see handoffs | see handoffs |

Two pieces landed deliberately *between* phases, each to stop the next phase inheriting a
race: **asserted effect ownership** (`b8afd82`, before phase 4 - a cold dispatch was the named
trigger for the `firstSnapshotTimeout` loop) and the **collector async remote layer**
(`7b89c0e`, before phase 5 - it created the `poller` seam so phase 5's two network pollers
never touched `Snapshot`).

**Phases 0-4 spanned two repositories and neither half worked without the other**, so a change
in that territory - the launch path, panel creation, the dispatch hook chain - usually still
needs both. Phases 5 and 6 were single-repo, and phase 6's two halves were independent.

**Session hopping (2026-08-03; merged as `93372a9` here and `d095d34` in `~/dotfiles`) is not
in the table above and is not a seventh phase.** It is a defect-and-feature batch out of three user
complaints - the session order disagreeing with the `M-j`/`M-k` bindings, hopping requiring
vigil at all, and a session lingering in the list after `prefix d` - not a continuation of the
six-phase design. It did span both repositories.
`docs/superpowers/2026-08-03-session-hopping-handoff.md` is its authority, and it **corrects
its own design and plan on four points plus one measurement**; read it before either. **Its first
section is the one to read before testing anything**: `Session.ID` is additive with
`protocol.Version` still 1, so a new client fed by an *old* daemon sees `ID: 0` everywhere,
`(Created, ID)` degrades to `Created`, and the original bug is exactly what you get. Kill the
daemon after `make install`.

### Start here

1. **`docs/superpowers/2026-08-03-phase-6-deletions-handoff.md`** - the most recent phase, and
   the shortest. Its verification-limits section is the honest account of what a green suite
   did and did not prove.
2. **`docs/superpowers/2026-08-01-phase-5-work-queue-handoff.md`** - its **process notes are
   the most valuable page in this directory**. Read them before writing any plan here.
3. **`docs/superpowers/2026-07-30-phase-4-handoff.md`** - still the authority on the daemon's
   job runner and on `ExecCommander.Run`. Superseded on its two deferred items by
   `docs/superpowers/2026-07-30-binary-refresh-handoff.md`, which carries the landmine that the
   whole re-exec mechanism is **macOS-only**, and on the `Run` grandchild defect it left
   deferred by `docs/superpowers/2026-08-03-run-wait-delay-handoff.md`.
4. `docs/superpowers/2026-07-31-collector-async-remote-handoff.md` - the authority on the
   `poller` seam and on the `fillGit` measurement below.

## What is open

Roughly in the order they are worth doing. Nothing here is scheduled; the first two are the
ones that make the primary surface behave the way the design claims it already does.

The measurements and file references for each of these live once, in "Key Conventions" below.
This list is the priority order and the reason for it, not a second copy.

1. **`queueRowBudget` squeezes the session table to 3 rows** on a tall terminal with a full
   queue, inverting the design's premise that the session list is primary. A proportional
   policy would match the intent. Less urgent since the table got a viewport - those rows are
   now scrollable rather than unreachable - but 3 rows is still the wrong allocation.
2. **Stories can starve reviews out of the queue.** Every story sorts ahead of every review and
   `queue_limit` applies to the merged list, so 20 undeduped stories means zero review requests
   shown - and review requests were the feature's original motivation.
3. **No surface shows a Shortcut story state, and none shows a branch across all sessions at
   once.** Both were `worktree-status` columns, deleted in phase 6. Feature work either way;
   the interim answers are `short story <id>` and the detail panel one session at a time.
4. **The `{flags}` hook migration warning is effectively invisible.** Both `runTUI` and
   `runPanel` use `tea.WithAltScreen()`, so a panel never shows it and the `prefix v` popup
   destroys it on close. A user whose `config.toml` lacks `{flags}` gets teleported on their
   first queue dispatch and never sees why. A toast is the real fix.
5. **vigil's session order and the tmux bindings' order agree only while the user presses
   neither `s` nor `f`, and nothing detects a divergence.** They share `session_id` as the key
   now (`(Created, ID)` here, pure `session_id` in `~/dotfiles/scripts/scripts/tmux-hop`), but
   `CycleSort` and `CycleFilter` are live keys and a filter is the worse of the two: a shorter
   `visibleSessions` shifts every index below the filtered-out row, so `M-<n>` silently lands
   somewhere else. Accepted on the user's statement that they neither sort nor filter. There is
   also no automated check that the two orders agree at all - it has only ever been compared by
   hand, and the equivalence rests on `session_created` being monotonic in `session_id`, which
   is asserted nowhere and fails under a backwards clock step.
6. **Complaint 1 - a session taking "a few seconds" to leave the list after `prefix d` - is
   instrumented but uncaused.** It was never reproduced. The daemon now logs
   `session dropped: <name>` and `~/dotfiles`' `git-worktree-done`/`git-worktree-cleanup` carry
   **temporary** stamps to `/tmp/vigil-hop-timing.log`; collecting one real `prefix d` timeline
   is deferred to the human and has **not** been done. The leading untested hypothesis is popup
   occlusion; the `slow poll` lines discussed below are the other live candidate. Remove the
   dotfiles stamps once the timeline is read - the instruction is in the `TEMPORARY` comment
   above each `stamp` helper.
7. Smaller, each documented in a handoff: 80-column dashboards overflow their height by 1-2
   lines because the footer help line wraps; `getSelf` is Load-then-Run-then-Store rather than
   single-flight (unreachable today, `storyPoller.pass` is serialized by `passMu`); the cursor
   clamp in `applySnapshot` is a bounds check, not identity preservation, so a vanishing
   session can silently retarget the cursor from one queue item to another; the
   `if s.prevSessions != nil` guard in `daemon.logDroppedSessions` is dead (ranging a nil map
   is a no-op) and the ruling is to remove the branch and keep the loop; and `README.md:108`
   still documents the old, broken `notify` default.

**`fillGit` blocking publication used to be item 1 here and was demoted on 2026-08-03, because
the measurement it rested on did not reproduce.** The attribution did: `git status --porcelain`
is effectively the whole of `fillGit`, and no other git call crossed a 30ms threshold across
five timed polls. The magnitude did not: 0.138s cold and 0.009s warm, with the memo skipping
every poll, against the ~3.0-3.5s and never-skips recorded on 2026-07-31. The 20x is the
untracked cache - `-c core.untrackedCache=false` puts portal's `status` at 0.67-0.91s, dead on
the old figure - but `~/.gitconfig` has set `core.untrackedCache` and `core.fsmonitor` since
2026-01-02, months before that measurement, so the gap is **not fully explained**. Concurrency
contention (1 vs 9 simultaneous `status` calls: 0.065s vs 0.067s) and dirty-tree cache
invalidation (50 writes per iteration, `status` steady at 0.035s) were tested and ruled out.
Cold worktrees and machine load were not, and the cold-worktree exposure is only the *first*
`status` in a fresh worktree, so it is a one-or-two-poll hiccup after a dispatch rather than a
sustained cadence. `docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md`
is the design that was written for it and **deliberately not implemented** - read its problem
section before re-ranking this, and use the `slow poll` daemon log line as the evidence. **The
session-hopping design claimed zero `slow poll` lines in the whole daemon log as a third data
point for this demotion, and that count was simply wrong**: the same append-only log holds **at
least eight as of 16:19 on 2026-08-03, and at least nine as of 16:48** - the log is append-only
and never truncated, so any count here is a floor with a timestamp, never a total. Six of the
nine are 1.0-1.6s at `pr-35108`, a freshly dispatched worktree, which is the cold-worktree
story; one is 11.1s total with 11.085s in git, timestamped seven minutes before the design that
recorded zero. **Three of the nine are `sc-223374`, a warm long-lived worktree, and the
cold-worktree-first-`status` story does not explain those** - that is exactly the suspect this
bullet names as untested. So there is no third data point, the slow path is live on this
machine, and re-running the grep is the first thing to do before re-ranking this.

**Two silent-failure modes to know about before debugging anything in this area**, neither of
them scheduled for a fix: a failed queue fetch is completely silent, so "no assigned stories"
and "Shortcut is unreachable" render identically; and queue dedup hardcodes the `SC-<id> ` /
`PR-<number> ` naming convention that `~/dotfiles`' `session_name_from_title` produces, with a
test on the vigil side only, so if that format changes dedup degrades silently and the queue
advertises work already in flight.

## Do not "finish" these - they are deliberate

- **`lib/tmux.sh`'s `display-popup` branch (`run_worktree_popup`, around line 611) is not
  leftover phase 6 work.** It is the only code path for a manual `gh-review <url>` or
  `shortcut-implement <id>` run from a terminal; both call `run_worktree_popup` directly,
  never through the daemon's dispatch queue. Deleting it would silently turn every manual run's
  popup into inline output in the caller's own pane. Phase 6's popup item was the
  `dispatch-from-chrome` tunnel, which phase 4 had already removed.
- **The daemon never restarts itself** when its binary changes; it publishes
  `Snapshot.DaemonBin` and clients render `daemon outdated`. Designed, rejected, reasoning in
  the spec.
- **The remote pollers have no ticker.** They are woken only by `Snapshot`'s nudge, which is
  what makes "one daemon means one `gh` budget" true. Adding a ticker restores per-panel
  polling and only two tests would notice, and only for a *fast* ticker.
- **`--detached`'s gate lives in the workflow scripts, not in `run_worktree_popup`.**
  `gh-review` and `shortcut-implement` hardcode `--detached` into `run_worktree_popup`'s args
  regardless of their own flag, so `lib/tmux.sh:618` is unconditionally suppressed for them and
  live only for `gh-worktree`/`shortcut-worktree`. The gate that matters is each script's own
  `${detached}` check on its own teleport: `gh-review:196,216-217`,
  `shortcut-implement:196,229-230`. Phase 6's review caught this documented at the wrong place;
  do not re-derive it from the library.

**A standing warning about the plans in this directory. This is now the repository's default
failure mode, not a pattern.** Across three plans, **nineteen** briefs have contained tests
that would have passed with their subject deleted - six in the phase 3 plan, four in the
collector async remote plan, and **nine in the phase 5 plan**. Most were written by the plan's
author, including tests justified with reasoning that was factually wrong about this codebase.
Three of phase 5's nine survived **all ten per-task reviews** and were caught only by the
whole-branch review, all three on the primary render path.

What actually worked, in order of effectiveness:

1. **Mandating a mutation check per test, with the output pasted into the report.** Every test
   whose brief demanded "delete the subject, watch it fail, restore" came back sound. One
   implementer caught its own vacuous test this way before reporting.
2. **Reviewers re-deriving claims instead of reading them.** The reviewer who reimplemented a
   sort comparator in a standalone program found a key untested; the one who re-ran a 39,900-
   case overflow sweep found the fix worked and then found the cursor could still outrun it.
3. **A whole-branch review that explicitly distrusts the suite.** Per-task reviews were good
   and still missed all three seam gaps, because a task-scoped diff cannot see a seam.

So: where a brief and the shipped code disagree, **the code is right**; watch every test fail
before writing its subject; and budget a whole-branch review that samples tests nobody flagged.

**Phase 6 added a fourth data point, and it is a different shape from the other three.** It had
no tests to be vacuous - it was deletions - and the deletions themselves came through every
review clean. But the whole-branch review still returned **eight findings, and all eight were in
the prose about the deletions rather than in a deletion.** Two mattered: the `--detached` gate
was documented at the wrong place, and the accepted capability loss was recorded as one column
when it was two. Both had been "verified" by a per-task reviewer that re-derived the claim and
still reached the right accept decision on the wrong derivation - the failure was not laziness,
it was stopping one call frame too early.

The lesson generalises past tests: **a claim about a mechanism is only verified when you have
traced it to the line that actually decides.** For `--detached` that meant reading the callers,
not the library function they call. Two reviewers and the controller all stopped at
`run_worktree_popup` because a gate was visibly there, and it was the wrong gate. When a
handoff states a mechanism, check the call sites, not just the implementation.

Read these before changing the daemon, `internal/collect`, `internal/transition`,
`internal/dispatch`, `internal/view`'s layout, or the launch path in `~/dotfiles`:

- `docs/superpowers/specs/2026-07-31-phase-5-work-queue-design.md` and
  `docs/superpowers/plans/2026-07-31-phase-5-work-queue.md` - the design and plan for the
  work queue. The design is **superseded on two points**: its `queue_story_query` default
  used `%self%`, which nothing substituted until `24079de`; and it claimed `queue_limit`
  "bounds the rendered height", which is true in item count and was false in rows, fixed in
  `dca1243`. Its "Rejected alternatives" section is still current and worth reading - it is
  why the panel has a badge and no rows, and why the menu bar does not present the queue.
  **Nine of the plan's briefs contained vacuous tests**; the shipped versions are the fixed
  ones, and the plan's step-1 verification incantation is itself buggy (`HOME=... GH_TOKEN=
  "$(gh auth token)"` on one line evaluates `HOME` before the token capture).
- `docs/superpowers/specs/2026-07-31-collector-async-remote-design.md` and
  `docs/superpowers/plans/2026-07-31-collector-async-remote.md` - the design and plan for
  the poller seam. The design is **superseded on one point**, marked inline: it argues
  `fillGit` stays synchronous because git is "not the latency source", which the
  verification disproved. The plan's `TestRemoteStartIsIdempotent`,
  `TestPollIssuesNoGhCalls` and `TestADaemonFedClientSpendsNoGhBudget` as written were
  three of the four vacuous tests above; the shipped versions are the fixed ones.
- `docs/superpowers/2026-07-29-phase-3-handoff.md` - current state, what phase 3 changed,
  the verification results and what they do NOT prove, the deferred list and the landmines.
  **Start here.** Superseded on one point: its "The two-owner window is narrowed, not
  closed" section described the spawn grace, which no longer exists. Effect ownership is
  asserted now (daemon-only) - see "Key Conventions".
- `docs/superpowers/specs/2026-07-29-phase-3-panel-by-default-design.md` - the phase 3
  design, reconciled with what shipped. Its "What this does not fix" section is the honest
  account of the effect-ownership race the spawn grace left open; that race is closed, so
  read it as history rather than as current state.
- `docs/superpowers/plans/2026-07-29-phase-3-panel-by-default.md` - the phase 3 plan. Six
  of its briefs contained defects written by the plan's author, four of them tests that
  would have passed with their subject deleted; see the standing warning above. Read the
  handoff's process notes before trusting any brief in it.
- `docs/superpowers/2026-07-28-phase-2-blockers-handoff.md` - the state phase 3 started
  from. Still current on the daemon, the transition split and every landmine phase 3 did
  not touch. Superseded only where the phase 3 handoff says so.
- `docs/superpowers/2026-07-27-phase-2-handoff.md` - the state phase 2 merged at. Still
  the best account of the daemon and panel work, but superseded on the three blockers and
  on the claim that review-thread data is detail-panel-only.
- `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md` - the full design. Its
  "Still open after phase 2" section is no longer the whole debt list; the blockers
  handoff carries what is left.
- `docs/superpowers/specs/2026-07-27-phase-2-blockers-design.md` - the design for the
  blocker work.
- `docs/superpowers/2026-07-27-phase-1-handoff.md` - superseded, kept for the reasoning
  behind the daemon's polling cadences.

## Architecture

Go + Bubble Tea TUI. Single static binary.

- `main.go` — Entry point, dependency checks, tea.NewProgram. `main` is one line: the body is `run(args []string, stdout, stderr io.Writer) int`, which exists so exit codes and dispatch order are assertable. `vigil config get <key>` dispatches alongside `help` and `version`, i.e. **before** the `tmux`/`git`/`gh` `LookPath` check - a bash caller receiving "gh not found" instead of a value would silently disable the panel on a machine mid-setup, which is indistinguishable from `panel_auto = false`
- `internal/model/model.go` — Bubble Tea Model/Update/View, polling, state management
- `internal/model/keys.go` — Keybindings
- `internal/model/messages.go` — All tea.Msg types
- `internal/session/` — Session, GitStatus, PRStatus structs, SessionState enum, sorting. Also `QueueItem` (`queue.go`), whose `SessionPrefix()` encodes the `SC-<id> ` / `PR-<number> ` convention dotfiles' `session_name_from_title` produces. **That trailing space is load-bearing** - without it `SC-223477` matches a session named `SC-2234770`
- `internal/view/` — Rendering: table, detail panel, status bar, styles, cell formatters, and the queue section (`queue.go`). Pure: `RenderQueue` takes `now` and `maxRows` as parameters rather than reading a clock or guessing a budget. `QueueRowsShown` is shared with `Model.drawnQueueRows` so the cursor's ceiling cannot disagree with what was drawn
- `internal/fetch/` — Subprocess wrappers: tmux, git, gh CLI, Commander interface
- `internal/action/` — Merge, approve, cleanup, rebase, draft, dispatch actions
- `internal/config/` — TOML config loading, hook template expansion and execution. `RunHook` takes a `fetch.Commander` and runs `sh -c 'exec 2>&1; <hook>'`, so hook output includes stderr (which `MergePR` searches for "merged")
- `internal/cache/` — JSON session cache for instant startup
- `internal/collect/` - UI-independent session state collection (shared by the daemon and the TUI's self-polling fallback). **`Snapshot` is local-only**: tmux, bell flags and git, then a read of the PR store and a nudge. Off-box data is fetched by pollers on their own goroutines (`remote.go`: the `poller` seam, the `remote` scheduler, `prPoller`) and published by a later `Snapshot`. Phase 5's `storyPoller` and `reviewPoller` (`queue.go`) are siblings of `prPoller`, not stages in `Snapshot`; they share a `queueStore` and have **no `track`/`fill`**, because they hold global lists rather than per-branch data grafted onto sessions. `Collector.Queue(sessions)` is the read side - pure over both stores plus the session list, called once per poll by the daemon and by a self-polling client, never by `Snapshot`
- `internal/transition/` - state-change detection (`Detector`) and the side effects a change triggers (`Runner`: the `notify` hook and `auto_cleanup`). The two halves have different owners: `Detector` is shared, because every client renders its own toasts and detection must not be implemented twice, while `Runner` is constructed only by the daemon. Effect ownership is **asserted, not inferred** - see the "Key Conventions" bullet
- `internal/protocol/` - newline-delimited JSON over a unix socket, **bidirectional since phase 4**. The daemon writes `Snapshot`, clients write `Request`; direction alone disambiguates them, so there is no envelope. `Version` stays **1** because `Snapshot.Jobs`, `Snapshot.Queue`, `Snapshot.QueueHidden` and `Request.Detached` are all additive - an old panel ignores the key, a new one sees nil against an old daemon. An old daemon ignoring `Detached` means a queue dispatch teleports, which is exactly today's behaviour rather than an error. `RequestDecoder.Next` deliberately does not reject an unknown version: the daemon has to see such a request to answer with a refused job, and a drop at the decoder is indistinguishable from a daemon that never read
- `internal/dispatch/` - the submission client behind `vigil dispatch` and the `d` key. Validates input, generates a job id, dials (spawning a daemon and retrying if none answers), writes one `Request`, and waits for its id to appear in a snapshot. **The snapshot is the ack**; there is no response frame, which is what makes a refusal visible in every panel rather than only to the CLI. Does not import `internal/daemon` - `Options.Spawn` is a func field so `main` does the wiring
- `internal/daemon/` - `vigil daemon`: runs one `Snapshot` per tick at `tmux_interval` (default 1s) so tmux metadata (including bell flags) is never more than a tick stale; git state is gated inside `Snapshot` on `git_interval` (default 3s), while PR state per branch is fetched off the poll loop by the collector's remote workers and gated on `pr_interval` (default 30s) inside `prPoller.pass`. `Run` calls `Collector.Start` before the ticker and `Collector.Wait` in the shutdown arm. Startup serializes on an flock'd lock file beside the socket (`vigild.sock.lock`), held across the stale-socket removal and the bind, so racing daemons cannot both bind. Every client gets its own writer goroutine and a one-deep latest-wins queue, so a client that stops reading can neither stall the poll loop nor block new connections. `New` wires a `transition.Detector` and a `transition.Runner` (nil disables both, which is what a `Server` literal in a test gets); effects run in one goroutine per event because `poll` is synchronous per tick, and `Run` waits on `pendingEffects` before returning. **Phase 4 added a job runner**: one reader goroutine per connection accepts `Request` frames, and a serialized queue runs one dispatch at a time, because two concurrent `git worktree add` calls in one repository contend on the index lock. Jobs run on their own goroutine - `poll` is synchronous per tick, so a job run there would freeze every panel's stream for the length of a dispatch. `Snapshot.Jobs` is built from a copy taken under the job mutex, since a running job writes `Status` while poll marshals. The **writer stays the sole closer** of a connection: a reader that closed it could pull the socket from under a writer mid-`Encode`
- `vigil --panel` - the same `Model` with `panelMode` set: compact status bar, width-responsive table, no detail panel and no footer. Since phase 3 a panel is also created for every new tmux session, so this is the common way vigil runs, not the rare one. Spawning is no longer a panel-only behaviour: **every mode starts a daemon if none is running** - panel, dashboard and the `prefix v` popup, which is the dashboard model - at startup and on every failed reconnect probe, because the daemon is the only process that runs transition side effects and a dashboard-only user with `panel_auto = false` would otherwise never see the `notify` hook fire

## Testing

```bash
make test    # go test -race ./...  (-race is not optional: the daemon's design is a concurrency claim)
make lint    # golangci-lint
```

## Build & Install

```bash
make build     # compile binary
make install   # install to ~/.local/bin/vigil via a temp file and rename, never in place
```

## Key Conventions

- ANSI colors (adapts to terminal theme, no hardcoded hex)
- No global mutable state — config/caches passed explicitly
- Commander interface for subprocess calls (testable). Every subprocess goes through it, hooks and the browser opener included. The only direct `exec` sites left are `internal/fetch/cmd.go` (the real `Commander`) and `internal/model/client.go` (the daemon spawn), which must be real processes
- View is pure — pane capture in Update via tea.Cmd, not in View
- context.Context for cancellation
- Both the daemon and a self-polling client run one `Collector.Snapshot` per `tmux_interval` (default 1s); git work is gated inside it by the `git_interval` (default 3s) memo, and PR work per branch by the `pr_interval` (default 30s) check inside `prPoller.pass`, off the poll loop. The TUI has no separate tmux/git/PR tick cycles - `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd` and their messages and ticks are gone. **Whether the `git_interval` memo actually skips depends on the repository** - it did not on 2026-07-31 and does on 2026-08-03; see the `fillGit` bullet below for both measurements and for the log line that settles it on any given machine
- `Collector.Snapshot` is not reentrant: `gitMemo`, the last lock-free memo here, is owned by the calling goroutine (the PR store is mutex-guarded instead, because a worker writes it while `Snapshot` reads). `Collector`'s exported fields and `clock` are read-only once `New` returns, since `prPoller.pass` reads `Cmd`, `PRInterval` and `clock` from its own goroutine. `Model.startPoll` is the only issuer of `collectCmd` and refuses while `pollInFlight`, so at most one `Snapshot` is ever in flight per client. The self-poll chain is a self-rescheduling one-shot `CollectTickMsg`, created at exactly two sites (`Init`'s fallback branch and `handleDaemonLost`) and continued only in the tick handler
- **Transition side effects belong to the daemon and to nothing else. Ownership is asserted, not inferred.** `Model.checkStateTransitions` detects transitions and renders them - a toast per event, plus `maybeAutoFocus` - and runs no effects at all. Clients hold no `transition.EffectRunner`; `transition.Runner` is constructed only in `internal/daemon`. This replaced a timer (`spawnGrace` / `effectsDisownedUntil` / `daemonSeenSinceArm`) that tried to infer ownership from who owned the poll loop and could only *narrow* the window where two processes owned one event: `handleDaemonLost` started self-polling while the daemon it lost may still have been alive, `firstSnapshotTimeout` could loop a panel through connect/timeout/self-poll, and the per-process `inFlightEffects` could not help across two processes - one `Done` event then meant two `CleanupSession` calls against one worktree. None of that mechanism exists any more; do not reintroduce a client-side effect path
- The price of asserted ownership, and it is deliberate: **while no daemon is running, the `notify` hook does not fire and nothing is auto-cleaned.** A client self-polling for data is a data path, not an owner. This is why every mode now spawns a daemon when none answers and retries on every failed probe (`spawnDaemonOnce`, floored at `spawnCooldown` 15s per process). Toasts are unaffected - they are per-client, ungated, and fire on both the daemon-fed and self-polling paths
- `Done`-bound effects are serialized per session **in the daemon** (`daemon.Server.inFlightEffects`), because a merged session that gets a bell re-enters `Done` and would otherwise run two `CleanupSession` calls against one worktree. That hazard is now entirely within one process. Every other transition dispatches ungated. `auto_cleanup` failures go to the daemon log, not to a client
- `transition.Runner` refuses cleanup for a session that any client is attached to (`fetch.AttachedSessions`), and for a malformed event (empty `Session`, `PanePath` or `GitRoot`, logged). Both guards fail closed: if tmux cannot be reached, or the session is absent from tmux's list, cleanup is skipped
- The review-threads poll fetches only the unresolved count (`reviewThreadsQuery` has no `comments(` connection). Comment bodies are fetched on demand for the selected branch (`FetchReviewComments`) and cached by branch in `Model.reviewComments`; the cache is only cleared by `r`
- Detail panel: three modes (pane, PR description, review comments) with auto-select by state. Comments mode costs one `gh api graphql` call the first time a branch is viewed
- Session filtering by state, sorting by created/state/alpha, batch operations via multi-select
- **`SortCreated` compares `(Created, ID)`, and the second key is not optional.** `#{session_created}` is whole seconds, so ties are common; the tie used to fall through the stable sort to `ListSessions`' alphabetical order while `~/dotfiles`' `M-j`/`M-k` ordered by `#{session_id}`, which is exactly why the two disagreed. **Not pure `ID`**, because `ID` 0 means a session hydrated from a cache file written before `json:"id"` existed - `(Created, ID)` degrades to today's behaviour for those, where pure `ID` would hoist every one of them to the front until the first poll landed. `ID` 0 is also legitimately tmux's first session (`$0`) and the comparator is right either way: 0 sorts first and a real `$0` is the oldest. **The same degradation makes the fix inert against an old daemon**, which is the first thing to check when `M-j` still looks broken: `Session.ID` is additive and `protocol.Version` stayed 1, so an old daemon's snapshot has no `id` key, every session decodes to `ID: 0`, the tie falls through to `Created` alone and lands back on the original bug. The daemon never restarts itself by design, so `make install` is not enough - kill it. Nothing warns and no test can catch it: the degraded behaviour *is* the old behaviour. The equivalence with the bindings' pure-`session_id` order holds only while `session_created` is monotonic in `session_id`, which is **not** guaranteed - a backwards clock step between two creations makes them disagree for that pair - so do not restore the "provably the same total order" wording the design shipped with. The index column needs nothing: `RenderTable` passes its loop index `i` to `renderRow`, which passes it to `indexCol`, so the label is absolute rather than viewport-relative and scrolling cannot shift it; `indexCol` blanks past 9, which is why there are ten digit bindings and no eleventh. **`~/dotfiles/scripts/scripts/tmux-hop` is the other half of this contract and must never invoke vigil** - not the binary, not the daemon, not the socket - because tmux navigation has to work on a machine with vigil uninstalled. That is what makes the two orders one convention rather than two implementations. One caveat on the digit bindings: `LayoutForWidth` drops the index column below `tierNoGit` (41), so the landscape panel's default 40 columns renders no index for `M-<n>` to match, and the numbers exist only in the dashboard or a panel 41 columns or wider
- **The default `notify` hook's adjacent quoting is correct and the readable form has never worked.** `tmux display-message -d 5000 "vigil: "{session}" → "{new_state}` (`internal/config/config.go:66`) closes the literal before each placeholder and reopens after it, so the shell concatenates the pieces into the one argument `display-message` accepts. `ExpandHook` guarantees **one shell-quoted word per placeholder**, which is the whole reason `"vigil: {session} → {new_state}"` cannot work: the placeholder lands as `'...'` inside the hook's own `"..."`, and dotfiles' `session_name_from_title` produces names containing literal double quotes that close that string early and split the message in two. Every fire failed with `command display-message: too many arguments (need at most 1)` - 22 such lines on 2026-08-03 alone, 26 in the log, and zero successes ever. `rawPlaceholders` was **not** widened to fix it and must not be; the hook body was what was wrong. Any hook needing a placeholder inside a larger string has to concatenate the same way
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold
- Draft toggle (`D`) with batch support
- Auto-cleanup merged sessions (configurable via `auto_cleanup` setting, off by default). Safe to enable with any number of panels open: only the daemon runs it. Verified on a real machine on 2026-07-29, before asserted ownership landed, under the weaker guarantee of the time: with two panels attached, four sessions reaching `Done` produced four hook invocations rather than eight; an unattached session's worktree was removed and a session with a client attached survived. That measurement is still valid but no longer the binding argument - the client has no code path that could produce the eight
- **`vigil dispatch` exits 0 on acceptance, not success.** The job outlives the CLI, which is the point. A refusal - duplicate input, unknown request version or type, empty input, full queue - is registered as a job in state `JobRefused`, never silently dropped, because the submitting client waits for its id to appear in a snapshot and a drop is indistinguishable from a daemon that never read the frame. `JobRefused` is distinct from `JobFailed` on purpose: refused means never accepted, failed means accepted, ran and lost. Conflating them made the CLI exit non-zero for work the daemon had actually started
- **`VIGIL_CLIENT` is how a daemon-run job learns which tmux client to act on.** The daemon has no tty, so it resolves the most recently active client per job and exports it into the hook's environment. The shell side uses it for three things: the switch target, the new window's size, and the panel orientation. It is an environment variable rather than a flag because the alternative threads a parameter through five levels, one of which re-quotes into a command string with `printf '%q'` and runs it through `bash -c`. Verified 2026-07-30 on an isolated server: with it, a session comes out 350x90 with a 40-column panel; genuinely headless, 80x24 with a panel at half the window, which is the ~175-column balloon's precondition
- **A hook's grandchildren can hold its output pipe.** `exec.CommandContext` kills only the direct child, so `cmd.Wait` blocks until every descendant closes the inherited fd. `ExecCommander.RunStream` therefore uses a process group, a `Cancel` that signals the group, and a `WaitDelay` backstop. Without it a hung dispatch was unbounded, blocked every later job, and left `Run` waiting on `pendingEffects` forever - a daemon that never exits, never releases its flock and never unlinks its socket, after which **no daemon can start again at all**. `ExecCommander.Run`, the non-streaming path used by `notify` and `cleanup`, **had the same shape and was fixed on 2026-08-03**: `cmd.Output()` collects into buffers, which os/exec also copies through a goroutine, so a hook that exited 0 and left a descendant behind blocked `Wait` for that descendant's whole life - measured at **30.0s for a `sleep 30 &`, bounded to 2.0s after**, at the `RunHook` seam rather than in the commander. Cancellation cannot help on that path and that is the point: the direct child has already exited, so there is nothing left to kill, which is why the delay rather than a process group is the fix. **`Run` deliberately did not get `RunStream`'s process group kill** - killing descendants is right for a dispatch that must actually die, and wrong for a `notify` hook whose backgrounded work is what the user asked for. **Both paths now map `exec.ErrWaitDelay` to success when the process exited 0** (`exitedCleanly`), because the delay alone converts such a hook into a failure it is not: on `Run` that reaches `MergePR`, which reads hook output to decide whether a PR merged, and on `RunStream` it marked a dispatch that had actually worked as a failed job in every panel - that one was live, not hypothetical, and the test for it failed before the fix. Measured 2026-08-03: os/exec gives the exit status precedence over the delay, so `ErrWaitDelay` never surfaces for a failed or killed process at all (`exit status 3`, `signal: killed`), which makes `exitedCleanly`'s `ProcessState` check a fail-closed backstop with no live path - pinned by a direct unit test rather than left as an unexercised guard
- **The `dispatch` hook must be `DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}`.** A *literal* `--detached` in the hook skips the teleport for every dispatch, and without `DISPATCH_INLINE` a client-less daemon tries `tmux display-popup`, which has nothing to draw on. `{flags}` expands to `--detached` for a queue selection and to nothing for the `d` key, which is how one hook serves both. **It is the only placeholder `ExpandHook` does not shell-quote** - safe because it carries one of two vigil-chosen constants, and necessary because `shellQuote("")` is `''`, a stray empty argument. `rawPlaceholders` must never be widened. `vigil` warns at startup for both an old-style hook and a missing `{flags}`, but **that warning is effectively invisible where it matters**: both `runTUI` and `runPanel` use `tea.WithAltScreen()`, so a panel never shows it and the `prefix v` popup destroys it on close. A user without `{flags}` gets teleported and never sees why. Note also that **no hook body may contain `${VAR}`**: `ExpandHook` reads every `{...}` as a placeholder, so a braced shell expansion fails before reaching `sh`. Use `$VAR`
- Cache interop with previous Python version (same JSON format)
- The TUI dials the daemon socket on startup and consumes its broadcast snapshots when reachable; it falls back to self-polling if the daemon is never reached, does not send a first snapshot within a bounded wait, or is lost mid-session
- Both paths are permanently supported and must **render** identically: git/PR data, sort order, toasts. They no longer behave identically in effects, and that is the point - the self-polling path runs none, so a `notify` hook is a claim about a daemon being up, not about a client having seen a transition. The one exception to "the `notify` hook fires once per real transition" survives inside the daemon: a repeat `Done` arriving while that session's cleanup is still in flight is skipped along with its hook (measured at 5 invocations for 7 transitions with the first effect blocked; toasts were 7 of 7 on both paths, and still are)
- `model.New` loads the session cache synchronously for every mode, so first paint is never blank on either path; nothing about the cache is emitted as a message
- The session cache path is an explicit `Model.cachePath` field (mirroring `daemon.Server.CachePath`), resolved once in `newModel`, never at the point of use. Empty disables both load and save, and `newTestModel` leaves it empty - that, not any guard inside `applySnapshot`, is what stops the test suite writing the developer's real `~/.local/share/vigil/cache.json`
- `r` clears the per-branch review-comment cache unconditionally, then asks for a forced poll. The forced poll is silently refused when a daemon is feeding the client: the daemon owns the cadence and the client cannot reach its memos. No toast
- A session missing PR data falls back to the last known PR for its branch (`prCache` client-side, a per-branch memo in `collect.Collector` daemon-side), so one failed `gh` call cannot blank the PR column or fire a spurious idle transition
- The table drops columns as width shrinks (`view.LayoutForWidth`). At width >= 104 the layout is exactly what it always was: the name column is capped at 52 and never stretches
- The tier selection widths (`tierFull` = 60, `tierNoGit` = 41, `tierCompact` = 28, `tierNoPR` = 15, `tierBare` = 4) are tuned constants frozen at the values verified on real panes, NOT derived from the fixed costs. Deriving them would move `tierNoGit` to 39 and width 40 - the landscape panel's default - would stop choosing the compact tier. `TestTierBoundariesAreFrozen` pins the tier chosen on both sides of all five boundaries, and `TestPanelWidthStillPicksTheCompactTier` pins width 40, the one boundary with a real-machine verification behind it. `TestFrozenThresholdsAdmitAUsefulName` (`internal/view/layout_test.go`) asserts the `threshold >= fixed + nameMin` invariant and is what the `layout.go` comment naming it refers to; lowering `nameMin` is still caught by nothing
- `nameMin` (8) never binds: the narrowest name the top four tiers yield at their own thresholds is 9 or 10, and the bare tier clamps to 1 instead. It documents the floor the frozen thresholds must respect rather than clamping anything, and lowering it is not caught by any test
- Every self-rescheduling tick carries an `epoch`. Bubble Tea ticks cannot be cancelled, so switching between daemon snapshots and self-polling bumps the epoch and the previous generation's ticks retire themselves
- A client that loses the daemon self-polls and probes the socket every 2s until it is back. A connected but silent daemon shows `daemon stale Ns` in the status bar after three poll intervals
- `panel_auto` (env `VIGIL_PANEL_AUTO`, default `"true"`) is the on/off switch for the phase 3 behaviour, read by bash as `vigil config get panel_auto`. Geometry stays in tmux user options because it is per-client and per-monitor; *whether the user wants panels at all* is a vigil preference and belongs in the file the user already edits. `config.IsSetting` exists because `GetSetting` cannot tell an unknown key from a setting that is legitimately empty, which `capture_window` is by default
- Any test that reaches `config.Load(config.ConfigPath())` must set its own `HOME`. `ConfigPath` resolves under `os.UserHomeDir`, so without one the suite reads the developer's real `~/.config/vigil/config.toml` - and `panel_auto = "false"` there, the documented way to turn the panel off, turned the suite red. Same class as the `cachePath` note above, different file
- Panel geometry is tmux's concern, not vigil's: vigil renders to fit whatever pane it is given and never chooses or changes its own size. Both panel creation paths live on the dotfiles side (`~/dotfiles`, `scripts/scripts/lib/tmux.sh`) - a separate repository. `add_vigil_panel <window-target>` is the one implementation of "split a panel and mark the pane"; `vigil-panel` (bound to `prefix p`) is a thin toggle over it, and `create_tmux_session` calls it for every new session
- The dotfiles half has one non-obvious rule vigil readers keep rediscovering: `create_tmux_session` runs `tmux new-session -d`, and a detached session has no client, so tmux sizes its windows to `default-size` 80x24. A 40-column panel is then half the window and tmux scales it to ~175 columns when a 350-column client attaches. `new-session` therefore takes `-x/-y` from the calling client (omitted entirely when there is no client - real tmux rejects an empty `-x` and would cost the user the session). `prefix p` never had this problem, because the window it splits is already at client size
- `esc` unwinds one layer per press: confirm prompt / multi-selection / dispatch prompt, then failed and refused dispatch jobs, then quit. The dismiss layer sends a `RequestDismiss` frame **with an empty ID**, so an old daemon drops it via `jobs.submit`'s empty-ID guard rather than registering an undismissable refusal for a type it does not know.
- Every vigil client stats its own executable every `binCheckInterval` and re-execs when size or mtime changes, deferring while a prompt or a selection is open and never within `binRestartFloor` of its own start. **The changed stamp must be seen on two consecutive checks**: `make build` writes `./vigil` in place, and exec'ing a half-written file kills the pane. A startup probe that failed leaves no baseline at all, so the first stamp a process can actually get is adopted rather than compared against zero. `checkBinary` reports whether to quit and both tick arms return `tea.Quit` when it does - the exec itself happens in `main` after `p.Run()` returns, because Bubble Tea owns the terminal inside `Update`. **macOS only**: on Linux `os.Executable()` resolves to `"/path/vigil (deleted)"` after a rename-over, the stat fails, and the whole feature (including `daemon outdated`) is a permanent, fail-closed no-op.
- **The daemon never restarts itself.** It publishes `Snapshot.DaemonBin` and clients render `daemon outdated`. Restarting it would drop every connection, so every panel would bounce through daemon-lost on every install. This was designed, rejected, and the reasoning is in the spec - do not reintroduce it.
- **`gh` exiting non-zero is not always a failure.** `runWithRetry` retries three times with 1s and 2s of backoff, and `gh pr view` on a branch with no PR exits 1 to say so - which cost ~4.5s per poll on every freshly dispatched session, all of it inside a synchronous `Snapshot` that publishes nothing until it returns. `definitiveAnswer` reads gh's stderr off the `*exec.ExitError` and returns immediately for a true answer. It matches on gh's English message, so a gh release that rewords it silently restores the old behaviour
- **`Collector.Snapshot` does local work only** - tmux, bells, git - then reads the PR store and nudges the remote workers. Nothing it does blocks on the network. Every process that calls it must call `Collector.Start(ctx)` once, or no off-box data is ever fetched; the daemon also calls `Collector.Wait()` before `Run` returns, so it does not release its flock and unlink its socket with a `gh` child alive. `Start`'s context is sticky (`sync.Once`), so the first caller's wins permanently and an already-cancelled one disables the pollers for good
- **The remote pollers have no ticker, and that is load-bearing.** They are woken only by `Snapshot`'s nudge. A daemon-fed client never calls `Snapshot`, so its workers block for the life of the process and spend no `gh` budget - which is what "one daemon means one `gh` rate-limit budget" actually rests on now. Adding a ticker restores per-panel polling for every open panel, and only `TestRemoteRunsNothingWithoutANudge` and `TestADaemonFedClientSpendsNoGhBudget` would notice - and only for a *fast* ticker. A `time.NewTicker(PRInterval)` at 30s passes both. The doc comment and review are the defence, not the suite
- **`fillGit` is where a slow poll comes from, but how slow depends entirely on the worktree's index, not on vigil.** `git status --porcelain` is effectively all of it - measured twice, and this half of the claim held both times: on 2026-08-03, across five timed polls logging every subprocess over 30ms, `rev-parse --show-toplevel`, `branch --show-current`, `rev-parse --verify`, `rev-list --count`, `merge-base` and `log -1` never appeared once. The magnitude is the part that moved. **2026-07-31, 9 worktrees: `fillGit` ~3.0-3.5s per poll, 99.7% of `Snapshot`, 0.7-1.6s per active `portal` worktree, and because `fillGit` >= `git_interval` the memo could never skip. 2026-08-03, 2 worktrees: `fillGit` 0.138s cold, `Snapshot` 0.009s warm, the memo skipping every poll.** The 20x is the untracked cache: `-c core.untrackedCache=false` reproduces 0.67-0.91s on demand, which is how to get the slow path back for debugging. That does not fully explain it - `~/.gitconfig` has set `core.untrackedCache` and `core.fsmonitor` since 2026-01-02 - and concurrency contention and dirty-tree invalidation were both tested and ruled out (see the demotion note under "What is open"). So treat "the memo never skips" as **conditional, not current**: the shape is real, taking `gh` off the path in `7b89c0e` **relocated** rather than removed it, and the remaining suspects are a fresh worktree's first `status` and machine load. **Do not re-derive this from scratch a third time** - the daemon logs `slow poll: <total> total, <git> in git, slowest <d> at <path>` whenever a poll exceeds `tmux_interval`, rate-limited to once a minute
- **The `slow poll` log line is daemon-only and deliberately not user-visible.** A slow poll is not a failure, and a persistent warning about a 100ms poll is noise; a self-polling client has no log at all. `Collector.GitStats()` is the seam it reads, and it inherits `Snapshot`'s threading rule: the `fillGit` half is goroutine-owned and unguarded like `gitMemo`, so only the goroutine that called `Snapshot` may read it, and only after `Snapshot` returns. It is reset at the top of every `Snapshot` so a poll that fails before `fillGit` reports zero rather than the previous poll's numbers, and it stays zero when every path was memoized, because then `fillGit` issued no subprocesses. The rate limit is a **window, not an edge trigger** like `pollFailing`: a genuinely slow machine would otherwise log once on the way in and go quiet for hours, and a diagnosis wants a series. `TestASlowPollLogsAgainOnceTheRateLimitWindowHasPassed` is the only thing standing between the window and a log-once-forever mutation
- `session.PRPending` means the branch has no entry in the PR store at all, which is **not** the same as a branch known to have no PR. `transition.Detect` skips a session where `PRPending && PR == nil`: no seed, no event, not recorded in `next`. Without it, async fill turns every daemon start into a burst of `notify` hooks and an `auto_cleanup` run against every already-merged worktree - measured at 6 hooks in one instant with the skip removed, 0 with it. The `PR == nil` half is there because a client fills the last known PR from `prCache` first. It costs at most one `notify` hook per session, at daemon start
- `Collector.Invalidate` makes remote entries due by **zeroing `fetchedAt` rather than dropping them**. Dropping them would re-mark every branch pending, and a pending session is skipped by `Detect`, so a forced refresh would swallow the transition it was pressed to find. Git comes back inside the next `Snapshot`; remote data comes back a tick later. The git half is still goroutine-owned and unguarded, the remote half is safe from anywhere
- `Collector.RefreshRemote` runs one pass of every poller synchronously and exists **so tests do not race a goroutine**. Production reaches a pass only through `Start`, so a nudge that never reached a worker leaves every `RefreshRemote`-driven test green; only `TestRunStartsTheRemoteWorkers`, `TestNewStartsTheRemoteWorkers` and `TestADaemonFedClientSpendsNoGhBudget` catch that
- **The work queue is dashboard-only. The panel gets a `⚡N` badge and zero rows**, because a panel is 152x9 on this machine with five sessions already in it. `Model.rowCount` returns just the session count in panel mode and `queueCursor` returns -1, so a panel's cursor cannot reach a queue row at all - without that, `enter` would fire a detached dispatch of an item the panel never drew. The dashboard has the same guard by a different route: `rowCount` ends at `drawnQueueRows()`, and `queueDispatchTarget` bounds on it again so it fails closed even if a clamp has not run
- **`drawnQueueRows()` and `View()`'s queue budget must stay one function.** Both call `queueRowBudget(tableHeight)` and `view.QueueRowsShown`. If they ever each compute the budget separately they will drift, and the drift is exactly the bug: the cursor reaching a row that was truncated away. This was shipped broken once and caught by measurement, not by reading
- **`lipgloss.Height("")` returns 1, not 0.** The `queueSection != ""` guard before subtracting it from `tableHeight` is why an empty queue does not silently cost the session table a row
- **The status bar budgets width with `lipgloss.Width`, not `visibleLen`.** `⚡` is one rune and two terminal cells, so a rune count under-budgets and the bar wraps, pushing every table row down - measured at widths 12, 25 and 34. `job.go:11-18` had already diagnosed this class once for the job line; nothing guarded the status bar until `eb6bbeb`. Do not revert it to a rune count, and prefer `lipgloss.Width` for any new width arithmetic
- **`truncateVisible(s, maxW)` returns at most `maxW` cells, ellipsis included.** It used to return `maxW` characters and *then* append `…`, so every caller padding to `maxW` overflowed its column by one. Four tests covered that helper and none pinned its width; the fix came out of a caller's width sweep
- **`%self%` in `queue_story_query` is substituted by vigil, not by `short`.** It is a `short search` feature, and vigil uses `short api`, a raw passthrough that templates nothing. `fetch.SearchStories` resolves it through `short api /member`, cached in a package-level `sync.Map` following the `nwoCache` precedent, looked up only when the query contains it, and **erroring rather than issuing a query with a literal or empty owner** - a broken query returns zero stories silently and forever. This shipped broken and was caught only by running against the real API
- **Queue dedup hides an item when a live session covers it**, by name prefix (`SC-<id> ` / `PR-<number> `) or, for reviews only, by `PR.Number`. The name key is a **cross-repository convention with a test on this side only**: if dotfiles' `session_name_from_title` changes format, dedup degrades silently and the queue advertises work already in flight. The `PR.Number` backstop covers reviews; stories would be fully exposed
- **A failed queue fetch is completely silent** - no log, no toast. "No assigned stories" and "Shortcut is unreachable" render identically. Consistent with `prPoller`, which does not log a failed `gh` either, and the reason `short` is deliberately absent from `main.go`'s `startupDependencies`. First diagnostic step is to run the poller's exact command by hand
- **`queueRowBudget` gives the queue everything above `minTableRows = 3`.** With many sessions and a full queue the session table is squeezed to 3 rows even on a tall terminal, which inverts the design's premise that the session list is primary. A proportional policy would match the intent better; this is open
- **The session table scrolls; the queue truncates. They are different fixes to the same defect, on purpose.** `view.TableWindow(cursor, count, height)` returns the first session `RenderTable` draws, so a cursor past the allocated height scrolls instead of addressing a row nobody drew. The queue solved its half by *forbidding* the cursor (`rowCount` ends at `drawnQueueRows()`, plus a `… +N more` line), which is right for queue items - extra work you can ignore - and wrong for sessions, which are what the dashboard is for. **This fixed the panel as well as the dashboard**: `panelView` passes `m.height-1` to the same `RenderTable`, and panel-mode `rowCount` is just the session count, so a 9 row panel with 10 sessions had the same unreachable rows. **The window is derived from the cursor, never stored.** A remembered offset would be a second piece of state that has to agree with the cursor and would need its own clamp in `applySnapshot` next to the cursor's; one computed from the cursor cannot disagree with it. Edge-pinned rather than centred because the case it exists for is a 3 row table, where a centred window scrolls on every keypress. **`cursor` may point past the last session** - that is how the Model says "the cursor is on a queue row" - and the `count-height` clamp is what holds the table still as the cursor crosses into the queue instead of dragging it further or snapping it to the top. An explicit `min(cursor, count-1)` alongside that clamp was written, found to be provably the same answer, and removed: with both present neither could be mutated away by a test. The `count <= height` short circuit is *not* redundant with it - at `count < height` the clamp alone returns a negative offset. There is deliberately no `… +N more` row: it would cost one of three rows in exactly the squeezed case this exists for, and the status bar already carries `N sessions` plus the per-state counts
- **There is no vigil equivalent of `worktree-status`'s Shortcut story-state column, and its Branch column is only a partial match.** Deleted in phase 6: it showed session, branch, git status and the story state for the `sc-NNNN` in the branch name. vigil's session table renders Indicator, Index, Name, Git, PR and State (`internal/view/table.go` `renderRow`, `internal/view/layout.go` `TableLayout`) and `session.Session` carries no story field. The git column (`GitColWithBg`, `internal/view/format.go:83-125`) renders `~N +N -N ↑N` and rebase age, not a branch name; the branch exists only in the dashboard detail panel for the selected session (`internal/view/detail.go:45-48`), and panel mode has no detail panel at all. So the loss is two columns, not one: story state entirely, and at-a-glance branch across every session. The work queue shows stories, but only assigned-and-not-done ones, and hides any a live session already covers - the opposite of "what state is this session's story in right now". Remaining ways to check: `short story <id>` or the Shortcut web UI for story state; the dashboard detail panel, one session at a time, for branch. Adding a story column and a compact branch indicator is feature work, not a phase 6 deletion, and is open

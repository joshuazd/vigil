# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across sessions.

## In-flight design work

An approved 6-phase design is turning the session list into the primary surface, with
sessions expanded next to it. **Phases 0, 1, 2, 3 and 4 are merged.** Phase 2's three
blockers landed as `31721d4`. Phase 3 landed 2026-07-29 as `a785fb1` here and `fefeeb1` in
`~/dotfiles`. Phase 4 landed 2026-07-30 as `352b254` here and the phase 4 merge in
`~/dotfiles`. Both spanned **two repositories** and neither half works without the other, so
a change to one usually needs the other.

Separately, **asserted effect ownership** landed as `b8afd82`, ahead of phase 4 and on
purpose: a cold dispatch was the named trigger for the `firstSnapshotTimeout` loop, so phase
4 would have inherited and widened the race it is now built on top of.

**Phase 5 is next: the work queue.** `vigild` also polls assigned Shortcut stories and
review-requested PRs; both vigil and the menu bar present a pickable list, and selecting an
item dispatches it. The same rule as before applies - live on phase 4 first. Phase 4 is the
phase that makes the daemon run jobs, which is the condition phase 5 inherits.

**Read these three, in this order, before starting phase 5:**

1. **`docs/superpowers/2026-07-31-collector-async-remote-handoff.md`** - the structural fix
   the poll-latency handoff called for, landed and verified. `Snapshot` is local-only, a
   `poller` seam owns off-box data, and **phase 5's two pollers are additions to that seam,
   not stages in `Snapshot`**. Carries the measured proof that the cold-start `notify` burst
   is prevented (0 hooks against 6 with the detector skip removed), and the finding that
   changes phase 5's plan: **`fillGit` costs ~3s per poll**, is >= `git_interval`, so the git
   memo never skips and the real publication cadence is ~3s rather than 1s. The "one slow
   thing blocks publication" shape moved from `gh` to `git`; it is not gone. Supersedes the
   poll-latency handoff, which is still worth reading for the `definitiveAnswer` history
   (`docs/superpowers/2026-07-31-poll-latency-handoff.md`, merged as `6874acf`) and whose
   "1.5 to 2 seconds" prediction the verification **refutes for cold start**.
2. **`docs/superpowers/2026-07-30-binary-refresh-handoff.md`** - phase 4's two deferred items,
   landed 2026-07-31 as `832a86e` and verified on the real machine. `esc` now clears a failed
   dispatch line for every panel at once, and a client re-execs when its own binary changes.
   Carries the operational note that **the first install after a change cannot re-exec
   anything**, because a panel must already be running the feature to notice, and the
   landmine that the whole re-exec mechanism is **macOS-only**.
3. **`docs/superpowers/2026-07-30-phase-4-handoff.md`** - what phase 4 verified, what it did
   **not**, and the landmines. Still the authority on the daemon's job runner and on
   `ExecCommander.Run`, the non-streaming path used by the `notify` and `cleanup` hooks,
   which **still** has the grandchild-holds-the-pipe defect that phase 4 fixed in
   `RunStream`. A hook that backgrounds a process wedges the daemon permanently; shipped
   defaults do not, which is the only reason it was left. Superseded only on its two deferred
   items, which are (2) above.

Read these before changing the daemon, `internal/collect`, `internal/transition`,
`internal/dispatch`, `internal/view`'s layout, or the launch path in `~/dotfiles`:

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
  would have passed with their subject deleted. Where a brief and the shipped code
  disagree, the code is right. Read the handoff's process notes before trusting any brief
  in it.
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
- `internal/session/` — Session, GitStatus, PRStatus structs, SessionState enum, sorting
- `internal/view/` — Rendering: table, detail panel, status bar, styles, cell formatters
- `internal/fetch/` — Subprocess wrappers: tmux, git, gh CLI, Commander interface
- `internal/action/` — Merge, approve, cleanup, rebase, draft, dispatch actions
- `internal/config/` — TOML config loading, hook template expansion and execution. `RunHook` takes a `fetch.Commander` and runs `sh -c 'exec 2>&1; <hook>'`, so hook output includes stderr (which `MergePR` searches for "merged")
- `internal/cache/` — JSON session cache for instant startup
- `internal/collect/` - UI-independent session state collection (shared by the daemon and the TUI's self-polling fallback). **`Snapshot` is local-only**: tmux, bell flags and git, then a read of the PR store and a nudge. Off-box data is fetched by pollers on their own goroutines (`remote.go`: the `poller` seam, the `remote` scheduler, `prPoller`) and published by a later `Snapshot`. Phase 5's Shortcut and review-request pollers are siblings of `prPoller`, not stages in `Snapshot`
- `internal/transition/` - state-change detection (`Detector`) and the side effects a change triggers (`Runner`: the `notify` hook and `auto_cleanup`). The two halves have different owners: `Detector` is shared, because every client renders its own toasts and detection must not be implemented twice, while `Runner` is constructed only by the daemon. Effect ownership is **asserted, not inferred** - see the "Key Conventions" bullet
- `internal/protocol/` - newline-delimited JSON over a unix socket, **bidirectional since phase 4**. The daemon writes `Snapshot`, clients write `Request`; direction alone disambiguates them, so there is no envelope. `Version` stays **1** because `Snapshot.Jobs` is additive - an old panel ignores the key, a new one sees nil against an old daemon. `RequestDecoder.Next` deliberately does not reject an unknown version: the daemon has to see such a request to answer with a refused job, and a drop at the decoder is indistinguishable from a daemon that never read
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
- Both the daemon and a self-polling client run one `Collector.Snapshot` per `tmux_interval` (default 1s); git work is gated inside it by the `git_interval` (default 3s) memo, and PR work per branch by the `pr_interval` (default 30s) check inside `prPoller.pass`, off the poll loop. The TUI has no separate tmux/git/PR tick cycles - `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd` and their messages and ticks are gone
- `Collector.Snapshot` is not reentrant: `gitMemo`, the last lock-free memo here, is owned by the calling goroutine (the PR store is mutex-guarded instead, because a worker writes it while `Snapshot` reads). `Collector`'s exported fields and `clock` are read-only once `New` returns, since `prPoller.pass` reads `Cmd`, `PRInterval` and `clock` from its own goroutine. `Model.startPoll` is the only issuer of `collectCmd` and refuses while `pollInFlight`, so at most one `Snapshot` is ever in flight per client. The self-poll chain is a self-rescheduling one-shot `CollectTickMsg`, created at exactly two sites (`Init`'s fallback branch and `handleDaemonLost`) and continued only in the tick handler
- **Transition side effects belong to the daemon and to nothing else. Ownership is asserted, not inferred.** `Model.checkStateTransitions` detects transitions and renders them - a toast per event, plus `maybeAutoFocus` - and runs no effects at all. Clients hold no `transition.EffectRunner`; `transition.Runner` is constructed only in `internal/daemon`. This replaced a timer (`spawnGrace` / `effectsDisownedUntil` / `daemonSeenSinceArm`) that tried to infer ownership from who owned the poll loop and could only *narrow* the window where two processes owned one event: `handleDaemonLost` started self-polling while the daemon it lost may still have been alive, `firstSnapshotTimeout` could loop a panel through connect/timeout/self-poll, and the per-process `inFlightEffects` could not help across two processes - one `Done` event then meant two `CleanupSession` calls against one worktree. None of that mechanism exists any more; do not reintroduce a client-side effect path
- The price of asserted ownership, and it is deliberate: **while no daemon is running, the `notify` hook does not fire and nothing is auto-cleaned.** A client self-polling for data is a data path, not an owner. This is why every mode now spawns a daemon when none answers and retries on every failed probe (`spawnDaemonOnce`, floored at `spawnCooldown` 15s per process). Toasts are unaffected - they are per-client, ungated, and fire on both the daemon-fed and self-polling paths
- `Done`-bound effects are serialized per session **in the daemon** (`daemon.Server.inFlightEffects`), because a merged session that gets a bell re-enters `Done` and would otherwise run two `CleanupSession` calls against one worktree. That hazard is now entirely within one process. Every other transition dispatches ungated. `auto_cleanup` failures go to the daemon log, not to a client
- `transition.Runner` refuses cleanup for a session that any client is attached to (`fetch.AttachedSessions`), and for a malformed event (empty `Session`, `PanePath` or `GitRoot`, logged). Both guards fail closed: if tmux cannot be reached, or the session is absent from tmux's list, cleanup is skipped
- The review-threads poll fetches only the unresolved count (`reviewThreadsQuery` has no `comments(` connection). Comment bodies are fetched on demand for the selected branch (`FetchReviewComments`) and cached by branch in `Model.reviewComments`; the cache is only cleared by `r`
- Detail panel: three modes (pane, PR description, review comments) with auto-select by state. Comments mode costs one `gh api graphql` call the first time a branch is viewed
- Session filtering by state, sorting by created/state/alpha, batch operations via multi-select
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold
- Draft toggle (`D`) with batch support
- Auto-cleanup merged sessions (configurable via `auto_cleanup` setting, off by default). Safe to enable with any number of panels open: only the daemon runs it. Verified on a real machine on 2026-07-29, before asserted ownership landed, under the weaker guarantee of the time: with two panels attached, four sessions reaching `Done` produced four hook invocations rather than eight; an unattached session's worktree was removed and a session with a client attached survived. That measurement is still valid but no longer the binding argument - the client has no code path that could produce the eight
- **`vigil dispatch` exits 0 on acceptance, not success.** The job outlives the CLI, which is the point. A refusal - duplicate input, unknown request version or type, empty input, full queue - is registered as a job in state `JobRefused`, never silently dropped, because the submitting client waits for its id to appear in a snapshot and a drop is indistinguishable from a daemon that never read the frame. `JobRefused` is distinct from `JobFailed` on purpose: refused means never accepted, failed means accepted, ran and lost. Conflating them made the CLI exit non-zero for work the daemon had actually started
- **`VIGIL_CLIENT` is how a daemon-run job learns which tmux client to act on.** The daemon has no tty, so it resolves the most recently active client per job and exports it into the hook's environment. The shell side uses it for three things: the switch target, the new window's size, and the panel orientation. It is an environment variable rather than a flag because the alternative threads a parameter through five levels, one of which re-quotes into a command string with `printf '%q'` and runs it through `bash -c`. Verified 2026-07-30 on an isolated server: with it, a session comes out 350x90 with a 40-column panel; genuinely headless, 80x24 with a panel at half the window, which is the ~175-column balloon's precondition
- **A hook's grandchildren can hold its output pipe.** `exec.CommandContext` kills only the direct child, so `cmd.Wait` blocks until every descendant closes the inherited fd. `ExecCommander.RunStream` therefore uses a process group, a `Cancel` that signals the group, and a `WaitDelay` backstop. Without it a hung dispatch was unbounded, blocked every later job, and left `Run` waiting on `pendingEffects` forever - a daemon that never exits, never releases its flock and never unlinks its socket, after which **no daemon can start again at all**. `ExecCommander.Run`, the non-streaming path used by `notify` and `cleanup`, still has this shape; shipped hook defaults do not background anything, which is the only reason it was left
- **The `dispatch` hook must be `DISPATCH_INLINE=1 dispatch --non-interactive {input}`.** `--detached` skips the teleport, and without `DISPATCH_INLINE` a client-less daemon tries `tmux display-popup`, which has nothing to draw on. `vigil` warns at startup when the configured hook still looks like the old one. Note also that **no hook body may contain `${VAR}`**: `ExpandHook` reads every `{...}` as a placeholder, so a braced shell expansion fails before reaching `sh`. Use `$VAR`
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
- **`fillGit` is the publication blocker now, and it is worse than "git is local so it is cheap" suggests.** Measured 2026-07-31 across 9 worktrees: `fillGit` ~3.0-3.5s per poll, 99.7% of `Snapshot`, and `git status --porcelain` is all of it - **0.7s to 1.6s on each active `portal` monorepo worktree**, 0.03s for every other git call. Because `fillGit` >= `git_interval` (3s), the git memo can never skip: the daemon runs `git status` continuously and the real publication cadence is ~3s, not the 1s `tmux_interval` the design assumes, so bell highlighting is up to 3s stale. Pre-existing, identical in `86e1fdc`, not a regression - but it means taking `gh` off the path **relocated** the "one slow thing blocks publication" shape from `gh` to `git` rather than removing it
- `session.PRPending` means the branch has no entry in the PR store at all, which is **not** the same as a branch known to have no PR. `transition.Detect` skips a session where `PRPending && PR == nil`: no seed, no event, not recorded in `next`. Without it, async fill turns every daemon start into a burst of `notify` hooks and an `auto_cleanup` run against every already-merged worktree - measured at 6 hooks in one instant with the skip removed, 0 with it. The `PR == nil` half is there because a client fills the last known PR from `prCache` first. It costs at most one `notify` hook per session, at daemon start
- `Collector.Invalidate` makes remote entries due by **zeroing `fetchedAt` rather than dropping them**. Dropping them would re-mark every branch pending, and a pending session is skipped by `Detect`, so a forced refresh would swallow the transition it was pressed to find. Git comes back inside the next `Snapshot`; remote data comes back a tick later. The git half is still goroutine-owned and unguarded, the remote half is safe from anywhere
- `Collector.RefreshRemote` runs one pass of every poller synchronously and exists **so tests do not race a goroutine**. Production reaches a pass only through `Start`, so a nudge that never reached a worker leaves every `RefreshRemote`-driven test green; only `TestRunStartsTheRemoteWorkers`, `TestNewStartsTheRemoteWorkers` and `TestADaemonFedClientSpendsNoGhBudget` catch that

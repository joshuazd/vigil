# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across sessions.

## In-flight design work

An approved 6-phase design is turning the session list into the primary surface, with
sessions expanded next to it. **Phases 0, 1, 2 and 3 are merged.** Phase 2's three blockers
landed as `31721d4`. Phase 3 landed on 2026-07-29 as `a785fb1` here and `fefeeb1` in
`~/dotfiles`; it spanned **two repositories** and neither half works without the other, so
a change to one usually needs the other. The branches were `phase-3-panel` in both.

**Phase 4 is next: `vigil dispatch <url-or-id>`, submitting a job to the daemon.** The
parent design says not to plan it until phase 3 has been lived on, and phase 3 has been
merged for as long as you have been reading this - so live on it first. Phase 3 is the
phase that makes N attached panels normal, which is the condition phase 4 inherits.

Read these before changing the daemon, `internal/collect`, `internal/transition`,
`internal/view`'s layout, or the launch path in `~/dotfiles`:

- `docs/superpowers/2026-07-29-phase-3-handoff.md` - current state, what phase 3 changed,
  the verification results and what they do NOT prove, the deferred list and the landmines.
  **Start here.**
- `docs/superpowers/specs/2026-07-29-phase-3-panel-by-default-design.md` - the phase 3
  design, reconciled with what shipped. Its "What this does not fix" section is the honest
  account of the effect-ownership race and is worth reading before touching
  `spawnDaemonOnce` or `handleDaemonLost`.
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
- `internal/collect/` - UI-independent session state collection (shared by the daemon and the TUI's self-polling fallback)
- `internal/transition/` - state-change detection (`Detector`) and the side effects a change triggers (`Runner`: the `notify` hook and `auto_cleanup`). Shared, because side effects belong to whoever owns the poll loop - the daemon when a client is connected to one, a self-polling client otherwise - and detection must not be implemented twice
- `internal/protocol/` - newline-delimited JSON snapshot protocol over a unix socket
- `internal/daemon/` - `vigil daemon`: runs one `Snapshot` per tick at `tmux_interval` (default 1s) so tmux metadata (including bell flags) is never more than a tick stale; git state is gated inside `Snapshot` on `git_interval` (default 3s) and PR state per branch on `pr_interval` (default 30s), each via its own memo. Startup serializes on an flock'd lock file beside the socket (`vigild.sock.lock`), held across the stale-socket removal and the bind, so racing daemons cannot both bind. Every client gets its own writer goroutine and a one-deep latest-wins queue, so a client that stops reading can neither stall the poll loop nor block new connections. `New` wires a `transition.Detector` and a `transition.Runner` (nil disables both, which is what a `Server` literal in a test gets); effects run in one goroutine per event because `poll` is synchronous per tick, and `Run` waits on `pendingEffects` before returning
- `vigil --panel` - the same `Model` with `panelMode` set: compact status bar, width-responsive table, no detail panel and no footer. A panel starts the daemon if none is running; the dashboard does not. Since phase 3 a panel is also created for every new tmux session, so this is the common way vigil runs, not the rare one

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
- Both the daemon and a self-polling client run one `Collector.Snapshot` per `tmux_interval` (default 1s); git and PR work is gated inside it by the `git_interval` (default 3s) and `pr_interval` (default 30s) memos. The TUI has no separate tmux/git/PR tick cycles - `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd` and their messages and ticks are gone
- `Collector.Snapshot` is not reentrant: its memos are owned by the calling goroutine. `Model.startPoll` is the only issuer of `collectCmd` and refuses while `pollInFlight`, so at most one `Snapshot` is ever in flight per client. The self-poll chain is a self-rescheduling one-shot `CollectTickMsg`, created at exactly two sites (`Init`'s fallback branch and `handleDaemonLost`) and continued only in the tick handler
- State-transition side effects run once per event, in whichever process owns the poll loop. Toasts and auto-focus stay per-client. `auto_cleanup` failures go to the daemon log, not to a client
- A panel that spawns a daemon stops owning effects for `spawnGrace` (5s), because `newModel` spawns and starts self-polling in the same breath while the reconnect probe only lands a probe interval later, and both would run the same event's effects. The rule is **arm on any successful spawn, provided the client has had a live daemon connection since the last arm** - not "arm once". The proviso is a `daemonSeenSinceArm` bool set only where a working connection is established (`newModel`'s dial, `handleProbeResult`'s live-conn branch) and cleared on every arm, so a daemon that spawns and never comes up arms exactly once and cannot chain suppressions. Arm-once was the original decision and missed daemon *restart*, where the two processes then run one `Done` event's `CleanupSession` twice against one worktree. Toasts are above the gate and unaffected
- The spawn grace **narrows** the two-owner window rather than closing it. It arms on spawn, but the hazardous state is "self-polling while a daemon is alive": `handleDaemonLost` starts self-polling and owning effects immediately and arms nothing, and only a *failed* probe reaches `spawnDaemonOnce`. A slow first snapshot (`firstSnapshotTimeout`, 5s, re-armed per reconnect) can loop a panel through connect/timeout/self-poll with an unsuppressed window each lap. `inFlightEffects` is per-process and cannot help across it. The real fix is asserted ownership, not a timer
- `Done`-bound effects are serialized per session on both paths (`inFlightEffects`), because a merged session that gets a bell re-enters `Done` and would otherwise run two `CleanupSession` calls against one worktree. Every other transition dispatches ungated
- `transition.Runner` refuses cleanup for a session that any client is attached to (`fetch.AttachedSessions`), and for a malformed event (empty `Session`, `PanePath` or `GitRoot`, logged). Both guards fail closed: if tmux cannot be reached, or the session is absent from tmux's list, cleanup is skipped
- The review-threads poll fetches only the unresolved count (`reviewThreadsQuery` has no `comments(` connection). Comment bodies are fetched on demand for the selected branch (`FetchReviewComments`) and cached by branch in `Model.reviewComments`; the cache is only cleared by `r`
- Detail panel: three modes (pane, PR description, review comments) with auto-select by state. Comments mode costs one `gh api graphql` call the first time a branch is viewed
- Session filtering by state, sorting by created/state/alpha, batch operations via multi-select
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold
- Draft toggle (`D`) with batch support
- Auto-cleanup merged sessions (configurable via `auto_cleanup` setting, off by default). Safe to enable with panels open, in the sense the handoff spells out, and verified on a real machine on 2026-07-29: with two panels attached, four sessions reaching `Done` produced four hook invocations rather than eight; an unattached session's worktree was removed and a session with a client attached survived
- Cache interop with previous Python version (same JSON format)
- The TUI dials the daemon socket on startup and consumes its broadcast snapshots when reachable; it falls back to self-polling if the daemon is never reached, does not send a first snapshot within a bounded wait, or is lost mid-session
- Both paths are permanently supported and must render identically (git/PR data, sort order, notifications). One known exception to "the `notify` hook fires once per real transition": a repeat `Done` event arriving while that session's cleanup is still in flight is skipped along with its hook, on both paths. Measured at 5 hook invocations for 7 transitions with the first effect blocked, identically on the daemon path and the model path (re-measured at HEAD `76d7779`, closing what was earlier an open gap between the two). Toasts are unaffected - they are per-client and ungated, 7 of 7. See the handoff for the full measurement
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

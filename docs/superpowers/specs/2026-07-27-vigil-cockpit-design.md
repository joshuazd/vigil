# Vigil Cockpit: session list as the primary surface

Date: 2026-07-27
Status: Approved, not yet planned

## Problem

The current workflow works but feels hacked together, and the reasons are specific.

**Dispatch reaches tmux by tunnelling through tmux.** The chain today is:

```
menu bar (dispatch.1d.sh or dispatch-bar)
  -> dispatch-from-chrome        # osascript scrapes the active Chrome tab
  -> finds "most recent tmux session"
  -> runs `dispatch` in a tmux popup inside it (DISPATCH_IN_POPUP=1)
  -> shortcut-implement | gh-review
  -> run_worktree_popup -> git-worktree-session -> git-worktree-new
  -> tmux send-keys the claude command into the :claude window
```

Four distinct problems in that chain:

1. **Input is a screen scrape.** The button dispatches whatever Chrome tab happens to be focused. No validation, no queue, no retry.
2. **Three layers of indirection to run a script.** Creating a tmux session needs a tmux context, so the script borrows an unrelated session, opens a popup in it, and bounces iTerm2 to the front afterward.
3. **`send-keys` is the Claude handoff.** `lib/tmux.sh:122` creates the session detached, then `shortcut-implement:208` and `gh-review:195` *type* the launch command into it. This races the shell's readiness and pushes a long prompt through shell quoting.
4. **Two menu bar implementations.** `dispatch.1d.sh` (SwiftBar plugin) and `dispatch-bar.swift` (compiled binary) both call `dispatch-from-chrome`.

**Observability is good but is not the default surface.** Vigil is bound as a popup (`.tmux.conf:31`, prefix+v) and also run standalone on a secondary monitor. It is an observer you visit, then it switches you away. The goal is for the session list to be the surface you live in, with individual sessions expanded from it.

Vigil itself is not the brittle part. It is clean, tested Go with good boundaries, and it already has a `dispatch` hook on `d`. That is the tell: vigil is already the better entry point, and the Chrome path exists in parallel to it rather than through it.

## Goals

- The session list is always on screen. Sessions are expanded next to it, not switched to in place of it.
- Dispatch has a durable place for work to land instead of a `send-keys` into a borrowed session.
- Work in progress is never broken. Every phase ships alone, reverts alone, and leaves the previous path working.

## Non-goals

- Rewriting the worktree/session bash in Go. The scripts work; rewriting them is pure risk for no gain.
- Replacing tmux. tmux remains the persistence layer and the terminal authority.
- Building a terminal emulator inside vigil (see Rejected alternatives).

## Architecture

Three components.

### `vigild`

The sole owner of polling: tmux session enumeration, git status, `gh` PR state, and (phase 5) the assigned-work queue. It publishes state snapshots to connected clients and accepts dispatch jobs.

Shipped as a subcommand of the existing binary (`vigil daemon`), not a second binary. One build, one install, one version. "vigild" is the name of the role in this document, not of an artifact.

One daemon means one `gh` rate-limit budget regardless of how many panels are on screen. This is what makes a panel-per-session affordable.

Dispatch jobs are executed by shelling out to the existing `shortcut-implement` / `gh-review` scripts, unchanged. `vigild` supplies the tmux context those scripts need, which is precisely what the popup tunnel was faking.

### `vigil`

The TUI, with two render modes over the same state:

- `full` - today's dashboard, for the standalone window and the popup.
- `--panel` - a compact strip, for the in-session panel.

The TUI prefers `vigild` for state. **If no daemon is reachable it self-polls exactly as it does today.** This fallback is what keeps the current setup working through every phase, and it is a permanent supported mode, not migration scaffolding.

### Session scripts (dotfiles `scripts/`)

Same responsibility as now: create the worktree and the tmux session. The change is who calls them (`vigild` rather than a popup chain) and how Claude is launched (initial command rather than `send-keys`).

### Transport

Unix socket, newline-delimited JSON, at `$XDG_RUNTIME_DIR/vigil/vigild.sock` falling back to `~/.local/state/vigil/vigild.sock`.

As built, `protocol.SocketPath` has a third fallback: `$TMPDIR/vigil/vigild.sock` (`os.TempDir()`), for the case where neither `XDG_RUNTIME_DIR` nor a home directory is available. Client and daemon resolve it identically, so they always agree.

A socket rather than watching a state file, for two reasons: dispatch needs request/response so failures can be reported, and the panel wants push so a terminal bell highlights immediately rather than on the next poll. One mechanism serves both.

The existing `internal/cache` JSON snapshot stays, as the cold-start view and as the data source for the self-polling fallback. Startup remains instant.

As built, the TUI loads that cache synchronously in `model.New`, for every mode and on both the daemon and self-polling paths, rather than as a startup command. First paint is therefore never blank, including while a just-started daemon has not completed a successful poll, and cached data never reaches `handleTmuxUpdated`, where it would re-merge over live sessions.

Clients connect and receive a full snapshot on connect, then a full snapshot on every poll cycle. `vigild` broadcasts to all connected clients.

As built, the connect-time snapshot exists only once the daemon has completed its first *successful* poll; before that a connecting client gets nothing until the next successful poll, and the client bounds that wait with a read deadline (5s) after which it falls back to self-polling.

As built, the daemon runs a single `Snapshot` per tick at `tmux_interval` (default 1s), matching the TUI's self-polling tmux cadence so bell highlighting is never more than a tick stale. Git state is gated inside `Snapshot` on `git_interval` (default 3s, keyed per pane path) and PR state per branch on `pr_interval` (default 30s), each via its own memo in `collect.Collector`, so the `gh` and git budgets match the TUI's self-polling rather than being 3x-30x it. A failed `gh` fetch reuses the last known PR for that branch instead of reporting no PR.

Snapshots carry shared state only. Which session is "current" and which is "last" are properties of a tmux *client*, not of the world, so each client resolves those itself on receiving a snapshot. `session.Session` already marks both fields `json:"-"`, so the type enforces this.

Full snapshots rather than deltas: a snapshot is a few dozen sessions of small structs, so delta encoding would add reconnection and ordering bugs to save bytes that do not matter. Revisit only if profiling shows otherwise.

## Panel geometry

**tmux decides placement; vigil renders to fit.** These are separate concerns and keeping them separate is what makes the responsive behavior simple.

The toggle binding measures the client and splits accordingly:

- Portrait (`client_height * 2 > client_width`, the vertical-monitor case): `split-window -vb -l 10`, a wide strip across the top.
- Otherwise: `split-window -hb -l 40`, a narrow column on the left.

A config setting overrides the automatic choice.

The panel reads its own pane dimensions and drops columns progressively as width shrinks. It reuses `internal/view/table.go`, which is already pure and therefore already testable at arbitrary geometries.

## Phasing

Each phase is independently shippable and independently revertible. Nothing is load-bearing until it is chosen.

### Phase 0: remove the send-keys race

Make Claude the pane's own process rather than text typed into a shell, and take the system prompt out of the command line.

Two changes:

1. Replace `tmux send-keys` (`shortcut-implement:208`, `gh-review:195`, `lib/tmux.sh:128`) with `tmux respawn-pane -k`, which replaces the pane's process directly. No shell readiness to race and no prompt text passing through a shell prompt. The respawned command appends `; exec "${SHELL}"` so exiting Claude leaves a usable shell instead of collapsing the pane and, with it, the window.

   `respawn-pane` rather than making it the session's initial command: both give the same property, but the initial-command form requires threading a `--claude-command` argument through `run_worktree_popup` -> `git-worktree-session` -> `create_tmux_session`, and the callers cannot build the command until after routing. Respawning keeps the existing call order and touches three lines instead of three scripts.

2. `claude_launch_cmd` (`lib/route.sh:500`) currently `printf '%q'`-quotes a multi-line system prompt into the command string. Instead write that prompt to a file and emit `--append-system-prompt "$(cat <file>)"`. The multi-line content never passes through tmux argument parsing.

   The prompt file lives in the worktree's private git directory (`git -C <worktree> rev-parse --absolute-git-dir`), as `vigil-launch-prompt.txt`. Not in the working tree, so it never appears in `git status` and cannot be committed by accident, and it is removed along with the worktree. Callers get the worktree path from `tmux display-message -p -t '=<session>:claude' '#{pane_current_path}'` rather than recomputing `${repo_root}/../${dir_name}`.

Removes a timing race and a quoting hazard. Independent of everything else in this spec and can land first.

This phase also introduces the first test harness for the scripts package, which has none today (`bats-core`, plus a `tmux` stub that records its argv so tmux interactions are assertable).

### Phase 1: `vigild`, invisible

Build the daemon, socket, and state protocol. The TUI prefers the daemon and self-polls without it. `main.go` gains real subcommand dispatch, replacing the ad-hoc `os.Args[1]` matching at `main.go:18`.

No visible change. Verified by comparing TUI output with the daemon running against the daemon stopped.

### Phase 2: panel mode, opt-in per session

Add `vigil --panel` and a tmux binding that toggles the panel into the current window using the geometry rule above.

Nothing auto-attaches. **Sessions already in progress are untouched until the key is pressed.** This is the phase to live on before going further.

### Phase 3: panel by default for new sessions

`create_tmux_session` in `lib/tmux.sh` adds the panel to new sessions, with a config flag to disable. Existing sessions keep working unpanelled and can be panelled with the toggle.

### Phase 4: dispatch through `vigild`

Add `vigil dispatch <url-or-id>`, which submits a job to the daemon. The daemon runs the existing scripts and streams status back to vigil's existing notification overlay.

The menu bar calls `vigil dispatch` instead of the popup tunnel. The standalone `dispatch` CLI keeps working unchanged for direct terminal use.

### Phase 5: work queue

`vigild` also polls assigned Shortcut stories and review-requested PRs. Both vigil and the menu bar present a pickable list; selecting an item dispatches it. Chrome-tab dispatch remains as the escape hatch for anything not in the queue, such as an unassigned PR or an arbitrary URL.

The orphaned `gh-review-poll` script was an earlier attempt at this and is superseded by it.

### Phase 6: deletions

Only after living on the above:

- `tmux-monitor` (orphaned, superseded by vigil)
- `gh-review-poll` (orphaned, superseded by phase 5)
- `worktree-status` and its `.tmux.conf` prefix+w / prefix+C-w bindings (superseded by the panel)
- One of the two menu bar implementations
- The popup tunnel inside `dispatch-from-chrome`

## Failure handling

| Failure | Behavior |
|---|---|
| `vigild` not running | TUI and panel self-poll, exactly as today. Panel shows a daemon-down indicator. |
| Socket stale or missing | The client self-polls. A stale socket file left by a killed daemon is removed by the next daemon start. Two daemons racing to bind the same fresh socket is still open: the loser exits with `ErrAlreadyRunning`, but there is no lock file, so a **phase 2** blocker. |
| No daemon running when a panel starts | Phase 2 onward: `vigil` spawns the daemon on demand, since N self-polling panels would multiply the `gh` budget. In phase 1 the TUI simply self-polls, which keeps that phase invisible. |
| `vigild` dies with panels open | Panels show last-known state marked stale, then reconnect when it returns. **Phase 2**: as built in phase 1, a client that loses the daemon falls back to self-polling permanently and shows no staleness marker; it never reconnects. Self-polling is a supported mode, so the data stays correct, but stale-marking and reconnection are phase 2 work. |
| Dispatch job fails | Structured error over the socket, surfaced in vigil's existing notification overlay. |
| Panel pane process dies | `remain-on-exit off` so the pane closes cleanly rather than leaving a dead pane. |
| Session's panel and a full vigil both open | Both are read-only views of the same daemon state. No conflict. |

## Testing

The codebase already has the two seams this needs: `fetch.Commander` for stubbing subprocesses, and a pure view layer.

- **Protocol**: unit tests for snapshot and delta encoding, connect/reconnect, and multi-client broadcast.
- **Panel rendering**: golden renders at several widths and heights, covering the column-dropping thresholds and both orientations.
- **Geometry rule**: unit test the portrait/landscape decision at boundary dimensions.
- **Dispatch**: daemon-side job runner tested against a stub script, covering success, non-zero exit, and timeout.
- **Fallback**: explicit test that the TUI produces correct state with no daemon reachable.
- **Per-phase gate**: each phase adds a check that the path it replaces still works.

## Rejected alternatives

**Embedded VT: vigil becomes a terminal client.** A new `internal/term` package using a PTY plus a VT emulator, running `tmux attach` inside it and forwarding keys. This is the most literal reading of "sessions embedded inside vigil" and gives full Claude TUI fidelity. Rejected because it carries two problems that never go away: nested tmux prefix collision (vigil must filter `C-Space` before the inner client sees it), and client size clamping (the same session attached both in a full-screen iTerm window and in vigil's narrow pane gets clamped to the smaller, needing `window-size latest` and still behaving oddly). It also means owning a terminal emulator.

Inverting the containment gets the same picture on screen with tmux doing the terminal work.

**`join-pane` loan.** Have vigil physically move the target session's pane into a cockpit window. No emulation and no nesting, but it mutates the source session's layout, the pane is "on loan," and a vigil crash mid-loan strands the pane in the cockpit session. Trades the current brittleness for a new kind.

**Sessions as windows in one tmux session.** Collapse everything into a single session with the list as window 0. Native navigation throughout, but loses per-session isolation and vigil's session-level model.

**File-watching instead of a socket.** Simpler and reuses `internal/cache`, but gives no request/response channel for dispatch errors and adds poll latency to bell highlighting. Would require a second mechanism for dispatch anyway.

**Dispatch from vigil only, dropping the menu bar.** Fewest moving parts, but loses the ability to dispatch without a terminal focused, which is the part of the current workflow that works well.

## Carried forward from phases 0-1

Recorded here because phase 2 planning starts from this document, and because these
were found during implementation rather than during design.

### Must be resolved before phase 2 ships

- **RESOLVED (task 1).** ~~**The daemon has no start-time mutual exclusion.** With a
  stale socket file present, two daemons can both unlink and both bind, leaving two
  live daemons with the first orphaned yet still polling and still writing the shared
  cache. Mapping `EADDRINUSE` to `ErrAlreadyRunning` covers the friendly-message case
  only. This is dormant today because nothing autostarts the daemon, and it becomes
  live the moment phase 2 has `vigil` spawn the daemon on demand from N panels - that
  is precisely the race, run concurrently. The fix is an flock'd lock file.~~ Startup
  now serializes on an flock'd lock file held across the stale-socket removal and the
  bind.
- **RESOLVED (task 2).** ~~**One wedged client stalls the poll loop.** The daemon
  sends to clients sequentially with a 5 second write deadline, and the connect-time
  send runs on the accept goroutine, so a client that connects and never reads also
  blocks new connections. Correct at one or two clients; a panel per session changes
  that. A single-writer design (accept hands new connections to the `Run` loop over a
  channel) removes both symptoms and the double-send window below.~~ Every client now
  gets its own writer goroutine and a one-deep latest-wins queue, and accept hands
  connections to `Run` over a channel, so a stalled client can neither block the poll
  loop nor block new connections.
- **RESOLVED (tasks 3, 4).** ~~**No reconnect, and no staleness signal for a
  live-but-silent daemon.** The client's first-snapshot read deadline is cleared once
  the first snapshot arrives, so a daemon that is alive but not broadcasting freezes
  the TUI on stale data indefinitely with no indicator. Fallback is one-way and
  permanent. Acceptable while self-polling is a supported mode and there is one
  client; not acceptable with a panel per session.~~ A self-polling client now probes
  the socket every 2s and reconnects when the daemon returns; the reconnect is
  epoch-guarded so a stale probe from a retired generation cannot install itself. A
  connected but silent daemon now shows `daemon stale Ns` in the status bar after
  three poll intervals.

### Landmine to check before phase 3

`launch_claude_in_pane` targets the pane positionally as `:claude.1`, while this
document's panel geometry uses `split-window -vb` / `-hb`, which inserts the new pane
*before* the existing one. tmux pane indexes are positional, so adding a panel likely
renumbers the Claude pane to `.2` and phase 0's respawn would then target the panel.
Prefer a `pane_id` or a pane title over a positional target.

### Worth knowing

- `internal/collect` and the TUI's self-polling in `model.go` now implement the same
  job twice and have already drifted once (the extraction silently dropped branch
  deduplication). Collapsing the fallback onto `collect` is the durable fix.
- Two dependencies are correct today but unrecorded in code: concurrent `send` on one
  connection is frame-safe only because `protocol.Encode` performs exactly one `Write`
  and Go takes a per-fd write lock, and `internal/fetch`'s `MockCommander` is only
  concurrency-safe for its call log, not its handler maps.
- A permanently failing `gh` now shows the last known PR indefinitely on both paths,
  with no staleness marker. That is the deliberate trade behind memoizing PR state.

### Still open after phase 2

- Collapsing the TUI's self-polling onto `internal/collect` (still duplicated, still
  able to drift).
- Lazy review-thread fetching (the daemon still spends two GraphQL calls per PR per
  cycle).
- The daemon-up versus daemon-down TUI comparison was never run as a timing
  observation.
- A permanently failing `gh` still shows the last known PR indefinitely with no
  staleness marker. The new marker covers a silent *daemon*, not a silent `gh`.
- `internal/view`'s tests prove less than they appear to about styling. Under `go
  test` there is no tty and no forced color profile, so lipgloss emits zero ANSI
  bytes and every "styled" cell in those tests is a plain string. The synthetic-string
  test for escape-aware truncation is the only coverage of that behavior. This is a
  real bound on what the view suite verifies and should not be discovered a third
  time.
- State-transition side effects run once per attached client, not once per event.
  `checkStateTransitions` lives in the model, and every client keeps its own
  `prevStates`, so a session going Blocked fires the configured `notify` hook once
  per panel, and with `auto_cleanup = true` it runs `action.CleanupSession` - `git
  worktree remove` plus `tmux kill-session` - once per panel, concurrently, against
  the same worktree. Per-client *toasts* are correct and should stay per-client:
  each panel has its own screen. Per-client hooks and cleanups are not, and phase 2
  is exactly what makes N clients normal rather than exotic. Latent today only
  because `auto_cleanup` defaults to false and no `notify` hook is configured; do
  not enable `auto_cleanup` while panels are open. The durable fix is moving the
  side effects to the daemon, which owns one view of state and can fire each
  transition once, leaving the clients to render. Phase 3 work.
- A live panel resized across a tier boundary is unobserved. Three panes at fixed
  widths were verified rendering real sessions, but not one pane resized through the
  boundaries. `tea.WindowSizeMsg` handling is unchanged pre-existing code, so the risk
  is low, but that is not the same as having checked it.

### Process note

Five tests written into the phases 0-1 plan would have passed with their subject
deleted; the implementations were mostly correct while the verification was not. Plan-
authored tests need an explicit "would this fail if the code it names were removed?"
pass before dispatch. Two related traps recurred: `net.UnixListener.Close()` unlinks
its socket by default, so a test that closes a listener proves nothing about explicit
removal; and a test fixture that stands in for the script under test hides ordering
bugs in that script.

## Plan decomposition

This spec is deliberately larger than one implementation plan. The phases are the decomposition: each gets its own plan and its own implementation cycle, written when the previous phase has been lived on.

The first plan covers **phases 0 and 1 only**. They pair well: phase 0 is a self-contained fix to the launch path, and phase 1 is invisible by construction, so together they produce a verifiable no-visible-change milestone with the daemon in place. Phase 2 is where behavior changes and deserves its own plan and its own settling period.

Do not plan phases 3 through 6 in advance. What phase 2 teaches about living with the panel should inform them.

## Repository split

- **`~/vigil`**: `vigild`, the socket protocol, panel render mode, subcommand dispatch in `main.go`, `vigil dispatch`, the work queue.
- **`~/dotfiles/scripts/scripts`**: phase 0 launch fix in `lib/tmux.sh`, panel integration in `create_tmux_session`, menu bar pointing at `vigil dispatch`, phase 6 deletions.
- **`~/dotfiles/tmux/.tmux.conf`**: panel toggle binding, phase 6 binding removals.

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

A socket rather than watching a state file, for two reasons: dispatch needs request/response so failures can be reported, and the panel wants push so a terminal bell highlights immediately rather than on the next poll. One mechanism serves both.

The existing `internal/cache` JSON snapshot stays, as the cold-start view and as the data source for the self-polling fallback. Startup remains instant.

Clients connect and receive a full snapshot, then deltas. `vigild` broadcasts to all connected clients.

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

Make the Claude invocation the tmux session's initial command in `lib/tmux.sh:122` rather than text typed in later at `shortcut-implement:208` and `gh-review:195`. Pass the prompt via a file in the worktree rather than through a shell command line.

Removes a timing race and a quoting hazard. Independent of everything else in this spec and can land first.

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
| Socket stale or missing | Clients retry with backoff. `vigil` spawns the daemon on demand. |
| `vigild` dies with panels open | Panels show last-known state marked stale, then reconnect when it returns. |
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

## Plan decomposition

This spec is deliberately larger than one implementation plan. The phases are the decomposition: each gets its own plan and its own implementation cycle, written when the previous phase has been lived on.

The first plan covers **phases 0 and 1 only**. They pair well: phase 0 is a self-contained fix to the launch path, and phase 1 is invisible by construction, so together they produce a verifiable no-visible-change milestone with the daemon in place. Phase 2 is where behavior changes and deserves its own plan and its own settling period.

Do not plan phases 3 through 6 in advance. What phase 2 teaches about living with the panel should inform them.

## Repository split

- **`~/vigil`**: `vigild`, the socket protocol, panel render mode, subcommand dispatch in `main.go`, `vigil dispatch`, the work queue.
- **`~/dotfiles/scripts/scripts`**: phase 0 launch fix in `lib/tmux.sh`, panel integration in `create_tmux_session`, menu bar pointing at `vigil dispatch`, phase 6 deletions.
- **`~/dotfiles/tmux/.tmux.conf`**: panel toggle binding, phase 6 binding removals.

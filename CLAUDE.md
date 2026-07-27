# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across sessions.

## Architecture

Go + Bubble Tea TUI. Single static binary.

- `main.go` — Entry point, dependency checks, tea.NewProgram
- `internal/model/model.go` — Bubble Tea Model/Update/View, polling, state management
- `internal/model/keys.go` — Keybindings
- `internal/model/messages.go` — All tea.Msg types
- `internal/session/` — Session, GitStatus, PRStatus structs, SessionState enum, sorting
- `internal/view/` — Rendering: table, detail panel, status bar, styles, cell formatters
- `internal/fetch/` — Subprocess wrappers: tmux, git, gh CLI, Commander interface
- `internal/action/` — Merge, approve, cleanup, rebase, draft, dispatch actions
- `internal/config/` — TOML config loading, hook template expansion and execution
- `internal/cache/` — JSON session cache for instant startup
- `internal/collect/` - UI-independent session state collection (shared by the daemon and the TUI's self-polling fallback)
- `internal/protocol/` - newline-delimited JSON snapshot protocol over a unix socket
- `internal/daemon/` - `vigil daemon`: runs one `Snapshot` per tick at `tmux_interval` (default 1s) so tmux metadata (including bell flags) is never more than a tick stale; git state is gated inside `Snapshot` on `git_interval` (default 3s) and PR state per branch on `pr_interval` (default 30s), each via its own memo. It broadcasts each snapshot to every connected client. A client that connects gets the latest snapshot immediately, but only once the daemon has completed a first successful poll; before that it gets nothing until the next successful poll, and falls back to self-polling if that takes longer than 5s

## Testing

```bash
make test    # go test ./...
make lint    # golangci-lint
```

## Build & Install

```bash
make build     # compile binary
make install   # copy to ~/.local/bin/vigil
```

## Key Conventions

- ANSI colors (adapts to terminal theme, no hardcoded hex)
- No global mutable state — config/caches passed explicitly
- Commander interface for subprocess calls (testable)
- View is pure — pane capture in Update via tea.Cmd, not in View
- context.Context for cancellation
- Background polling: tmux every 1s, git every 3s, PR every 30s (parallel fetches)
- Detail panel: three modes (pane, PR description, review comments) with auto-select by state
- Session filtering by state, sorting by created/state/alpha, batch operations via multi-select
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold
- Draft toggle (`D`) with batch support
- Auto-cleanup merged sessions (configurable via `auto_cleanup` setting, off by default)
- Cache interop with previous Python version (same JSON format)
- The TUI dials the daemon socket on startup and consumes its broadcast snapshots when reachable; it falls back to self-polling if the daemon is never reached, does not send a first snapshot within a bounded wait, or is lost mid-session
- Both paths are permanently supported and must render identically (git/PR data, sort order, notifications)
- `model.New` loads the session cache synchronously for every mode, so first paint is never blank on either path; nothing about the cache is emitted as a message
- A session missing PR data falls back to the last known PR for its branch (`prCache` client-side, a per-branch memo in `collect.Collector` daemon-side), so one failed `gh` call cannot blank the PR column or fire a spurious idle transition

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
- Background polling: git every 3s, PR every 30s (parallel fetches)
- Detail panel: three modes (pane, PR description, review comments) with auto-select by state
- Session filtering by state, sorting by created/state/alpha, batch operations via multi-select
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold
- Draft toggle (`D`) with batch support
- Auto-cleanup merged sessions (configurable via `auto_cleanup` setting, off by default)
- Cache interop with previous Python version (same JSON format)

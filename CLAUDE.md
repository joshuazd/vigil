# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across sessions.

## Architecture

Python + Textual TUI. Replaces the bash-based `tmux-monitor`.

- `app.py` — Textual app, compose, keybindings, worker orchestration, state tracking
- `widgets.py` — SessionTable, DetailPanel (with modes), StatusBar, DispatchInput
- `models.py` — Session, GitStatus, PRStatus, SessionState enum
- `tmux.py` — Subprocess wrappers for tmux commands
- `git_status.py` — Git status parsing (porcelain format, unpushed count)
- `pr_status.py` — GitHub PR status via `gh` CLI and GraphQL (includes review thread content)
- `config.py` — TOML config loading, hook template expansion and execution
- `actions.py` — Merge, approve, cleanup, dispatch actions (via configurable hooks)

## Testing

```bash
make test    # pytest
make lint    # ruff
```

## Installation

Bash wrapper at `./vigil` (project root) auto-bootstraps a venv at `~/.local/share/vigil/venv`.

## Key Conventions

- Theme is `nord`
- Command palette is disabled (`COMMANDS = set()`)
- All instance vars initialized in `__init__`, not class-level mutable defaults
- Subprocess errors raised, not swallowed
- Background polling: git every 3s, PR every 30s
- Detail panel has three modes (pane, PR description, review comments) via `DetailMode` enum
- Session filtering by state, batch operations via multi-select
- State transition notifications with configurable hooks
- Stale branch warnings when rebase age exceeds threshold

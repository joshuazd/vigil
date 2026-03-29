# vigil

TUI mission control for tmux worktree sessions. Monitors session status, git changes, and PR state.

## Architecture

Python + Textual TUI. Replaces the bash-based `tmux-monitor`.

- `app.py` — Textual app, compose, keybindings, worker orchestration
- `widgets.py` — SessionList, SessionRow, StatusBar, DetailPanel
- `models.py` — Session, GitStatus, PRStatus, SessionState enum
- `tmux.py` — Subprocess wrappers for tmux commands
- `git_status.py` — Git status parsing (porcelain format, unpushed count)
- `pr_status.py` — GitHub PR status via `gh` CLI and GraphQL
- `actions.py` — Merge, approve, cleanup, dispatch actions

## Testing

```bash
# Install dev deps
pip install -e ".[dev]"

# Run full test suite
pytest tests/ -v

# Lint
ruff check src/ tests/
```

## Installation

Bash wrapper at `./vigil` (project root) auto-bootstraps a venv at `~/.local/share/vigil/venv`.

## Key Conventions

- Theme is `nord`
- Command palette is disabled (`COMMANDS = set()`)
- All instance vars initialized in `__init__`, not class-level mutable defaults
- Subprocess errors raised, not swallowed
- Background polling: git every 3s, PR every 30s

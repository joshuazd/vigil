# vigil

TUI mission control for tmux worktree sessions. Monitors session status, git changes, and PR state from a single dashboard.

## Prerequisites

- Python 3.10+
- [tmux](https://github.com/tmux/tmux)
- [git](https://git-scm.com/)
- [gh](https://cli.github.com/) (GitHub CLI, authenticated)

## Installation

```bash
./vigil
```

The bootstrap script auto-creates a venv at `~/.local/share/vigil/venv` and installs dependencies. Re-run after pulling updates.

## Keybindings

| Key | Action |
|-----|--------|
| `q` | Quit |
| `j` / `k` | Navigate down / up |
| `Enter` | Switch to session (popup mode) or toggle detail |
| `Tab` | Toggle detail panel |
| `o` | Open PR in browser |
| `m` | Merge PR (press twice to confirm) |
| `a` | Approve PR |
| `x` | Cleanup session (press twice to confirm) |
| `d` | Dispatch (Shortcut URL or PR number) |
| `b` | Rebase and force-push |
| `r` | Refresh |
| `Esc` | Cancel |

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `VIGIL_GIT_INTERVAL` | `3` | Git polling interval (seconds) |
| `VIGIL_PR_INTERVAL` | `30` | PR polling interval (seconds) |
| `VIGIL_CACHE_TTL` | `30` | Cache staleness threshold (seconds) |
| `VIGIL_LOG_LEVEL` | `INFO` | Log level (DEBUG, INFO, WARNING, ERROR) |
| `VIGIL_GIT_WORKERS` | `8` | Max parallel git status threads |

Logs are written to `~/.local/share/vigil/vigil.log` (rotating, 2 MB max).

## Development

```bash
pip install -e ".[dev]"
pytest tests/ -v
ruff check src/ tests/
mypy src/
```

## Architecture

See [CLAUDE.md](CLAUDE.md) for module-level documentation.

## License

[GPL-3.0](LICENSE)

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

### Config file

`~/.config/vigil/config.toml` — optional TOML config for hooks and future settings.

```toml
[settings]
git_interval = 3       # Git polling interval (seconds)
pr_interval = 30       # PR polling interval (seconds)
cache_ttl = 30         # Cache staleness threshold (seconds)
log_level = "INFO"     # DEBUG, INFO, WARNING, ERROR
git_workers = 8        # Max parallel git status threads

[hooks]
cleanup = "git-worktree-cleanup --session {session} {path}"
dispatch = "dispatch --detached --non-interactive {input}"
merge = "gh pr merge {branch} --squash --delete-branch"
approve = "gh pr review {branch} --approve"
```

### Hooks

Actions can be customized via shell command templates with `{placeholder}` variables:

| Hook | Variables | Default |
|------|-----------|---------|
| `cleanup` | `{session}`, `{path}`, `{branch}`, `{git_root}` | Built-in: kill tmux session + remove worktree |
| `dispatch` | `{input}` | *(none — must be configured)* |
| `merge` | `{branch}`, `{git_root}` | `gh pr merge {branch} --squash --delete-branch` |
| `approve` | `{branch}`, `{git_root}` | `gh pr review {branch} --approve` |

Variables are automatically shell-escaped. Set a hook to `""` to disable it.

### Environment variable overrides

Environment variables override TOML settings for quick testing or per-machine config:

`VIGIL_GIT_INTERVAL`, `VIGIL_PR_INTERVAL`, `VIGIL_CACHE_TTL`, `VIGIL_LOG_LEVEL`, `VIGIL_GIT_WORKERS`

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

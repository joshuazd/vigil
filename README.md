# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across all your sessions from a single view.

Navigate sessions with vim-style keybindings, merge PRs, approve reviews, rebase branches, and clean up sessions — all without leaving the terminal. Works with any tmux workflow: git worktrees, one-branch-per-session, or plain repos.

## Install

```bash
brew install joshuazd/tap/vigil
```

Or via PyPI:

```bash
pipx install vigil-tui
```

Or from source:

```bash
git clone https://github.com/joshuazd/vigil.git
pipx install ./vigil
```

## Prerequisites

- [tmux](https://github.com/tmux/tmux)
- [git](https://git-scm.com/)
- [gh](https://cli.github.com/) (GitHub CLI, authenticated)

## Usage

```bash
# Launch the TUI
vigil

# Show help
vigil --help
```

Vigil discovers all tmux sessions, reads git status from each session's working directory, and fetches PR state via `gh`. Sessions are color-coded by state: idle, pending review, CI failing, mergeable, etc.

## Keybindings

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate down / up (wraps around) |
| `Enter` | Switch to session (popup mode) or toggle detail |
| `Tab` | Toggle detail panel |
| `p` | Cycle detail mode (pane / PR description / review comments) |
| `f` / `F` | Cycle session filter by state (forward / backward) |
| `s` / `S` | Cycle sort mode: created / state / alpha (forward / backward) |
| `D` | Toggle PR draft status |
| `Space` | Toggle multi-select for batch operations |
| `o` | Open PR in browser |
| `m` | Merge PR (press twice to confirm) |
| `a` | Approve PR |
| `b` | Rebase and force-push |
| `x` | Cleanup session (press twice to confirm) |
| `d` | Dispatch (run configured hook with input) |
| `r` | Refresh |
| `Escape` | Clear selection / cancel |
| `q` | Quit |

With multi-select active, `m`, `a`, `x`, `b`, and `D` operate on all selected sessions as a batch.

## Configuration

All configuration is optional. Create `~/.config/vigil/config.toml` to customize:

```toml
[settings]
git_interval = 3              # Git polling interval (seconds)
pr_interval = 30              # PR polling interval (seconds)
cache_ttl = 30                # Cache staleness threshold (seconds)
log_level = "INFO"            # DEBUG, INFO, WARNING, ERROR
git_workers = 8               # Max parallel git status threads
capture_window = ""           # Window name for detail panel (empty = first window)
stale_threshold = 86400       # Rebase age warning threshold (seconds, default 24h)
notifications_enabled = true  # Toast + hook on session state changes
auto_cleanup = false          # Auto-cleanup sessions when PR merges

[hooks]
cleanup = "tmux kill-session -t {session} && git worktree remove {path}"
dispatch = "my-dispatch-script {input}"
merge = "gh pr merge {branch} --squash --delete-branch"
approve = "gh pr review {branch} --approve"
```

### Hooks

Actions are shell command templates with `{placeholder}` variables, automatically shell-escaped:

| Hook | Variables | Default |
|------|-----------|---------|
| `cleanup` | `{session}`, `{path}`, `{branch}`, `{git_root}` | Built-in (see below) |
| `dispatch` | `{input}` | *(none — must be configured)* |
| `merge` | `{branch}`, `{git_root}` | `gh pr merge {branch} --squash --delete-branch` |
| `approve` | `{branch}`, `{git_root}` | `gh pr review {branch} --approve` |
| `notify` | `{session}`, `{old_state}`, `{new_state}` | `tmux display-message "vigil: {session} → {new_state}"` |

The built-in cleanup kills the tmux session, then removes the git worktree if the session directory is one. For non-worktree sessions, it just kills the session. Override with a hook for custom behavior.

The default merge uses `--squash --delete-branch`. Override `[hooks] merge` for a different strategy. Set any hook to `""` to disable it.

### Environment variable overrides

Environment variables override TOML settings for quick testing:

`VIGIL_GIT_INTERVAL`, `VIGIL_PR_INTERVAL`, `VIGIL_CACHE_TTL`, `VIGIL_LOG_LEVEL`, `VIGIL_GIT_WORKERS`, `VIGIL_CAPTURE_WINDOW`, `VIGIL_STALE_THRESHOLD`, `VIGIL_NOTIFICATIONS`, `VIGIL_AUTO_CLEANUP`

## Development

```bash
git clone https://github.com/joshuazd/vigil.git
cd vigil
make install   # bootstrap venv
make test      # run tests
make lint      # run linter
make release   # tag, publish to PyPI, update Homebrew tap
```

The `./vigil` bootstrap script auto-creates a virtualenv at `~/.local/share/vigil/venv` for quick local use without a manual install.

Logs are written to `~/.local/share/vigil/vigil.log` (rotating, 2 MB max).

## License

GPL-3.0 — see [LICENSE](LICENSE) for details.

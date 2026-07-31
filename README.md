# vigil

TUI dashboard for tmux sessions. Monitors git status and GitHub PR state across all your sessions from a single view.

Navigate sessions with vim-style keybindings, merge PRs, approve reviews, rebase branches, and clean up sessions — all without leaving the terminal. Works with any tmux workflow: git worktrees, one-branch-per-session, or plain repos.

## Install

```bash
brew install joshuazd/tap/vigil
```

Or download a binary from [GitHub Releases](https://github.com/joshuazd/vigil/releases).

Or from source:

```bash
git clone https://github.com/joshuazd/vigil.git
cd vigil
make install
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

# Run the shared state daemon (optional: polls tmux/git/PR state on an
# interval and broadcasts it to every connected vigil client)
vigil daemon
```

Vigil discovers all tmux sessions, reads git status from each session's working directory, and fetches PR state via `gh`. Sessions are color-coded by state: idle, pending review, CI failing, mergeable, etc.

If `vigil daemon` is running, `vigil` consumes its broadcast snapshots instead of polling on its own. If the daemon isn't running, is unreachable, or doesn't send a snapshot within a few seconds of connecting, `vigil` falls back to polling tmux/git/PR state itself - both modes render identically.

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
| `Escape` | Clear selection / cancel, then dismiss a failed or refused dispatch job, then quit |
| `q` | Quit |

With multi-select active, `m`, `a`, `x`, `b`, and `D` operate on all selected sessions as a batch.

## Configuration

All configuration is optional. Create `~/.config/vigil/config.toml` to customize:

```toml
[settings]
tmux_interval = 1             # Tmux polling interval (seconds)
git_interval = 3              # Git polling interval (seconds)
pr_interval = 30              # PR polling interval (seconds)
cache_ttl = 30                # Cache staleness threshold (seconds)
git_workers = 8               # Max parallel git status fetches
capture_window = ""           # Window name for detail panel (empty = first window)
stale_threshold = 86400       # Rebase age warning threshold (seconds, default 24h)
notifications_enabled = true  # Toast + hook on session state changes
auto_cleanup = false          # Auto-cleanup sessions when PR merges
dispatch_timeout = 300        # Seconds before a running dispatch job is killed

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

Hook bodies must not contain `${VAR}`. A braced shell expansion collides with the `{placeholder}` syntax: `${VAR}` is read as the placeholder `{VAR}`, which is not a known variable, and the hook fails with `unknown placeholder in hook template` before `sh` ever sees it. Use `$VAR` instead. This applies to every hook, not just `dispatch`.

### Dispatch

`d` in the TUI, or `vigil dispatch <url-or-id>` from a shell, submits a job to `vigild`, which runs the `dispatch` hook and streams its output into a job line every panel shows. `vigil dispatch` starts a daemon if none is running, and exits as soon as the daemon acknowledges the job: exit 0 means accepted, not finished. The job outlives the submitting process, which is the point - the shell that submitted it is usually about to be replaced by the session the job creates.

The hook runs **inside the daemon**, which has no terminal:

```toml
[settings]
dispatch_timeout = 300        # Seconds before a running dispatch is killed

[hooks]
dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {input}"
```

What that means for the hook you write:

- **No popup, no tty.** A hook that opens `tmux display-popup -E` has no client to draw on and will hang until `dispatch_timeout` kills it. Run the work inline instead. If you use the `dispatch` script from `~/dotfiles`, `DISPATCH_INLINE=1` is what selects that branch, and the older `DISPATCH_IN_POPUP` is gone.
- **Do not pass `--detached`.** The teleport at the end of a dispatch is the feature; `--detached` skips it, leaving the new session created but unswitched.
- **`VIGIL_CLIENT` is exported into the hook.** It names the most recently active tmux client, resolved per job rather than per submission, and is how a client-less daemon can still pick a switch target, a window size, and a panel orientation. It is empty when no client is attached, and a hook must treat that as "nobody is watching" rather than an error.
- **`dispatch_timeout` (default 300s, `VIGIL_DISPATCH_TIMEOUT`) bounds the job.** On expiry the hook's whole process group is killed, backgrounded grandchildren included, and the job reports the timeout rather than its last output line.
- **Jobs run one at a time.** Two concurrent `git worktree add` calls in one repository contend on the index lock, so submissions queue; a duplicate of an in-flight input is refused rather than queued.

If your `dispatch` hook still passes `--detached` or still names `DISPATCH_IN_POPUP`, vigil prints a warning at startup naming this section.

### Environment variable overrides

Environment variables override TOML settings for quick testing:

`VIGIL_TMUX_INTERVAL`, `VIGIL_GIT_INTERVAL`, `VIGIL_PR_INTERVAL`, `VIGIL_CACHE_TTL`, `VIGIL_LOG_LEVEL`, `VIGIL_GIT_WORKERS`, `VIGIL_CAPTURE_WINDOW`, `VIGIL_STALE_THRESHOLD`, `VIGIL_NOTIFICATIONS`, `VIGIL_AUTO_CLEANUP`, `VIGIL_AUTO_FOCUS`, `VIGIL_PANEL_AUTO`, `VIGIL_DISPATCH_TIMEOUT`

## Development

```bash
git clone https://github.com/joshuazd/vigil.git
cd vigil
make build     # compile binary
make test      # run tests
make lint      # run linter
make install   # install to ~/.local/bin
make release   # tag, create GitHub release, GoReleaser builds + publishes
```

## License

GPL-3.0 — see [LICENSE](LICENSE) for details.

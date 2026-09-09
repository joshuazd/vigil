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

### A permanent panel outside tmux

`vigil --panel` does not have to run inside tmux. In an iTerm2 split pane above a tmux pane it becomes one panel that survives every session switch, rather than one panel per session.

Set it up once, by hand. The pane must be **created** with the `Vigil Panel` profile - `Cmd+Shift+D` splits with the *current* profile, and applying the right profile afterwards only changes appearance, leaving a correctly-profiled pane running a bare shell:

```bash
osascript -e 'tell application "iTerm2" to tell current session of current tab of current window to split horizontally with profile "Vigil Panel"'
```

Run that from the tmux pane, drag the new pane above it, then `Window > Save Window Arrangement` and `Settings > General > Startup > "Open default window arrangement"`. There is no script, deliberately - iTerm2's AppleScript `split horizontally` always adds the new pane *below* the one it splits, so a scripted split can only put vigil underneath; only the Python API can place a pane above, and an arrangement is what persists the layout anyway.

The `Vigil Panel` profile lives in `~/dotfiles` (a separate repository) as an iTerm2 Dynamic Profile. It matters because an arrangement restores a pane's *profile*, not a command typed at a prompt: a pane where you typed `vigil --panel` comes back as a bare shell, while one opened with that profile comes back running vigil. Its command is `/bin/zsh -c 'vigil --panel; exec /bin/zsh -l'`, and the `zsh -c` wrapper is required - a profile's custom command is not a login shell, so it starts with a PATH that has no `gh`, and vigil's dependency check then refuses to start and iTerm2 closes the pane with no explanation.

Set `panel_auto = "false"` alongside this, or every tmux session gets a second, redundant panel. It has to go *inside* the `[settings]` table - appending it to the end of `config.toml` lands it in whatever table comes last, where it is read as a hook and silently ignored. `vigil config get panel_auto` is the check. Transition side effects keep working either way: the outside panel starts a daemon like every other mode.

**A panel outside tmux is a read-only board.** Session switching is gated on running inside a tmux client, so `enter` does nothing there - switch with the `M-j`/`M-k`/`M-<n>` tmux bindings in the pane below, which never invoke vigil. Auto-focus is off for any panel, inside tmux or not: it exists to aim the detail panel at whatever needs attention, and a panel has no detail panel.

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
queue_enabled = true          # Poll for work queue (stories and review-requested PRs)
queue_interval = 60           # Seconds between queue polls
queue_limit = 20              # Max items per queue section

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
| `dispatch` | `{input}`, `{flags}` | *(none — must be configured)* |
| `merge` | `{branch}`, `{git_root}` | `gh pr merge {branch} --squash --delete-branch` |
| `approve` | `{branch}`, `{git_root}` | `gh pr review {branch} --approve` |
| `notify` | `{session}`, `{old_state}`, `{new_state}` | `tmux display-message -d 5000 "vigil: "{session}" → "{new_state}` |

The built-in cleanup kills the tmux session, then removes the git worktree if the session directory is one. For non-worktree sessions, it just kills the session. Override with a hook for custom behavior.

The default merge uses `--squash --delete-branch`. Override `[hooks] merge` for a different strategy. Set any hook to `""` to disable it.

The default `notify` hook's quoting looks wrong and is not. Each placeholder is substituted as one shell-quoted word, so a placeholder left *inside* a larger double-quoted string lands as `'...'` within `"..."` - and a session name containing a double quote closes that string early, splitting the message into two arguments, which `tmux display-message` refuses with `too many arguments (need at most 1)`. Closing the literal before each placeholder and reopening after it lets the shell concatenate the pieces into the single argument tmux wants. Do not rewrite it as `tmux display-message "vigil: {session} → {new_state}"`; that form has never worked.

Hook bodies must not contain `${VAR}`. A braced shell expansion collides with the `{placeholder}` syntax: `${VAR}` is read as the placeholder `{VAR}`, which is not a known variable, and the hook fails with `unknown placeholder in hook template` before `sh` ever sees it. Use `$VAR` instead. This applies to every hook, not just `dispatch`.

### Work queue settings

| Setting | Default | Description |
|---------|---------|-------------|
| `queue_enabled` | `true` | Poll for assigned stories and review-requested PRs. `false` constructs no pollers at all. |
| `queue_pr_query` | `review-requested:@me -is:draft` | Passed to `gh search prs` after `--`, split on whitespace. A qualifier containing a space is not supported. |
| `queue_pr_age_days` | `14` | Appended as `updated:>=<date>`, recomputed each poll. GitHub search has no relative dates, which is why this is a separate setting rather than part of the query. `0` disables the window. |
| `queue_story_query` | `owner:%self% !is:done !is:archived` | Passed to `short api /search/stories?query=`. `%self%` is substituted with your Shortcut mention name (looked up via `short api /member` and cached) before the query is sent - `short api` does no templating of its own, unlike `short search`. Names no workflow state on purpose: state names are workspace-specific. |
| `queue_interval` | `60` | Seconds between queue polls. |
| `queue_limit` | `20` | Caps each fetch and the merged list. |

### Dispatch

`d` in the TUI, or `vigil dispatch <url-or-id>` from a shell, submits a job to `vigild`, which runs the `dispatch` hook and streams its output into a job line every panel shows. `vigil dispatch` starts a daemon if none is running, and exits as soon as the daemon acknowledges the job: exit 0 means accepted, not finished. The job outlives the submitting process, which is the point - the shell that submitted it is usually about to be replaced by the session the job creates.

The hook runs **inside the daemon**, which has no terminal:

```toml
[settings]
dispatch_timeout = 300        # Seconds before a running dispatch is killed

[hooks]
dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}"
```

> `{flags}` expands to `--detached` when the dispatch came from the work queue and to nothing otherwise. It is the one placeholder vigil does not shell-quote, because it carries a vigil-chosen constant rather than external data. A hook without it still works - queue selections just teleport - and vigil warns at startup.

What that means for the hook you write:

- **No popup, no tty.** A hook that opens `tmux display-popup -E` has no client to draw on and will hang until `dispatch_timeout` kills it. Run the work inline instead. If you use the `dispatch` script from `~/dotfiles`, `DISPATCH_INLINE=1` is what selects that branch, and the older `DISPATCH_IN_POPUP` is gone.
- **Do not hardcode `--detached`.** Use `{flags}` instead: vigil supplies `--detached` itself for queue-originated dispatches, where the user is mid-edit and should not be teleported away. A hook with a literal `--detached` skips the teleport for every dispatch, including ones started by hand from the TUI.
- **`VIGIL_CLIENT` is exported into the hook.** It names the most recently active tmux client, resolved per job rather than per submission, and is how a client-less daemon can still pick a switch target, a window size, and a panel orientation. It is empty when no client is attached, and a hook must treat that as "nobody is watching" rather than an error.
- **`dispatch_timeout` (default 300s, `VIGIL_DISPATCH_TIMEOUT`) bounds the job.** On expiry the hook's whole process group is killed, backgrounded grandchildren included, and the job reports the timeout rather than its last output line.
- **Jobs run one at a time.** Two concurrent `git worktree add` calls in one repository contend on the index lock, so submissions queue; a duplicate of an in-flight input is refused rather than queued.

If your `dispatch` hook still passes a literal `--detached` or still names `DISPATCH_IN_POPUP`, vigil prints a warning at startup naming this section. If it is otherwise fine but has no `{flags}` placeholder, vigil warns separately that queue selections will teleport.

### Environment variable overrides

Environment variables override TOML settings for quick testing:

`VIGIL_TMUX_INTERVAL`, `VIGIL_GIT_INTERVAL`, `VIGIL_PR_INTERVAL`, `VIGIL_CACHE_TTL`, `VIGIL_LOG_LEVEL`, `VIGIL_GIT_WORKERS`, `VIGIL_CAPTURE_WINDOW`, `VIGIL_STALE_THRESHOLD`, `VIGIL_NOTIFICATIONS`, `VIGIL_AUTO_CLEANUP`, `VIGIL_AUTO_FOCUS`, `VIGIL_PANEL_AUTO`, `VIGIL_DISPATCH_TIMEOUT`, `VIGIL_QUEUE_ENABLED`, `VIGIL_QUEUE_PR_QUERY`, `VIGIL_QUEUE_PR_AGE_DAYS`, `VIGIL_QUEUE_STORY_QUERY`, `VIGIL_QUEUE_INTERVAL`, `VIGIL_QUEUE_LIMIT`

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

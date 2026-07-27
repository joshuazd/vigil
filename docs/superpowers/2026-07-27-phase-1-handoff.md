# Vigil Cockpit: state after phases 0-1

Written 2026-07-27, at the point phases 0 and 1 merged. Read this plus the spec
before starting phase 2.

- Spec (the whole 6-phase design, plus a carry-forward section): `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`
- Executed plan (phases 0-1 only): `docs/superpowers/plans/2026-07-27-vigil-cockpit-phases-0-1.md`

The goal of the whole design: make the vigil session list the surface you live in,
with sessions expanded next to it, and give dispatch a durable place for work to land
instead of a `send-keys` into a borrowed tmux session.

## What is built and merged

**Phase 0 (`~/dotfiles`, 11 commits, merged to `master`).** Claude is now the tmux
pane's own process via `tmux respawn-pane -k` rather than text typed in with
`send-keys`, so there is no shell-readiness race. Its multi-line system prompt travels
in a file inside the worktree's private git dir (`vigil-launch-prompt.txt`), so it
never appears in `git status` and dies with the worktree. Re-dispatching a story that
already has a live session switches to it instead of relaunching, which matters
because `respawn-pane -k` would SIGKILL the Claude running there.

Also in phase 0: the first test harness that package has ever had (24 bats tests plus
a `tmux` stub that records argv), `make lint` in CI, and `shell/.zshenv` so a
non-interactive `zsh -c` gets a real environment.

**Phase 1 (`~/vigil`, 23 commits, merged to `main`).** `vigil daemon` polls once and
broadcasts snapshots over a Unix socket. The TUI prefers it and self-polls as a
permanent, supported fallback. New packages: `internal/collect` (UI-independent state
collection), `internal/protocol` (newline-delimited JSON over the socket),
`internal/daemon` (the server). First tests ever in `internal/model`.

Phase 1 is deliberately invisible: the TUI renders identically whether the daemon runs
or not. It bought architecture, not features. Its entire point is making a
panel-per-session affordable in phase 2 without multiplying the `gh` budget.

## Current machine state

- `~/.local/bin/vigil` is the phase-1 build (`v1.2.3-26-gb081bda`), installed via
  `make install`. Reversible with `brew reinstall vigil`.
- `~/.zshenv` is symlinked into the repo. The previous hand-written file (Portal S3
  stub exports) was moved to `~/.zshenv.local`, which `.zshenv` sources first, before
  its interactive/login early return, so those exports still reach every shell.
- **The daemon is not running and is not set up to autostart.** Deliberate, see below.
- `stow shell` still aborts on `.functions` and `.fzfrc`. Both are already correct
  symlinks into the repo, just hand-made with absolute paths so stow will not claim
  them. Pre-existing; a two-minute cleanup whenever it is convenient.

## Verification status

- **Phase 0 verified end to end.** A real `dispatch sc-222545` created the worktree and
  session, mise tool versions resolved correctly inside the worktree, the window stayed
  named `claude`, quitting Claude left a usable shell, and a second dispatch of the same
  story switched to the live session rather than relaunching. Running `dispatch`
  directly from inside tmux produces popup-in-popup weirdness; the menu-bar path sets
  `DISPATCH_IN_POPUP=1` and does not.
- **Phase 1's daemon runs correctly**: socket created, second instance refused with
  `vigil: daemon already running`, SIGTERM removes the socket, silent when healthy. Run
  under real conditions and observed working.
- **Phase 0 confirmed working in production**, beyond the single dispatch test: three
  concurrent live sessions were observed running
  `zsh -c claude ... --append-system-prompt "$(cat <worktree>/.git/worktrees/<name>/vigil-launch-prompt.txt)" ... ; exec "${SHELL}"`,
  which is exactly the designed shape.
- **PR cadence gating verified by measurement.** With the daemon polling 8 sessions and
  no TUI client attached, a 300s sample consumed **160 GraphQL calls and 0 core**,
  extrapolating to ~1,920/hour against a 5,000/hour limit. That matches
  8 sessions x 2 calls x 10 cycles exactly, so `pr_interval` gating is behaving as
  designed. With the original bug the same window would have been roughly 1,600 calls,
  about 19,200/hour, roughly 4x over the limit.
- **Note `gh` routes PR reads through GraphQL**, not REST, so GraphQL's 5,000/hour is
  the budget that matters. `core` stays at 0.
- **Still not done: the daemon-up versus daemon-down TUI comparison.** The daemon has
  been run, but no TUI client had connected at the time of checking (`lsof` on the
  socket showed only the daemon itself), so the invisibility claim still rests on static
  analysis and unit tests rather than observation. To do it, run `vigil` with the daemon
  up and watch timing rather than appearance: how fast a new session appears, how fast a
  bell highlights, and whether any PR column blanks or any spurious `-> idle`
  notification fires. Then compare with the daemon stopped.

## Polling cadences

The daemon runs one `Snapshot` per tick at `tmux_interval` (default 1s), matching the
TUI's hardcoded 1s tmux tick. Git and PR work is gated *inside* `Snapshot` by per-key
memos in `collect.Collector`: git on `git_interval` (3s default, keyed by pane path),
PRs on `pr_interval` (30s default, keyed by branch + git root). A failed `gh` fetch
reuses the last known PR rather than reporting none.

Three separate tick loops were deliberately rejected: they would need to merge partial
updates into a retained session list, which is exactly the logic in the TUI's
`handleTmuxUpdated` that discards `Git`/`PR` when it goes wrong.

**Why the gating matters.** An earlier version of the daemon polled PRs on
`git_interval`, which is two `gh` calls per open PR every 3 seconds. With five open PRs
that is roughly 12,000 calls/hour against a 5,000/hour token. Once limited,
`FetchPRStatus` returns nil, every session flips to `idle`, and each fires a
notification plus the `notify` hook, then flaps back. Do not remove the memos, and if
you add a fourth concern, gate it too.

## Cheap win available in phase 2

An idle daemon costs ~38% of the GraphQL budget (measured above), and the user's own
dispatch and review work draws on the same pool. Two levers, in increasing effort:

1. **`pr_interval = 60`** in `~/.config/vigil/config.toml` halves it to ~960/hour, with
   no code change. PR state rarely moves faster than a minute.
2. **Only one of the two GraphQL calls per PR is broadly needed.** `FetchPRStatus` makes
   a `gh pr view --json` call plus a `gh api graphql` call for review threads
   (`internal/fetch/pr.go`). The review-thread data is only consumed by the detail
   panel's review-comments mode, i.e. for the *selected* session. Fetching it lazily for
   the selection rather than for every session on every cycle roughly halves the daemon's
   cost. This matters more in phase 2, where a panel per session means more clients but
   the same single poller.

## Must be fixed before phase 2 ships

All three are fine with one client and not fine with a panel per session.

1. **The daemon has no start-time mutual exclusion.** With a stale socket file present,
   two daemons can both unlink and both bind, leaving two live daemons with the first
   orphaned yet still polling and still writing the shared cache. Mapping `EADDRINUSE`
   to `ErrAlreadyRunning` covers only the friendly-message case. Dormant today because
   nothing autostarts the daemon; it becomes live the moment phase 2 has `vigil` spawn
   it on demand from N panels, which is precisely that race run concurrently. Fix: an
   flock'd lock file.
2. **One wedged client stalls the poll loop.** The daemon sends to clients sequentially
   with a 5s write deadline, and the connect-time send runs on the accept goroutine, so
   a client that connects and never reads also blocks new connections. A single-writer
   design (accept hands new connections to the `Run` loop over a channel) fixes both.
3. **No reconnect, and no staleness signal for a live-but-silent daemon.** The client's
   first-snapshot read deadline is cleared once the first snapshot arrives, so a daemon
   that is alive but not broadcasting freezes the TUI on stale data with no indicator.
   Fallback is one-way and permanent.

Related but lower priority: `Run` does not `WaitGroup`-wait for the accept goroutine.
Harmless now, and it already caused one test flake when a change made polling faster.

## Landmine to check before phase 3

`launch_claude_in_pane` targets the Claude pane positionally as `:claude.1`, while the
spec's panel geometry uses `split-window -vb` / `-hb`, which inserts the new pane
*before* the existing one. tmux pane indexes are positional, so adding a panel likely
renumbers the Claude pane to `.2`, and phase 0's respawn would then target the panel.
Prefer a `pane_id` or a pane title over a positional target.

## Other things worth knowing

- `internal/collect` and the TUI's self-polling in `model.go` now implement the same
  job twice, and have already drifted once: the original extraction silently dropped
  branch deduplication. Collapsing the fallback onto `collect` is the durable fix and a
  natural phase-2 task.
- Two dependencies are correct today but unrecorded in code: concurrent `send` on one
  connection is frame-safe only because `protocol.Encode` performs exactly one `Write`
  and Go takes a per-fd write lock; and `internal/fetch`'s `MockCommander` is
  concurrency-safe for its call log only, not its handler maps.
- A permanently failing `gh` now shows the last known PR indefinitely on both paths,
  with no staleness marker. Deliberate trade behind memoizing PR state.
- `cache.Save` uses `os.CreateTemp`, so a hard-killed writer can leave `cache-*.json`
  litter in `~/.local/share/vigil/`. `Load` only reads `cache.json`, so it is inert.

## Process notes for phase 2

Phases 0-1 ran as: brainstorm -> spec -> plan -> one implementer subagent per task,
each followed by a task review, then a whole-branch review and a fix wave. That caught
a lot, including one Critical that no per-task review could see because it lived in the
composition rather than in either part.

Two habits worth keeping:

- **Check whether a plan-authored test would fail if its subject were deleted.** Five
  tests written into the phases 0-1 plan would have passed with their code removed. The
  implementations were mostly fine; the verification was not.
- **Do not accept "pre-existing" without checking.** It was claimed twice and was wrong
  twice. A throwaway `git worktree add` at the base commit settles it in a minute.

Two specific traps that recurred: `net.UnixListener.Close()` unlinks its socket by
default, so a test that just closes a listener proves nothing about explicit removal;
and a test fixture standing in for the script under test hides ordering bugs in that
script.

# Vigil Cockpit: state after phase 2

Written 2026-07-27, at the point phase 2 merged. Read this plus the spec before starting
phase 3.

- Spec (the whole 6-phase design, plus the authoritative debt list in "Still open after
  phase 2"): `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`
- Executed plan (phase 2 only): `docs/superpowers/plans/2026-07-27-vigil-cockpit-phase-2.md`
- Superseded, kept for the reasoning behind the polling cadences:
  `docs/superpowers/2026-07-27-phase-1-handoff.md`

The goal of the whole design: make the vigil session list the surface you live in, with
sessions expanded next to it, and give dispatch a durable place for work to land instead
of a `send-keys` into a borrowed tmux session.

## What is built and merged

**Phase 2 (`~/vigil`, 23 commits, merged to `main`; `~/dotfiles`, 4 commits, merged to
`master`).** `vigil --panel` renders the session list compactly in a tmux pane, and
`prefix p` toggles one into the current window. Nothing auto-attaches: a session is
untouched until the key is pressed. That is the whole visible feature.

Most of the work is underneath it, because a panel per session means many clients where
there was one:

- **The daemon serializes startup on an flock'd lock file** (`vigild.sock.lock`, beside
  the socket), held across the stale-socket removal and the bind. Phase 1's blocker: two
  daemons could both find a stale socket, both unlink it, and both bind.
- **Every client gets its own writer goroutine and a one-deep latest-wins queue.** Accept
  hands connections to `Run` over a channel and `Run` owns the client list, so a client
  that stops reading can neither stall the poll loop nor block new connections.
- **Every self-rescheduling tick carries an epoch.** Bubble Tea ticks cannot be cancelled,
  so each switch between daemon and self-polling bumps a generation counter and the
  previous generation's ticks retire themselves on arrival. Without it, reconnection
  would leave a poll loop running per switch for the life of the process.
- **A client that loses the daemon probes every 2s until it returns**, and a daemon that
  is connected but silent for three poll intervals shows `daemon stale Ns`. Before this,
  fallback was one-way and permanent, and a live-but-silent daemon froze the TUI on stale
  data with no indicator.
- **The table drops columns as width shrinks** (`view.LayoutForWidth`), which is what
  makes a 40-column pane usable. At width >= 104 nothing changed: the name column is
  capped at 52 and never stretches.

On the dotfiles side, `scripts/vigil-panel` measures the client and splits (a strip on
top for a portrait client, a 40-column column on the left otherwise), and pane targeting
moved from positional `:claude.1` / `.2` to `pane_id` resolved from a `@vigil_claude`
marker. That last one closes the landmine phase 1 flagged for phase 3: `split-window -b`
inserts before the existing pane, so every index in the window shifts when a panel opens.

## Current machine state

- `~/.local/bin/vigil` is the phase-2 build (`v1.2.3-52-g157939f`), installed via
  `make install`. Reversible with `brew reinstall vigil`.
- `prefix p` and `prefix C-p` are live in the running tmux server.
- **A daemon is running and will keep running.** The first panel spawns one, detached
  with `Setsid`, so closing the pane or the whole session leaves it alive.
  `pkill -f 'vigil daemon'` stops it.
- **Nothing is pushed.** Both merges are local: `main` ahead of `origin/main`, `master`
  ahead of `origin/master`.
- `stow shell` still aborts on `.functions` and `.fzfrc`. Both are already correct
  symlinks into the repo, just hand-made with absolute paths so stow will not claim them.
  Pre-existing, unrelated, still a two-minute cleanup whenever convenient.

## Verification status

Everything below was observed on the real machine, not inferred. Method: detached tmux
sessions of fixed sizes with vigil as the pane command, an isolated `XDG_RUNTIME_DIR` so
nothing touched the live socket, and `tmux capture-pane -p` to read what actually
rendered.

- **The flock works under the race it was written for.** Ten concurrent `vigil daemon`
  starts against one socket: exactly one survived, nine printed `vigil: daemon already
  running`. Phase 1's blocker, verified for the first time.
- **The daemon-up versus daemon-down comparison phase 1 never ran.** Full dashboard at
  120x20 against a daemon, then with the daemon killed: git and PR columns byte-identical,
  no PR blanking, no layout change. The invisibility claim is now observed.
- **Reconnect works.** `daemon back, streaming snapshots` appeared within 3s of a daemon
  returning.
- **Setsid detaches.** Killing the panel's tmux session left the daemon running.
- **The daemon log stays empty when healthy**, which is the point of only logging a
  dropped client when its write deadline expires rather than on every routine disconnect.
- **Column dropping and live resize.** Real sessions rendered at 110, 40 and 24 columns,
  and one pane driven 110 -> 40 -> 24 -> 110, with correct columns at each and no line
  ever exceeding the pane.

One thing that could not be explained: during the reconnect test, the daemon log gained a
single `vigil: daemon already running` line in the same minute a daemon started
successfully. Final state was unambiguous and correct - one daemon, holding the socket,
serving the TUI - and that message is the flock refusing a duplicate, which is designed
behaviour. But which process emitted it was never established, and the ten-way race test
covers the same property deliberately, so nothing rests on it.

## The gh budget changed shape

Phase 1 measured an idle daemon at roughly 1,920 GraphQL calls/hour with 8 sessions,
against a 5,000/hour limit shared with your own `gh` use. That number has not changed per
daemon, and N panels still cost the same as one client, because they all read one poller.

What changed is that **a daemon now exists**. Before phase 2 nothing autostarted it, so
the background cost was zero unless you started one by hand. The first `prefix p` starts
one and it persists. Two levers, unchanged from phase 1:

1. `pr_interval = 60` in `~/.config/vigil/config.toml` halves it, no code change.
2. Only one of the two GraphQL calls per PR is broadly needed. `FetchPRStatus` makes a
   `gh pr view --json` call plus a `gh api graphql` call for review threads
   (`internal/fetch/pr.go`), and the review-thread data is only consumed by the detail
   panel's review-comments mode, i.e. for the selected session. Fetching it lazily would
   roughly halve the daemon's cost. Still not done.

Note that panels never open the detail panel, deliberately: it runs `capture-pane` every
tick, and one panel per session would multiply that.

## Must be resolved before phase 3 ships

1. **State-transition side effects run once per attached client, not once per event.**
   `checkStateTransitions` lives in the model and every client keeps its own `prevStates`,
   so a session going Blocked fires the `notify` hook once per panel, and with
   `auto_cleanup = true` it runs `action.CleanupSession` - `git worktree remove` plus
   `tmux kill-session` - once per panel, concurrently, against the same worktree. Latent
   today only because `auto_cleanup` defaults to false and no `notify` hook is configured.
   **Do not enable `auto_cleanup` while panels are open.** Per-client toasts are correct
   and should stay per-client; hooks and cleanups are not. The durable fix is moving the
   side effects to the daemon, which owns one view of state and can fire each transition
   once. Phase 3 makes the panel default for new sessions, which is exactly what makes
   N clients normal rather than exotic.
2. **`internal/collect` and the TUI's self-polling still implement the same job twice**,
   and have already drifted once. Carried from phase 1, still true, still the durable fix.
3. **The `colIndex` / `colState` layout constants reserve 2 columns where the renderers
   emit 1**, so every tier threshold is 1-2 columns pessimistic. Safe direction, but it
   hid a dropped truncation until a fixture was widened. Deliberately deferred rather than
   fixed at the end of phase 2, because it shifts every threshold and every layout test.

## Found by using it, after the merge

Two things that only a real panel could surface, both fixed or recorded the same day:

- **Enter closed the panel.** `popupMode` was derived from `os.Getenv("TMUX") != ""`,
  which was a correct proxy for "transient popup" until a third surface existed that also
  runs inside tmux. `handleSelect` and `handleOpenPR` both did `m.cancel(); tea.Quit` in
  that mode, so the panel switched to the session and then deleted itself, with
  `remain-on-exit off` closing the pane. Fixed by splitting the concept: the field is now
  `insideTmux`, which means only that, and `exitsAfterAction()` (`insideTmux && !panelMode`)
  answers the question the quit sites are actually asking. A composition defect of the
  kind phase 1's retro warned about: two correct parts, plus a third mode nobody
  reconciled with them. No reviewer caught it because no task brief mentioned Enter.
- **`make install` with a daemon running produced a binary that would not run.** The
  target did `cp` in place, and since phase 2 a daemon runs from `~/.local/bin/vigil`
  continuously. Overwriting the inode of a running image invalidates its code signature,
  and macOS then SIGKILLs every later exec of that path (exit 137, no output). Fixed by
  installing through a temp file and renaming. Pre-existing bug, newly reachable: before
  phase 2 nothing ran persistently from that path.

## Landmines and sharp edges

- **`daemon stale Ns` can cry wolf on a cold start.** The threshold is
  `max(5s, 3 * tmux_interval)`, and `Snapshot` is synchronous per tick, so a freshly
  spawned daemon's first poll (cold git plus `gh` across every session) can block the
  broadcast for longer than that. Observed once at 5s on a first spawn; a warm daemon
  never showed it. Harmless and self-correcting, but if it becomes annoying the fix is to
  not start the staleness clock until the second snapshot, rather than to raise the
  threshold.
- **`visibleLen` counts runes, not display columns.** A CJK session name or any
  double-width glyph still overflows a pane, and the "never exceeds its width" tests
  assert against the same rune metric the renderer uses, so they cannot see it.
  Pre-existing, untouched by phase 2, and the first thing to suspect if a panel ever wraps.
- **`internal/view`'s tests prove less than they look about styling.** Under `go test`
  there is no tty and no forced colour profile, so lipgloss emits zero ANSI bytes and
  every "styled" cell in those tests is a plain string. The synthetic-string test for
  escape-aware truncation is the only coverage of that behaviour.
- **A permanently failing `gh` still shows the last known PR indefinitely** with no
  staleness marker on either path. The new marker covers a silent daemon, not a silent
  `gh`. Deliberate trade behind memoizing PR state.
- **The daemon only prunes dead clients after a successful poll.** While
  `Collector.Snapshot` keeps failing, clients that connect, time out and reconnect can
  accumulate a goroutine and an fd each. Bounded by poll recovery and hard to reach, but
  the 2s probe is new and it is what would make that churn continuous.
- **Panel notifications can be dropped.** `RenderTable` only places a notification when a
  padding row is left over, so a 10-row panel showing 10 sessions never renders "daemon
  lost, polling directly". The `no daemon` health segment covers the same ground, so this
  is an inconsistency between two signals rather than a hole.
- `cache.Save` uses `os.CreateTemp`, so a hard-killed writer can leave `cache-*.json`
  litter in `~/.local/share/vigil/`. `Load` only reads `cache.json`, so it is inert.
  Carried from phase 1.

## Process notes for phase 3

Phase 2 ran as: plan -> one implementer subagent per task, each followed by a task review
(spec plus quality), then a whole-branch review on a stronger model and a single fix wave.
Nine tasks, eleven fix rounds, one final wave.

**Nine defects were found in the plan during execution. Every one was in test code or in
cross-task ordering. None was in implementation prose.** The plan's own process notes,
carried forward from phases 0-1, warned that five tests in the previous phase would have
passed with their subject deleted - and the same author then wrote six more of exactly
that kind. Self-review did not catch any of them. What caught them was reviewers tracing
fixture state against guard order, and implementers who flagged a brief-mandated test as
vacuous instead of banking a green suite.

The specific shapes, because they recur:

- **A guard that cannot be reached.** A test asserting a guard rejects something, against
  a fixture whose zero-value state already trips an unrelated earlier guard. Deleting the
  guard under test changes nothing. Fix: give the fixture live values so the guard under
  test is the only thing that can produce the asserted outcome.
- **An assertion satisfied by the wrong thing.** Several substrings grepped against a
  flattened log, each satisfiable by a different call. In one case, dropping `-p` from the
  panel marker passed the whole bats suite while causing `prefix p` to kill the user's
  Claude pane. Fix: assert co-occurrence within one recorded invocation.
- **A metric that cannot see the failure.** The table tests asserted per-line width, but
  lipgloss wraps an over-wide row into sub-lines that each individually pass. The property
  that mattered was "N rows in, N lines out". Fix: assert the invariant the feature exists
  to protect, not a proxy for it.
- **A test whose subject is never exercised.** After the wrap assertion was added, the
  fixture's cells were still narrower than every column, so no truncation ever ran. A
  passing test that never reaches its subject is a vacuous test one level removed.
- **Arithmetic in a test that was never checked against the implementation.** One test
  asserted a segment survives at a width where the budget could never fit it.
- **Cross-task ordering.** A helper assigned to task 5 was needed by task 4.
- **Documentation asserting code that does not exist.** `CLAUDE.md` was made to claim the
  `prefix p` toggle script existed while it lived in no repository yet, in a repo the
  branch could not merge. `CLAUDE.md` is loaded as authoritative agent context, so that
  line would have been believed.

Three habits worth keeping:

- **Every plan-authored test needs an explicit "would this fail if the code it names were
  removed?" pass before dispatch.** Stated in the phase 0-1 notes, not actually performed
  when writing the phase 2 plan. Performing it means mutating the code, not reading the
  test.
- **Mutation-verify a fix, not just its test.** The strongest reviews in this phase
  deleted the production line and reported which test failed and on which line. Several
  findings survived a green suite and died to a mutation.
- **A subagent that says "I kept the brief's test verbatim but it is vacuous" is doing the
  job correctly.** Two implementers did this and both were right. Brief-mandated defects
  are the controller's to fix, not the implementer's to silently work around.

One process hazard worth naming: the working ledger for this phase held the defect list,
the adjudicated residuals and the verification results, and the finishing skill directs
that the workspace be deleted on merge because "git history is the record now". Git
history did not hold any of that. This document exists because it was reconstructed
before the session ended. Write the handoff before deleting the workspace, not after.

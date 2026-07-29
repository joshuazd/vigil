# Phase 3: state after the branch

Written 2026-07-29 at the point `phase-3-panel` was finished in both repositories and
before it was merged. Suite green in both: `go test -race ./...` 12/12 packages and
`golangci-lint` 0 issues in `~/vigil`, `bats tests/` 60/60 in `~/dotfiles`.

- Design: `docs/superpowers/specs/2026-07-29-phase-3-panel-by-default-design.md`.
  Corrected during execution in four places; the corrections are described below.
- Executed plan: `docs/superpowers/plans/2026-07-29-phase-3-panel-by-default.md`.
- Prior state: `docs/superpowers/2026-07-28-phase-2-blockers-handoff.md`. Still the best
  account of the daemon, the transition split and the landmines, and still current on
  everything this branch did not touch.

Phase 3 makes new tmux sessions come up with a vigil panel already in them. Branches are
`phase-3-panel` in `~/vigil` (from `main` at `630e33e`) and in `~/dotfiles` (from `master`
at `40b5106`).

## What landed

**`vigil config get <key>`.** `main.go` gained a `run(args, stdout, stderr) int` seam and
`parseArgs` a second return value, so `main` is one call and dispatch order is assertable.
The `config` case returns alongside `help` and `version`, before the `tmux`/`git`/`gh`
`LookPath` check: a bash caller that got "gh not found" instead of a value would silently
disable the panel on a machine mid-setup, and that is indistinguishable from the setting
being off. A known key prints its value and exits 0; an unknown key prints nothing to
stdout and exits 1, which is what lets a caller tell "off" from "not a setting".
`config.IsSetting` exists for that distinction, because `GetSetting` returns `""` for both
an unknown key and `capture_window`, which is legitimately empty by default.

**`panel_auto`.** New setting, env var `VIGIL_PANEL_AUTO`, default `"true"`.

**The daemon ownership grace.** `Model.effectsDisownedUntil` is consulted next to `local`
in `checkStateTransitions`, so a panel that has just spawned a daemon does not also own
that daemon's events. The rule is **not** the one the design originally specified - see
the corrections below.

**The panel split moved into `lib/tmux.sh`.** `add_vigil_panel <window-target>` and
`panel_geometry` now live beside `create_tmux_session`, and `vigil-panel` is reduced to a
toggle that sources the lib. `create_tmux_session` calls `add_vigil_panel` for the claude
window, gated on `vigil config get panel_auto`, fail-soft throughout.

**`setup_secondary_pane` measures the pane it splits**, not the window. Only meaningful in
combination with the panel landing first; see the ordering note.

## Corrections to the design, made during execution

Four. The first two are the ones that matter.

**The panel was sized against an 80x24 window.** The design said the no-client case only
needed `panel_geometry` to avoid arithmetic on an empty string. That was not the problem.
`create_tmux_session` always runs `tmux new-session -d`, and a detached session has no
client, so tmux sizes the window to `default-size` - 80x24 - and nothing in
`~/dotfiles/tmux/.tmux.conf` overrides it. `panel_geometry` measures the *calling* client,
returns `-hb 40`, and that 40 is applied to an 80-column window: half of it. tmux
redistributes panes proportionally on attach. Measured on an isolated server with a real
350x90 client: **175 columns before the fix, 40 after.** `new-session` now takes `-x`/`-y`
from a factored `client_dimensions`, omitted when there is no client, because real tmux
rejects `-x ""` with "width invalid" and creates nothing.

Nothing in bats could see this: the stub records argv and has no geometry. And `prefix p`
is the one path unaffected, because that window is already at client size - which is why
seven per-task reviews missed it and only the final whole-branch review caught it.

**The arming rule is connection-gated, not arm-once.** The design specified "set once, on
the first successful spawn only". That covers cold start but not daemon restart: a
long-running panel whose daemon crashes respawns one from a failed probe, does not arm,
and both processes then own the same event - two cross-process `CleanupSession` calls on
one worktree for a `Done` event, since `inFlightEffects` is per-process. What shipped
instead: **arm on any successful spawn, provided the client has had a live daemon
connection since the last arm**, carried by `daemonSeenSinceArm`, set at the two places a
working connection is established (`model.go:263` in `newModel`, `model.go:1092` in
`handleProbeResult`) and cleared on arm. A daemon that never comes up never sets it, so it
still cannot chain suppressions.

The design's stated reason for arm-once was also false. It claimed re-arming every spawn
would suppress effects "forever". `spawnCooldown` is 15s and `spawnGrace` is 5s, so it
would suppress about 5s in every 15s. Degraded, not silent.

**The panel goes in before `setup_secondary_pane`, not after.** The plan initially said
after, "so that split sees post-panel width", which is backwards: a panel added afterwards
is exactly what that split cannot see. The two changes are one change in two files.

## The two-owner window is narrowed, not closed

State this precisely, because the spec's "what this does not fix" section used to name only
the N-self-polling-clients case.

The grace arms on **spawn**, but the hazardous state is "self-polling while a daemon is
alive". `handleDaemonLost` calls `startPoll` and the client owns effects from that instant;
it does not call `spawnDaemonOnce`, and only a *failed* probe does. So a client that loses
its connection to a live daemon owns effects alongside it until the next probe reconnects.

`firstSnapshotTimeout` is 5s and is re-armed on every reconnect, so a daemon whose first
`Snapshot` exceeds it - git plus `gh` across many sessions on a cold dispatch, which is
precisely the phase-3 scenario - can put a panel in a repeating loop of connect, timeout,
self-poll, reconnect. `inFlightEffects` is per-process, so a `Done` landing in one of those
laps is two `CleanupSession` calls against one worktree.

Bounded by the fact that a daemon which has not produced a first snapshot has not detected
transitions either, so the overlap only bites on a later excursion. But do not carry
forward a belief that exactly one process owns effects unconditionally. It does not.

## Verification status

Run 2026-07-29 after `make install` and a daemon restart. Method for the isolated checks:
a `tmux` shim on `PATH` forwarding to `tmux -L <name>`, an isolated `HOME` and
`XDG_RUNTIME_DIR` so the config and socket paths never touch the real ones, and a `notify`
hook appending to a file. The developer's tmux server, daemon, config and sessions were
left byte-identical, and were checked afterwards.

- [x] **`make install` and daemon restart.** Installed via temp-file rename, new inode, so
      the running image's code signature was untouched. The old daemon was killed and a
      live panel respawned one from the new binary within 4 seconds - the reconnect and
      respawn path, observed on the real machine.
- [x] **`vigil config get` against the real binary.** `panel_auto` prints `true`, exit 0.
      An unknown key prints nothing to stdout, exit 1.
- [x] **A new session comes up panelled, at the right size.** A throwaway session created
      through the real `create_tmux_session` on the real server, with a 152x127 portrait
      client: window 152x127, panel pane 152x10, claude pane 152x116. The `-vb 10` top
      strip, correct. Pre-fix this window would have been 80x24 and the 10 rows would have
      scaled to roughly 52.
- [x] **`panel_auto = false`.** Set in the real config file, backed up first. The session
      came up with a single 152x127 pane and no panel marker. Restored and diffed clean.
- [x] **The suite survives `panel_auto = "false"` in the real config.** Confirms the final
      review's finding that `TestConfigGetAnswersWithoutTheDependencies` was reading the
      developer's real `~/.config/vigil/config.toml`. It no longer is.
- [x] **`prefix p` round-trips.** On an isolated server with a 200-column client, driving
      the real script through `run-shell` as the keybinding does: 0 panels, then 1 at
      40x50, then 0, then 1.
- [x] **The nit split sees the narrowed pane.** With `pane_command` set, panes come out
      40 / 154 / 154 and the split correctly picks `-h`. This is the Task 6 and Task 7
      pairing observed end to end, which no bats test can do against a stub returning a
      constant `pane_width`.
- [x] **A cold start fires `notify` exactly once.** Isolated harness, panel spawns the
      daemon, detector primed at idle (0 notify lines after priming, bell flag 0), then one
      real transition (bell flag 1): **1** invocation, `work attention`.

**The counterfactual for that last one FAILED TO REPRODUCE, and this is the one real gap.**
The same harness against `main`'s binary - which has no grace period at all - also produced
**1**, at bell delays of 1.2s, 1.6s and 2.0s. The double-fire needs the transition to land
between the client's priming poll and its reconnect, a window of roughly one second, and it
could not be hit by hand. So the real-machine result confirms the branch behaves correctly
but does **not** demonstrate that it fixed anything observable at the process level.

What stands behind the fix instead is the unit coverage, which is unusually strong: six
mutations of the arming code - arm-once, arm-unconditionally, delete each of the two
setters, never clear the flag, drop the consult-side gate - each killed by a distinct named
test, verified independently by two reviewers on separate occasions. Treat the grace period
as proven by mutation testing and merely not contradicted by the machine.

Not attempted: a real dispatch of a real story. That creates a git worktree in a real
repository, which was more side effect than the verification needed.

## Deferred, by area

**`lib/tmux.sh`**

- `add_vigil_panel` sets `@vigil_panel` and then `remain-on-exit off` as two separate calls
  after the split. If the panel command exits immediately the pane is gone before the
  second call, which fails, and `add_vigil_panel` returns 1. Found while tracing a toggle
  run with `VIGIL_BIN=/bin/cat`: `no such pane: %1`, rc 1. With a real vigil this needs a
  vigil that dies at startup - a missing `gh`, for instance. Consequences are a `warn` on
  the create path and a non-zero exit from the keybinding, not a broken session. Setting
  `remain-on-exit` before the marker, or tolerating its failure, would close it.
- `client_dimensions` is now the first tmux call before `new-session`, so a dispatch from
  outside tmux with no server running prints a tmux connect error to stderr. `rc=0` and the
  session is correct. A `2>/dev/null` inside the helper is safe, since both callers key on
  empty stdout. Cheapest of these to fix and the only user-visible one.
- `[ "${width}" -ge 200 ]` in `setup_secondary_pane` silently takes the `else` branch on
  non-numeric or empty width, with no logging. Pre-existing, and it degrades toward `-v`,
  the safe direction.
- `-y "${client_height}"` is one row taller than the attached window ends up. Irrelevant for
  a left-hand column, would shave a row off a `-vb` top panel.
- The gate now guards with `command -v` and honours `${VIGIL_BIN:-vigil}`, both fixed at
  final review. Note the first-upgrade consequence: a stale `~/.local/bin/vigil` predating
  `config get` now *warns* on every session creation until `make install` runs. Intended,
  and better than silence, but it will be loud once.

**Tests**

- `tests/tmux_lib.bats:477` has a one-sided guard: `[ "$(... | grep -cFx ...)" -eq 0 ]`
  passes on empty output, so renaming the call so nothing is recorded leaves it green. It
  still dies under the mutation it was written for, so it is not dead, but it wants a
  positive anchor.
- `tests/tmux_lib.bats:492`, "the session size and the panel size come from one query",
  does not test that. Undoing the `client_dimensions` factoring leaves the whole suite
  green. The name overclaims.
- The tmux stub returns a constant `pane_width`, so nothing in bats can observe real
  geometry. This is the blind spot that hid the 175-column defect. Anything about pane
  *geometry* needs a real tmux server.

## Landmines

- **`tmux new-session -d` gives an 80x24 window.** Anything that splits or sizes a pane at
  session-creation time is sizing against 80x24 unless `-x`/`-y` are passed, and tmux
  rescales proportionally on attach. This branch fixed it for the panel. The next thing that
  splits at creation time will hit it again.
- **The 175-column balloon still happens in the genuinely headless case**: server up, no
  client, session created, then a wide client attaches. `client_dimensions` returns empty,
  the flags are omitted, and the window is 80x24 again. Deliberately out of scope - a
  headless creator has no dimensions to pass - but it is not fixed everywhere.
- **bats disables `errexit` inside `run`**, so a test that calls a function via `run` cannot
  prove that function is fail-soft under `errexit`. Two tests on this branch look like they
  prove it and do not. The property was verified separately by calling
  `create_tmux_session` directly under `set -o errexit`.
- **bash exempts a `!`-negated command from `errexit` unless it is the final statement.**
  A negated assertion is safe as a test's last statement and inert anywhere else. This
  invalidated one assertion during the run. A counted form
  (`[ "$(... | grep -c ...)" -eq 0 ]`) does not depend on position.
- **`tmux_call_args`, `tmux_call_args_matching` and `tmux_call_index` all end in a pipe and
  therefore always exit 0.** `run <helper>; [ "${status}" -eq 0 ]` can never fail. Assert on
  `${output}`. One test on this branch was vacuous for exactly this reason before it was
  caught. Every other use in the suite was audited and pairs the status check with a real
  assertion on the output.
- **`tmux_call_index` exits 0 with empty stdout on no match**, so an unguarded
  `[ "$a" -lt "$b" ]` becomes a bash "integer expression expected" error rather than a clean
  false. It fails loud, not silent. Guard with `[ -n "${x}" ]`.
- **Session names contain spaces.** `tmux list-panes -a -F '#{session_name} #{pane_id} ...'`
  piped to `awk '$3=="1"'` reads a word of the session name, not the third field. This bit
  the verification run itself and produced a wrong picture of which sessions had panels. It
  is the same whitespace-splitting family as the `BellFlags` landmine in the phase 2
  handoff. Use `|` separators and parse on those.

## Process notes

Eight tasks, one implementer subagent each, a task review after each, a final whole-branch
review, one fix wave and one scoped re-review.

**Six defects in the plan were written by its author, and every one was caught by a
mutation check rather than by reading a diff.** Four were tests that would have passed with
their subject deleted:

- The plan asserted `main_test.go` did not exist. It did, with a table covering all seven
  dispatch branches; the brief's verbatim replacement dropped `-h`, `-v`, `--panel` and
  no-args.
- A brief's test grepped a `split-window` log line for `"nit"`, which only ever appears on
  the `send-keys` line, so the lookup was always empty.
- The same test then passed against unfixed code by accident: the stub's fallthrough
  returned a path string, `[ "/tmp/stub-worktree" -ge 200 ]` errored, bash took the `else`
  branch, and `else` was the expected answer.
- The ordering test had the same empty-lookup defect, making an unguarded `-lt` a bash
  error rather than an assertion.
- A fix instruction written mid-flight told an implementer to assert with
  `! command -v vigil`, which is inert.
- The plan had the panel and the nit split in the wrong order, which would have made the
  `pane_width` change meaningless.

**Implementers pushed back on the brief four times and were right every time**, including
twice on tests the brief had written for them. An implementer that reports "I kept the
brief's test and it is vacuous" is doing the job correctly.

**The final whole-branch review earned its cost.** Seven task reviews, each of which did
real mutation work and found real things, all missed the defect that made the feature not
work, because each was scoped to a diff and the defect lived in the interaction between a
detached session's default size and a geometry rule two tasks apart. The lesson is narrower
than "do a final review": a test harness that cannot observe the thing the feature produces
- here, pane geometry - will not catch a defect in it no matter how many reviews run
against it.

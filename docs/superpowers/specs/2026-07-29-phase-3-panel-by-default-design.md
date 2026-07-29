# Phase 3: the panel by default for new sessions

Written 2026-07-29, after phase 2 and the phase 2 blockers merged (`31721d4`) and were
lived on. This is the design for phase 3 of
`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`, which deliberately left
phases 3 through 6 unplanned until phase 2 had been used.

Read alongside:

- `docs/superpowers/2026-07-28-phase-2-blockers-handoff.md` - the debt ledger and the
  landmines. One of its open items is in scope here and is the reason the vigil half of
  this design exists.
- `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md` - the parent design.

## What phase 3 is

New tmux sessions come up with a vigil panel already in them, instead of the user pressing
`prefix p` in each one. Existing sessions are untouched and keep working unpanelled.

The parent design states it as one line: "`create_tmux_session` in `lib/tmux.sh` adds the
panel to new sessions, with a config flag to disable." That is still the shape. This
document settles the decisions that line leaves open and adds one change in `~/vigil` that
phase 3 makes necessary.

## Decisions

**The panel is created eagerly, at session creation.** Not lazily on first attach. The
alternative was a `client-session-changed` hook that panels a session the first time a
client lands on it, which would avoid a vigil process for dispatched sessions that are
never opened and would measure geometry against the client actually displaying the
session. Rejected as more machinery than the saving is worth: `create_tmux_session` gets
one straight-line step instead of the session-creation path and a tmux hook having to
agree with each other.

Note that "detached at creation" is the universal case, not an edge case:
`create_tmux_session` always runs `tmux new-session -d` first, and `run_worktree_popup`
always passes `--detached` to the inner `git-worktree-session` and switches the client
afterwards. Eager creation therefore always runs against a session no client is on yet.

**And that is a sizing problem, not just a measurement one.** A detached session has no
client, so tmux sizes its windows to `default-size` - 80x24, and nothing in
`~/dotfiles/tmux/.tmux.conf` overrides it. A panel is split at an absolute column count, so
`panel_geometry`'s 40 columns, measured against a 350-column client, lands in an 80-column
window and takes half of it. tmux then redistributes panes proportionally when the session
is attached and the panel arrives at ~175 columns. Measured on an isolated server:
`new-session -d` with no `-x/-y` gives an 80x24 window and a 40/39 pane split;
`new-session -d -x 350 -y 90` gives 350x90 and 40/309.

So `create_tmux_session` passes the calling client's dimensions to `new-session` via
`-x`/`-y`, omitting them when there is no client at all. This is separate from, and larger
than, the `panel_geometry` no-client fallback described below: the fallback stops an empty
measurement from crashing, while `-x/-y` is what makes a correct measurement land in a
window it fits. `prefix p` never had either problem, because the window it splits is
already at client size - which is why the toggle, the only path anyone had exercised, gave
no warning of this.

**The panel goes in the `claude` window only.** `create_tmux_session` builds two windows,
`claude` and `server`. The server window keeps its full width for logs. `prefix p` still
adds a panel to any other window on demand, since the toggle is per-window.

**There is no session-persistent panel, because tmux has no session-scoped pane.** Panes
belong to windows in tmux 3.6 and there is no sticky-pane concept. Two things are genuinely
session- or client-scoped and neither is usable:

- The status line persists across every window and can be up to five lines fed by
  `#(command)`. But tmux interprets only its own `#[fg=…]` format escapes and mangles raw
  ANSI SGR, so lipgloss output would need an entire second render backend; and there is no
  cursor and no key routing, so the panel would lose selection and every keybinding.
- Popups own input focus while open, so the pane behind one cannot be typed into.

The workable approximation - an `after-new-window` hook gated on a session-level flag, so
every window a flagged session gains is panelled - was considered and declined. It costs a
vigil process per window rather than per session, and `prefix p` would need a per-window
suppression flag to stop the hook resurrecting a panel the user just killed. Recorded here
so phase 6 does not rediscover it as an idea rather than as a decision.

**The disable flag lives in vigil's config file, read through a new subcommand.** A new
`panel_auto` setting in `~/.config/vigil/config.toml`, defaulting to on, read by bash as
`vigil config get panel_auto`.

The competing option was a tmux user option, which is what `vigil-panel` already uses for
`@vigil_panel_orientation` and `@vigil_panel_size`, with the stated rationale that
"placement is tmux's concern and this script is the only reader". That rationale still
holds for geometry, which is per-client and per-monitor and genuinely tmux-shaped. It does
not extend to *whether the user wants panels at all*, which is a vigil preference and
belongs in the file the user already edits. Geometry stays in tmux options; the on/off
switch goes in the config file.

## The dotfiles side

Repository: `~/dotfiles`, `scripts/scripts/`.

### `add_vigil_panel <window-target>`, new, in `lib/tmux.sh`

Everything `vigil-panel`'s `main` does today except the toggle. Resolve geometry,
`split-window` with `-b -d -P -F '#{pane_id}'` running `${VIGIL_BIN:-vigil} --panel`, then
set `@vigil_panel 1` and `remain-on-exit off` on the returned pane id, pane-scoped.

It takes a window target so it can panel a session that is not the current one, which is
the whole reason it cannot simply be the existing script.

`panel_geometry` moves alongside it, with one behavioural change: when
`#{client_height}`/`#{client_width}` come back empty - no client attached, which is the
headless dispatch case - it returns the `left`/40 branch. Today that path reaches
`$((height * 2))` with an empty string, which is an arithmetic error under `errexit`.

The client query itself is factored out as `client_dimensions`, because
`create_tmux_session` needs the same answer to size the window it will be splitting (see
the sizing note under Decisions). Two copies of that query could disagree about what "no
client" looks like, and a panel sized against one window and split into another is exactly
the defect being fixed.

### `vigil-panel` becomes a thin toggle

Sources `lib/tmux.sh`, keeps `panel_pane`, and `main` reduces to: a marked pane exists →
`kill-pane`; otherwise → `add_vigil_panel` on the current window. The script stops being
self-contained, which is the cost of having one implementation of panel creation.

Its existing bats assertions for `-l` adjacency and the pane-scoped `set-option` move down
onto the shared function, since that is where the behaviour now lives.

### `create_tmux_session` gains a panel step

Immediately after the `server` window is created and **before**
`setup_secondary_pane`:

```bash
vigil_bin="${VIGIL_BIN:-vigil}"
if command -v "${vigil_bin}" > /dev/null 2>&1; then
  if ! panel_auto="$("${vigil_bin}" config get panel_auto)"; then
    warn "vigil config get panel_auto failed"
    panel_auto=""
  fi
  if [ "${panel_auto}" = "true" ]; then
    add_vigil_panel "=${session_name}:claude" || warn "vigil panel failed"
  fi
fi
```

Fail-soft is the point of the guard: a panel that cannot be created must never take the
session with it. An uninstalled or half-installed vigil means no panel rather than a broken
`ts`.

Two details the shorter `[ "$(vigil config get panel_auto 2> /dev/null)" = "true" ]` form
got wrong, and which are worth spelling out because both are silent:

- `2> /dev/null` hides a **present but failing** vigil exactly as thoroughly as an absent
  one. A user mid-upgrade then gets no panel and no explanation, while a failed *split*
  does `warn`. `command -v` separates the two: silent when absent, warned when present and
  erroring.
- The gate must read `${VIGIL_BIN:-vigil}`, because that is what `add_vigil_panel` launches.
  A bare `vigil` decides with a different binary than it runs, and with `VIGIL_BIN` pointing
  at a dev build and no `vigil` on `PATH` the override is inert on the create path while
  still working through `prefix p`.

**`add_vigil_panel` must check its own errors explicitly rather than leaning on
`errexit`.** Both callers - `ts` and `git-worktree-session` - run under `set -o errexit`,
but bash disables it for the whole of a function invoked on the left of `||`. So inside
`add_vigil_panel`, a failing `split-window` would not abort the function; execution would
continue into the `set-option` calls with an empty pane id. The function has to test the
`split-window` result and return non-zero itself.

Session creation is otherwise unchanged in shape - `new-session -d` first, everything set
up while detached, attach or switch last - with the one addition that `new-session` now
carries `-x`/`-y` from `client_dimensions` when there is a client to measure.

### `setup_secondary_pane` measures the pane it splits

It currently reads `#{window_width}` and picks `-h` at 200 or wider. `window_width` does
not shrink when a 40-column panel appears, but the claude pane it actually splits does, so
the threshold would fire at an effective 160. It already resolves `claude_pane` through the
`@vigil_claude` marker, so the change is `display-message -t "${claude_pane}" -p
'#{pane_width}'` in place of the window query. The 200 keeps its current meaning: how wide
the thing about to be split in half really is.

### Ordering

The panel goes in **before** `setup_secondary_pane`, and this is the whole reason the
`pane_width` change above is worth making. The two are one change in two files: the panel
must already be taking its 40 columns at the moment the nit split measures, or the split
measures a pane that is about to be narrowed and the corrected metric reports a stale
number. Panel second would leave `setup_secondary_pane` reading full width and choosing
`-h` at an effective 160 - the exact bug the `window_width` fix exists to remove.

The panel is also before `launch_claude_in_pane`, so `respawn-pane -k` lands on a pane
whose geometry is final.

Resulting order in `create_tmux_session`: `new-session -d` and mark the claude pane →
`new-window server` → panel → `setup_secondary_pane` → `launch_claude_in_pane` →
`select-window`. Splitting the window target `=<session>:claude` splits that window's
active pane, which at that point is the only pane, and `-d` leaves the claude pane active
for the steps that follow.

## The vigil side

Repository: `~/vigil`.

### `vigil config get <key>`

`parseArgs` returns a bare command string today and grows a second return value for the
remaining arguments: `(string, []string, error)`.

The new case dispatches next to `help` and `version` - that is, it returns **before** the
`tmux`/`git`/`gh` `LookPath` check. Reading a config value has no business requiring `gh`
to be installed, and a bash caller receiving exit 1 and "gh not found" instead of a value
would silently disable the panel on any machine mid-setup.

Prints `GetSetting(key)` and exits 0. An unknown key prints nothing and exits 1, so a
caller can distinguish "off" from "not a setting". `printUsage` gains the line.

New setting `panel_auto`, env var `VIGIL_PANEL_AUTO`, default `"true"`, alongside
`auto_cleanup` and `auto_focus`. The env > TOML > default precedence comes from
`GetSetting` unchanged.

### The daemon ownership window

This is in scope because phase 3 is what makes it routine.

`checkStateTransitions` decides effect ownership on `msg.Local` alone
(`internal/model/model.go:1325`): a self-polling client owns effects, a daemon-fed one does
not. A panel that has just spawned a daemon is self-polling *and* about to stop being the
owner. `newModel` spawns the daemon and starts self-polling immediately, while the
reconnect probe only lands at `daemonProbeInterval` plus the dial. During that window both
the fresh daemon and the still-self-polling client own effects for the same event. The
blockers handoff measured `notify=4` for two transitions and `kill-session=2` for one.

Today that requires hand-starting a panel on a machine with no daemon. After phase 3 it
happens on every cold-start dispatch.

**Fix.** A new `effectsDisownedUntil time.Time` field on `Model`, consulted next to `local`
in `checkStateTransitions`. Armed on **any** successful spawn, provided the client has had a
live daemon connection since the last arm.

The proviso is carried by a `daemonSeenSinceArm bool`, set at the two places a working
connection is actually established - `newModel`'s dial and `handleProbeResult`'s live-conn
branch - and cleared on every arm. Never by a spawn attempt merely returning nil.

Arming on the first spawn only was this document's original decision. It covers cold start
but misses daemon restart: a panel whose daemon crashes mid-session respawns one from a
failed probe, does not arm, and both processes then own the same event. For a `Done` event
that is two cross-process `CleanupSession` calls against one worktree, since
`inFlightEffects` is per-process.

The connection gate is what keeps a dead daemon from chaining suppressions.
`handleProbeResult` calls `spawnDaemonOnce` again on each failed probe, but a failed probe
never sets `daemonSeenSinceArm`, so a daemon that spawns and never comes up arms exactly
once; when that single grace period expires the client takes ownership back and keeps it.

An unconditional re-arm would **not** suppress effects "forever". An earlier draft of this
section and of the plan both claimed it would, and both were wrong: `spawnCooldown` is 15s
and `spawnGrace` is 5s, so an unconditional re-arm suppresses about 5 seconds in every 15,
recurring for as long as the daemon keeps failing. A duty cycle, not a permanent silence -
bad, but bounded, and the false version of the argument would have justified the wrong rule
for the wrong reason. The gate earns its place by making the suppression one-shot per
genuine daemon.

Grace of 5 seconds, sized against a healthy reconnect landing at `daemonProbeInterval`
(2s) plus a 300ms dial. It becomes a `var` rather than a `const`, following
`firstSnapshotTimeout` and `daemonProbeInterval`, so tests shorten it instead of sleeping.

Scoped to panels by construction: `newModel` only spawns when `panel` is set, so a plain
`vigil` dashboard is untouched and owns effects from its first poll.

**What this does not fix.** This **narrows** the two-owner window. It does not close it.

Two clients both self-polling with no daemon anywhere still both own effects. That is the
pre-existing N-self-polling-clients case, unchanged by the blockers branch and unchanged
here.

More importantly, the grace arms on *spawn*, but the hazardous state is "self-polling while
a daemon is alive", and those are not the same set. `handleDaemonLost` starts self-polling
and owning effects immediately without arming anything; only a *failed* probe reaches
`spawnDaemonOnce`. So a client that loses its connection to a daemon that is still running
owns effects alongside that daemon until the next probe reconnects. `firstSnapshotTimeout`
is 5s and re-arms on every reconnect, so a daemon whose first snapshot is slow can put a
panel into a repeating connect / timeout / self-poll loop, each lap of which is an
unsuppressed two-owner window. `inFlightEffects` is per-process and cannot help: a `Done`
event landing in one of those windows is two cross-process `CleanupSession` calls against
one worktree.

Closing that properly needs ownership to be asserted rather than inferred - the daemon
telling clients it owns effects, or a lock the owner holds - which is a phase 4+ shape, not
something to bolt onto a timer. Recorded here so the next reader does not mistake the grace
period for a complete answer.

## Failure handling

| Case | Behaviour |
|---|---|
| `vigil` not on `PATH` | `command -v` fails, so no panel and no message. `ts` and dispatch work as today. |
| `vigil` present but erroring | No panel, and `warn "vigil config get panel_auto failed"`. Distinguishing this from the row above is the whole reason for the `command -v` guard. |
| `panel_auto = false` | No panel. `prefix p` unchanged. |
| Session already exists | `create_tmux_session` returns `SESSION_EXISTED` before the panel step, so a re-dispatch never stacks a second panel. |
| No client attached at create time | `new-session` omits `-x/-y` (real tmux rejects an empty `-x` with "width invalid" and creates nothing, so this branch is load-bearing, not cosmetic) and `panel_geometry` falls back to `-hb 40` instead of erroring on empty arithmetic. |
| A client is attached at create time | `new-session` is given the client's `-x/-y`, so the 40-column panel lands in a window that is really that wide and survives the attach at 40 rather than being scaled up. |
| `split-window` fails | Warned; session creation continues and claude still launches. |
| Daemon spawn fails | Existing "could not start daemon" toast, client self-polls. The grace period expires and the client takes ownership of effects back. |
| Worktree removed under a panel | Pre-existing and unchanged. The panel's cwd is the worktree, but `git-worktree-done` kills the session too. The daemon already avoids this with `cmd.Dir = "/"`. |

## Testing

### Dotfiles (bats)

The harness already has most of what this needs: `setup_tmux_stub`, `assert_arg_after`,
`tmux_call_args_matching`, and a `tests/stubs` directory to hold a new `vigil` stub
answering `config get`.

Two harness gaps have to be closed first, and both are the "a stub that ignores its input
caps how much any mutation can prove" lesson from the blockers retro:

- **The tmux stub answers every `display-message` with one canned value.** Once
  `panel_geometry` asks for `#{client_height} #{client_width}` and `setup_secondary_pane`
  asks for `#{pane_width}` in the same run, one value cannot serve both, and a mutant
  asking the *wrong* question would still receive the *right* answer. The stub must key
  its answer on the requested format.
- **No helper exposes call order.** Every existing helper throws position away, so the
  ordering row below is unassertable as the harness stands.

| Property | Assertion |
|---|---|
| `add_vigil_panel` targets the window it is handed | `split-window` argv contains the passed target, not the current window |
| Geometry, portrait | `panel_geometry` prints `-vb 10` |
| Geometry, landscape | `panel_geometry` prints `-hb 40` |
| Geometry, no client dimensions | `panel_geometry` prints `-hb 40` and does not error |
| Size reaches tmux as a size | `add_vigil_panel`'s `split-window` argv has the size adjacent to `-l`, via `assert_arg_after` - a substring check passes even when `-l` is gone and tmux reads the number as the pane command |
| Markers set on the new pane | pane-scoped `set-option` for both `@vigil_panel` and `remain-on-exit`, targeting the returned pane id |
| Panel created when enabled | `create_tmux_session` splits with the `vigil` stub returning `true` |
| No panel when disabled | stub returns `false`; `refute_tmux_subcommand` on the panel split |
| No panel when `vigil` is absent | `PATH` replaced wholesale, not merely stripped of `tests/stubs` - `~/.local/bin` survives a prefix strip and holds a real vigil; session still created and **nothing is warned** |
| A broken `vigil` is warned about | stub exits non-zero; no panel, but `vigil config get panel_auto failed` on the output, which is what separates it from the row above |
| The gate honours `VIGIL_BIN` | no `vigil` on `PATH`, `VIGIL_BIN` at the stub; the panel is created and `split-window` argv carries `${VIGIL_BIN} --panel` as an exact line |
| The window is created at client size | `new-session` argv has `-x`/`-y` adjacent to the client's width and height, via `assert_arg_after` |
| No client, no size flags | neither `-x` nor `-y` appears in `new-session` argv. Counted, not negated: a bare `! … \| grep` mid-test is exempt from errexit and its status is discarded |
| Panel failure does not abort | `split-window` stub fails; session exists and `respawn-pane` still ran |
| `setup_secondary_pane` measures the pane | its `display-message` asks for `#{pane_width}` and targets the claude pane, not the window |
| Ordering | the panel split is recorded **before** the nit split, and both before `respawn-pane` |
| Toggle still toggles | existing `vigil-panel` tests, against the refactored script |

### Vigil (go, `-race`)

`main.go` has no seam today, so the dependency-check ordering is not testable as written.
Extract `run(args []string, stdout, stderr io.Writer) int` and reduce `main` to a call to
it. Small, and it turns the ordering from a comment into an assertion.

| Property | Assertion |
|---|---|
| `config get` precedes the dependency check | `run([]string{"config","get","panel_auto"})` with `gh` removed from `PATH` writes the value and returns 0. It also needs its own `HOME`: `ConfigPath` resolves under `os.UserHomeDir`, so without one the test reads the developer's real config and a `panel_auto = "false"` there - the documented way to turn this feature off - turns the suite red |
| Unknown key | exit 1, nothing on stdout |
| `panel_auto` default | `GetSetting` returns `"true"` with no config |
| Env and TOML overrides | standard `GetSetting` precedence |
| Disowned inside the grace window | counting `EffectRunner`: a panel that spawned a daemon runs 0 effects for a transition |
| Owned after it expires | same fixture, grace shortened; effects run |
| **A repeated failed probe does not extend the deadline** | respawn with `daemonSeenSinceArm` false; the deadline is unchanged |
| **A respawn after a real disconnect does re-arm it** | respawn with `daemonSeenSinceArm` true; the deadline moves and the flag is cleared |
| Both setters of `daemonSeenSinceArm` are reached through production paths | `newModel`'s dial and `handleProbeResult`'s live-conn branch each have their own test, so deleting either one is caught by exactly one of them |
| A dashboard owns effects immediately | non-panel model, effects run on the first transition |
| A dashboard never arms the deadline | `New` leaves `effectsDisownedUntil` zero, which is the control for the arming test above |

The two bolded rows pin the arming rule from both sides. Neither alone is enough: the first
alone is satisfied by arm-once, the second alone by an unconditional re-arm. The third row
exists because a fixture that sets `daemonSeenSinceArm` by hand leaves both of its real
setters untested - a defect that was found and fixed twice on this branch, once for
`effectsDisownedUntil` and again for the flag that gates it.

### Real-machine verification

The blockers handoff's lesson applies directly: prime deliberately, then transition,
because a test producing no event proves nothing about a system that only acts on events,
and "nothing happened" looks identical to "the feature is broken".

- [ ] Dispatch a story from cold with no daemon running. One panel appears, one daemon
      starts, and the first real transition fires the `notify` hook **once**, not twice.
      This is the ownership fix observed rather than inferred.
- [ ] `panel_auto = false` produces an unpanelled session and nothing else differs.
- [ ] Eyeball the nit split at the real terminal width with 40 columns gone. That
      threshold change is the one thing here that is felt daily.
- [x] The panel is 40 columns wide after the client lands on the new session, not a
      proportional share of it. Done on an isolated `tmux -L` server driving the real
      `create_tmux_session` with a real 350x90 pty client: 175 columns before the `-x/-y`
      change, 40 after. The stub cannot show this - it records argv and has no layout.

## Out of scope

- The `x` and batch `x` paths still do not participate in `inFlightEffects`. Named in the
  blockers handoff as the last live instance of that branch's headline bug. Judged not
  worth doing now: it needs `auto_cleanup` enabled and a keypress inside the cleanup
  window.
- The per-branch review-comment cache still caches a failed fetch as a settled empty
  result.
- `internal/protocol`'s permanent version-mismatch error, which would give the ownership
  window an unbounded form the first time `Version` changes. Still latent; `Version` is 1
  on both sides.
- Phases 4 through 6. Unchanged: do not plan them until phase 3 has been lived on.

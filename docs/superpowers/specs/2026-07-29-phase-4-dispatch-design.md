# Phase 4: dispatch through `vigild`

Written 2026-07-29, after phase 3 merged as `a785fb1` here and `fefeeb1` in `~/dotfiles`.
This is the design for phase 4 of the cockpit plan
(`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md:145`), which specified:

> Add `vigil dispatch <url-or-id>`, which submits a job to the daemon. The daemon runs the
> existing scripts and streams status back to vigil's existing notification overlay.
>
> The menu bar calls `vigil dispatch` instead of the popup tunnel. The standalone `dispatch`
> CLI keeps working unchanged for direct terminal use.

Read `docs/superpowers/2026-07-29-phase-3-handoff.md` first. This design depends on two of
its findings: the effect-ownership race it says is narrowed rather than closed, and the
80x24 headless-window landmine it says is still open. Phase 4 is the case that landmine
fires in.

Like phase 3, this spans **two repositories** and neither half works without the other.

## The workflow this exists to fix

The user clicks a menu bar button (`dispatch-bar`, a Swift `NSStatusItem`). That runs
`dispatch-from-chrome`, which reads the active Chrome tab's URL, finds the most recently
attached tmux session, activates iTerm2, and opens a `tmux display-popup -E` running
`dispatch --non-interactive <url>` inside that session. `dispatch` routes to
`shortcut-implement` or `gh-review`, which fetch the story or PR, create a worktree and a
tmux session, launch Claude in it, and `switch-client` the user into it.

Everything from the popup inward stays. The popup itself goes.

What the popup costs today:

- It requires an attached client. With none, `dispatch-from-chrome` opens an iTerm tab,
  attaches it to a session, and polls for up to 5 seconds waiting for the client to exist.
- It hijacks the screen for the ~60 seconds the scripts take (`short` fetch,
  `shortcut-claim`, `classify_story` - an LLM call - then the worktree and session).
- It needs `DISPATCH_IN_POPUP=1` so `run_worktree_popup` runs inline instead of opening a
  nested popup inside the popup.
- The work is owned by the popup's process. Dismiss it and the dispatch dies.

Phase 6 lists "the popup tunnel inside `dispatch-from-chrome`" as a deletion. This is the
phase that earns it.

### The state of dispatch inside vigil

`vigil` already has a `d` key: it opens a text input and `action.Dispatch` runs the
configured `dispatch` hook through `cfg.RunHook` with a **15-second timeout**, inside the
vigil process. `classify_story` alone exceeds that. So the in-vigil path almost certainly
cannot complete a real dispatch today, and if it ever did, the work would be owned by a
pane that can close. Phase 4 replaces it rather than adding beside it.

## Decisions

Four, taken during design.

**The teleport is preserved.** After clicking the button you still end up in the new
session's `claude` window, because that is the workflow. It is the constraint that shapes
most of the rest of this design: `switch-client` with no `-c` needs a current client, and
`vigild` has no tty.

**In-flight jobs are carried in the snapshot, not in toasts.** The overlay the parent
design named gives every notification a 3-second expiry (`addNotification`) and renders
only the newest unexpired one (`activeNotification`). Streaming a 60-second job into that
produces a few flashes separated by silence, and a terminal failure that lands while the
user is looking away is gone. Jobs instead live in the daemon and ride in `Snapshot`, so
every client renders the same persistent line, a restarting panel does not lose it, and a
failure can be retained deliberately.

**`vigil dispatch` spawns a daemon if none is running.** It reuses the path panels already
use. The alternative - failing with "no daemon" - makes the menu bar button do nothing
useful on a machine with no sessions open, which is a state the current script handles.

**Jobs are serialized, one at a time.** Two concurrent `git worktree add` calls in one
repository contend on the index lock, and dispatches arrive one button-click at a time.
Queued jobs are visible as `queued`.

## Architecture

```
dispatch-bar (Swift)
  └─ dispatch-from-chrome           reads the Chrome tab URL, activates iTerm2 or
     │                              attaches one so a client exists
     └─ vigil dispatch --cwd ~/portal <url>
          ├─ validates the input, generates a job id
          ├─ dials vigild.sock; spawns `vigil daemon` and retries once if absent
          ├─ writes one Request frame
          └─ waits for the next Snapshot; exits 0 once its id appears in Jobs
                                        (this is the ack - no response frame exists)

vigild
  ├─ reader goroutine per client       accepts Request frames
  ├─ job queue                         one job at a time; dedups on input
  └─ job goroutine
       ├─ resolves the most recently active tmux client
       ├─ runs the `dispatch` hook: cwd = job cwd, VIGIL_CLIENT exported,
       │                            output streamed line by line
       ├─ each line updates job.Status
       └─ exit status sets succeeded / failed
                                        │
  poll loop (every tmux_interval) ──────┴─→ copies jobs into Snapshot.Jobs, broadcasts

every panel and dashboard
  └─ one line below the table:  ⚡ sc-12345 · classifying for routing…
```

Out of scope, deliberately: Chrome-tab reading stays in `dispatch-from-chrome`; the
standalone `dispatch` CLI's terminal behavior is unchanged; the pickable work queue is
phase 5.

### Why request frames on the existing socket

The socket is broadcast-only today - the daemon writes `Snapshot` frames, clients write
nothing, and each client has a single write-only goroutine.

Two alternatives were considered and rejected. **A second control socket** leaves the
snapshot path untouched, at the permanent cost of a second listener, a second stale-socket
path, and a duplicated lock story. **A spool directory** needs no protocol change and
survives a daemon restart, but it is filesystem-as-IPC with fsync and partial-write
discipline forever, and it cannot grow into phase 5.

Phase 5 is the work queue: `vigild` polls assigned stories and review-requested PRs, and
both vigil and the menu bar present a pickable list that dispatches on select. That is more
daemon→client data and more client→daemon commands. The protocol becomes bidirectional
either way; this does it once, in the place it belongs.

## Protocol

Both additions are additive.

```go
// Clients write these. The daemon never does.
type Request struct {
    Version int    `json:"version"`
    Type    string `json:"type"`   // "dispatch"
    ID      string `json:"id"`     // client-generated
    Input   string `json:"input"`
    Cwd     string `json:"cwd"`
}

type Job struct {
    ID      string `json:"id"`
    Input   string `json:"input"`
    State   string `json:"state"`   // queued | running | succeeded | failed
    Status  string `json:"status"`  // last output line, or the failure reason
    Started int64  `json:"started"`
    Ended   int64  `json:"ended"`
}

type Snapshot struct {
    // ...existing fields
    Jobs []Job `json:"jobs,omitempty"`
}
```

`protocol.Version` stays **1**. Direction disambiguates the two frame types, so no envelope
is needed, and adding a field to a JSON struct is compatible in both directions: an old
panel ignores `jobs`, and a new panel against an old daemon sees nil. Bumping the version
would instead push every not-yet-reinstalled panel onto `ErrVersionMismatch` and into
self-polling, for no gain.

**The snapshot is the ack.** `vigil dispatch` submits, then reads snapshots until its job id
appears, for at most 5 seconds. There is no response frame type, which means a rejection is
visible in every panel rather than only to the CLI.

`vigil dispatch` is fire-and-forget: **exit 0 means accepted, not succeeded.** It does not
wait out the job, because the whole point is that the job outlives it. A job the daemon
refuses is still registered under its id, in state `failed` with the reason as its status -
a duplicate submit, or a `Request.Version` the daemon does not understand. That keeps the
ack mechanism working for refusals, lets the CLI print the actual reason and exit non-zero,
and puts the refusal in every panel too. The only case with no job at all is a daemon that
never read the frame.

That case is the one skew case that matters, and it is covered for free: a new
`vigil dispatch` against a **pre-phase-4 daemon** writes into a socket nobody reads, no job
id ever appears, the 5-second wait expires, and the CLI exits non-zero with

> daemon did not accept the job; it may be running an older vigil

That is what the first upgrade will show if a daemon from before phase 4 is still alive.
It is the same "loud once" situation phase 3's `config get` gate created, and the message
has to name the cause because the fix (`make install`, restart the daemon) is not guessable
from a timeout.

## Ownership and concurrency

The daemon's design is a concurrency claim, which is why `-race` is not optional in the
Makefile. Three rules:

1. **Jobs never run on the poll goroutine.** `poll` is synchronous per tick; a job executed
   there would freeze every panel's snapshot stream for the length of a dispatch. Jobs run
   on their own goroutine, tracked by a `WaitGroup` that `Run` waits on before returning,
   exactly as `pendingEffects` already does for transition effects.
2. **The job table is copied under a mutex once per tick.** A job goroutine writes `Status`
   while the poll goroutine marshals the snapshot. `Snapshot.Jobs` is built from a copy
   taken under the job mutex, so no `Job` value is ever shared with a running goroutine.
3. **The connection now has two owners.** `client.writeLoop` currently closes the
   connection on exit. Adding a reader means a reader at EOF can race a writer mid-`Encode`.
   The reader signals through the existing `done` channel and never closes the connection
   itself; the writer stays the sole closer.

### Job lifecycle

`queued` → `running` → `succeeded` | `failed`.

One job runs; the rest sit `queued`. A submit whose `Input` matches a job already `queued` or
`running` is refused - registered under its own id as `failed`, not silently dropped - so a
double-click cannot produce two worktrees for one story. (`shortcut-implement` already
handles a session that exists, via `SESSION_EXISTED`, but two concurrent jobs would both run
before either session existed.)

Succeeded jobs drop out of the snapshot after 10 seconds. Failed jobs are retained for 10
minutes so a failure cannot expire unseen, and the reason also goes to `vigild.log`, which
is already where daemon-side effect failures go. There is no dismiss keybinding: phase 5
adds request types anyway, and one can be added then if the retention window proves wrong.

### Streaming subprocess output

`fetch.Commander.Run` buffers and returns at exit, so it cannot feed a live status line.
Adding a method to `Commander` would break every fake in the suite, so `fetch` gains a
segregated interface that `ExecCommander` also satisfies:

```go
type StreamCommander interface {
    RunStream(ctx context.Context, dir, name string, args []string, onLine func(string)) error
}
```

`config` gains `RunHookStream`, sharing `ExpandHook` and the `sh -c 'exec 2>&1; …'`
construction with `RunHook` through one unexported helper, so the two cannot drift on
quoting or on the stderr merge - which is load-bearing, since `warn` and `error` write to
stderr.

Status lines are read tolerantly: strip ANSI, then strip a leading `>>> ` or `!!! ` if
present. `lib/output.sh` writes those prefixes, but this is a soft read of its format, not a
contract. An unrecognized line still displays, with its prefix intact.

## The client travels as an environment variable

Exploring `~/dotfiles` turned up the finding that shapes the dotfiles half.

`client_dimensions` (`lib/tmux.sh:109`) is `tmux display-message -p '#{client_height}
#{client_width}'` with no `-c`, so it measures the *calling* client. A daemon-run job has
none, and two things follow:

- `create_tmux_session` gets no `-x/-y`, so the window is `default-size` 80x24, and a
  40-column panel in an 80-column window arrives at roughly 175 columns when a wide client
  attaches. This is verbatim the landmine the phase 3 handoff left open: *"the 175-column
  balloon still happens in the genuinely headless case: server up, no client, session
  created, then a wide client attaches."* Phase 4 is that case, on every dispatch.
- `panel_geometry` takes its no-client branch and forces `left`, so a portrait monitor gets
  a 40-column column instead of the `-vb 10` top strip it should get.

Separately, `shortcut-implement:135` guards on `is_in_tmux`, which is `[ -n "${TMUX:-}" ]`.
A daemon child has no `$TMUX`, so today's script would refuse the job outright with "Not
running in tmux".

So a client identity has to reach the scripts, and it is load-bearing for three things: the
switch, the window size, and the panel orientation.

**It travels as `VIGIL_CLIENT`, exported by the daemon into the job's environment, empty
when nothing is attached.** The alternative - a `--client` flag - would have to thread
through `dispatch` → `shortcut-implement` → `run_worktree_popup` → `git-worktree-session` →
`create_tmux_session` → `panel_geometry`, five levels, one of which re-quotes its arguments
into a command string with `printf '%q'` and runs them through `bash -c`. An environment
variable crosses that boundary for free, `CLAUDE_PROMPT_FILE` and `DISPATCH_IN_POPUP` are
existing precedent for the pattern, and it keeps the hook template to one placeholder.

## Changes by repository

### `~/vigil`

- **`protocol`**: `Request`, `Job`, `Snapshot.Jobs`. Version stays 1.
- **`daemon`**: a reader goroutine per client; a serialized job queue; jobs on their own
  goroutines under a `WaitGroup`; the job table copied under a mutex per tick; the client
  resolved per job; `VIGIL_CLIENT` exported into the job environment.
- **`fetch`**: `StreamCommander`, implemented by `ExecCommander`. `MostRecentClient`,
  alongside the existing `MostRecentSession`.
- **`config`**: `RunHookStream`. A `dispatch_timeout` setting, default 300s, replacing the
  hardcoded 15s.
- **`main.go`**: `vigil dispatch [--cwd <path>] <url-or-id>`. Unlike `config get`, this
  belongs **after** the `tmux`/`git`/`gh` `LookPath` check - it genuinely needs all three,
  and a missing dependency is worth reporting before a job is queued.
- **`model`**: the job line, rendered below the table in both panel and dashboard mode,
  truncated to width, present only while a job exists. `d` submits to the daemon on the
  same path the CLI uses, which is what removes the 15-second timeout. `action.Dispatch`
  and its tests are deleted; its input validation (non-empty, ≤500 characters, no control
  characters) moves to the submit path, where it guards the daemon rather than one client.
- **`d` needs a cwd**, and a panel's cwd is a worktree, not the repository a new worktree
  should be cut from. It resolves the main worktree from the selected session's git root via
  `git rev-parse --git-common-dir`, falling back to the process cwd.
- **`spawnDaemon` strips `TMUX` and `TMUX_PANE` from the child environment.** It currently
  inherits them from the panel that spawned it, so the daemon carries one pane's tmux
  identity for its entire life - stale the moment that pane dies, and enough to make
  `is_in_tmux` lie. Same class of bug as the existing `cmd.Dir = "/"` guard, and worth
  fixing regardless of what the scripts check.

### `~/dotfiles`

- **`client_dimensions [client]`** honors `VIGIL_CLIENT`, so the window and the panel are
  both sized against the client the user will actually be switched to. Its existing comment
  about two callers needing to agree with each other still holds and now covers a third
  reason to keep it factored.
- **A `switch_client_to <target>` helper** in `lib/tmux.sh`, replacing the raw
  `switch-client` calls, passing `-c` when `VIGIL_CLIENT` is set. It never falls back to
  `attach-session`. `create_tmux_session:315` attaches when not in tmux, and a daemon child
  that attaches would block until the job timeout. The dispatch path passes `--detached` so
  it does not reach that line today, but it is one flag away from doing so.
- **`is_in_tmux` in the two workflow scripts** becomes a server-reachability check rather
  than a `$TMUX` check. `is_in_tmux` itself stays for the callers that genuinely mean "am I
  running inside a tmux client".
- **`dispatch-from-chrome`** loses the popup tunnel, the URL temp file and
  `DISPATCH_IN_POPUP`, and calls `vigil dispatch --cwd "$HOME/${repo}" "$url"`. It **keeps**
  the iTerm activate-or-attach branch: with no client attached anywhere it still opens an
  iTerm tab on the most recent session, so a client exists for the teleport to land on.
  Dropping that would silently lose the teleport in the case it matters most.
- **`DISPATCH_IN_POPUP` is renamed `DISPATCH_INLINE`**, because "in popup" stops describing
  the case that sets it - the daemon is not a popup - and it moves into the hook string,
  where the user's configuration already lives:

  ```toml
  dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {input}"
  ```

  `--detached` drops off, which is what lets the teleport happen at all.

## Error handling

| Failure | Behavior |
|---|---|
| No daemon at submit | Spawn `vigil daemon`, wait for the socket, retry once, then fail naming the socket path. |
| Daemon never acks within 5s | `vigil dispatch` exits non-zero: "daemon did not accept the job; it may be running an older vigil". Covers protocol skew. |
| Malformed input | Rejected client-side before submitting; never reaches the daemon. |
| Duplicate submit | Registered as a `failed` job, reason "duplicate of an in-flight dispatch". The CLI reads that back and exits non-zero. |
| Unknown `Request.Version` | Same shape: a `failed` job naming the version, so the refusal is legible rather than a silent drop. |
| `dispatch` hook unset | Job fails immediately with `HookNotConfigured`; reason in the job line and in the log. |
| Job exits non-zero | `failed`, last `!!!` line as the reason, retained 10 minutes, logged. |
| Job exceeds `dispatch_timeout` | Killed; `failed` with "timed out after 300s". |
| No client attached | `VIGIL_CLIENT` empty; the job runs and the session is created, with no switch. |
| Daemon killed mid-job | The job dies with it, leaving the same half-made worktree a dismissed popup leaves today. |

## Testing

Go tests carry most of the weight, and `-race` is the point of several:

- A `Request` round-trip; a snapshot carrying `Jobs` still decoding against a struct without
  the field, and one without `Jobs` decoding to nil against a struct with it.
- The ack contract: submit, and the job appears in the next snapshot.
- A refusal - duplicate, and unknown request version - arrives as a `failed` job rather than
  as nothing, and the CLI reports its reason rather than the skew message.
- Polling continues while a job blocks. This is what proves jobs are off the poll goroutine,
  and it fails if rule 1 is violated.
- Concurrent broadcasts against a job goroutine mutating `Status`, under `-race`.
- Serialization: two submits, the second stays `queued` until the first ends.
- Dedup rejection of an identical in-flight input.
- Failure capture: reason, retention window, log line.
- A reader hitting EOF does not kill its writer, and the connection is closed once.
- `vigil dispatch` spawns an absent daemon and then submits.
- `d` and the CLI reach the same submit path.

On the bats side, one limit has to be stated rather than discovered: **the tmux stub returns
a constant `pane_width` and cannot observe real geometry.** That blind spot is what hid
phase 3's 175-column defect through seven per-task reviews. bats can assert that
`client_dimensions` passes `-c` when `VIGIL_CLIENT` is set, that `switch_client_to` does the
same, and that `dispatch-from-chrome` invokes `vigil dispatch` with the right `--cwd`. It
cannot show that a dispatched session comes out the right size.

That check needs the real-tmux method phase 3 used - an isolated server, a `tmux` shim on
`PATH`, an isolated `HOME` and `XDG_RUNTIME_DIR`, and a real client of known dimensions -
and it must not be skipped, because it is the assertion that phase 4 has not reintroduced
the balloon through the back door.

Real-machine verification list:

- A dispatched session comes out at the target client's size, with the orientation that
  client's aspect ratio implies.
- The teleport lands in the new session's `claude` window.
- The job line appears in a panel and updates as the script progresses.
- A failing dispatch leaves a `failed` line for its retention window and a log entry.
- A dispatch with nothing attached anywhere runs and creates the session, without switching.
- `d` inside vigil does the same thing as the menu bar button.

## What this does not fix

**Phase 4 makes the effect-ownership race more likely to bite.** The phase 3 handoff names
the scenario precisely: `firstSnapshotTimeout` is 5 seconds and is re-armed on every
reconnect, so *"a daemon whose first `Snapshot` exceeds it - git plus `gh` across many
sessions on a cold dispatch, which is precisely the phase-3 scenario - can put a panel in a
repeating loop of connect, timeout, self-poll, reconnect"*, and `inFlightEffects` is
per-process, so a `Done` landing in one of those laps is two `CleanupSession` calls against
one worktree.

Phase 4 does two things that push on exactly that. A dispatch adds a session, and every new
session gets a panel, so both the snapshot cost and the client count grow. And
`vigil dispatch` can now spawn the daemon, which means a cold daemon's first snapshot can
be triggered by a dispatch rather than by a panel. This design does not close the race -
the real fix is asserted ownership, not a timer - but it should not be merged without
someone deciding whether the timer holds under it. Raising `firstSnapshotTimeout` is a
mitigation, not a fix, and it trades against how long a panel waits before falling back.

**A job dies with the daemon.** The job is a child of `vigild` because capturing its output
is the whole point of the status line, and a `setsid`'d grandchild cannot be watched. This
is not worse than dismissing today's popup, but it is a new way to reach a half-made
worktree.

**No dismiss key.** A failed job occupies its line for 10 minutes. If that proves wrong,
phase 5's request types are the place to add a dismissal.

**The status line is only as good as the scripts' output.** `>>> ` lines are informative
because someone wrote them that way, not because anything enforces it. A script that goes
quiet for 40 seconds shows a stale line for 40 seconds.

**Nothing here helps a dispatch from outside tmux with no server running.** A client is
resolved if one exists; `dispatch-from-chrome` creates one if it can. Neither is the same as
handling a genuinely headless machine, which remains out of scope for the same reason phase
3 left it out: a headless creator has no dimensions to pass.

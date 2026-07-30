# Binary refresh and job dismissal

Design for the two items `docs/superpowers/2026-07-30-phase-4-handoff.md` deferred under
"Deferred, with the user's decision on each". Both were hit on the first day of real use of
phase 4, neither is speculative, and the handoff records that phase 5 makes the first one
strictly worse: the work queue adds more daemon-to-client data, so a client running an old
binary silently misses more.

This work lands **before** phase 5, for that reason.

## The shared defect

Both items are the same failure in different clothes: **something the user needs to see does
not appear, and the absence looks exactly like nothing having happened.**

- A `make install` leaves every running panel on the old image. `Snapshot.Jobs` is additive
  by design, so an old panel ignores the key rather than erroring. The user dispatches a
  story, the job runs correctly, and no job line appears anywhere.
- A failed or refused job line is retained ten minutes and cannot be cleared. The only way
  out was restarting the daemon, which drops the in-memory job table.

Neither is fixed by making the system louder. They are fixed by giving the user a way to
act, and by making staleness a thing that is stated rather than inferred.

## Part A: dismissing a finished job

### Protocol

One new request type. `protocol.Version` stays **1**, on the same argument that kept `Jobs`
at 1: this is additive, and the daemon already has a defined behaviour for a request type it
does not recognize.

```go
const RequestDismiss = "dismiss"
```

The client sends `Request{Version: protocol.Version, Type: RequestDismiss, ID: ""}`.

**The empty `ID` is load-bearing and is not an oversight.** `jobs.submit` returns silently on
`req.ID == ""`, before its reason switch. So a new client pressing the dismiss key against an
*old* daemon is a silent no-op, which is the correct outcome. Give the frame a real ID and
the old daemon takes the unknown-type branch and registers `unsupported request type
"dismiss"` as a **refused job** - a fresh red line, undismissable for ten minutes, produced
by the very key meant to clear one. The empty ID is what makes the wrong-way-round staleness
harmless instead of self-defeating.

### Daemon

`daemon.go`'s request case (currently an unconditional `s.jobs.submit(req)` at the
`case req := <-s.requests:` arm) routes on `req.Type`:

| `req.Type` | Action |
|---|---|
| `RequestDismiss` | `jobs.dismissTerminal()` |
| anything else | `jobs.submit(req)`, exactly as today |

Routing the default to `submit` rather than to a refusal is deliberate: `submit`'s reason
switch already produces the "unsupported request type" refusal, and that behaviour must not
move or change.

`jobs.dismissTerminal()` takes `j.mu` and deletes every job in `JobFailed` or `JobRefused`
from `byID`, `order` and `cwds`. It returns whether anything was removed; the caller
publishes immediately - the same `publishJobs` the submit path uses, for the same reason -
and skips the broadcast when nothing changed.

**Queued and running jobs are untouched.** Cancelling in-flight work is a different feature
with different hazards (a half-made worktree, a process group to signal) and is out of scope.
**Succeeded jobs are untouched too**: they age out in ten seconds on their own, so a key for
them would be a key for nothing.

There is **no ack frame**. The job disappearing from the next snapshot is the ack, which is
the same contract dispatch already has.

### Client

`esc` already runs a cascade at `model.go`'s `keys.Cancel` case: it unwinds one layer of
state per press and quits when there is nothing left to unwind. Dismissal joins it as a new
layer, ahead of the quit:

```
confirm prompt / multi-selection / dispatch prompt   (existing, unchanged)
  → dismiss failed and refused jobs                  (new)
    → quit                                           (existing)
```

The dismiss layer is entered only when `m.jobs` actually contains a job in `JobFailed` or
`JobRefused`. That gate is what keeps esc-to-quit working the rest of the time.

**The behaviour change, named:** while a failure line is on screen, esc no longer quits on the
first press. This is the cascade doing what it already does for a confirm prompt, but it is a
change and it is the one thing about this feature a user could be surprised by.

**The self-polling path needs no special case.** `m.jobs` is only ever populated from a
snapshot, so a client with no daemon has no jobs, the gate is false, and esc falls through to
quit exactly as it does today.

### Writing the frame

The frame goes out on `m.daemonConn`, the client's existing daemon connection. This is what
the bidirectional socket was built for: the daemon's per-connection `readLoop` reads requests
from the same connection it writes snapshots to. No dial, no daemon spawn, no ack timeout,
and it is guaranteed to reach the daemon that sent the job in the first place - which
`dispatch.Submit`, dialing fresh, is not.

Two constraints on the write:

- **It happens in a `tea.Cmd`, not in `Update`.** A write to a socket with a live reader will
  not block in practice, but "in practice" is not a guarantee, and freezing the update
  goroutine on a wedged daemon is the failure this codebase has already paid for once.
  A short write deadline bounds it.
- **A mutex guards writes to `daemonConn`.** A `*sync.Mutex` on the model, so the value
  copies Bubble Tea makes all share one. `net.Conn` is safe for concurrent read and write,
  but two concurrent *writes* can interleave into one malformed frame. The daemon tolerates
  that - `ErrMalformedRequest` is recoverable by construction - but the dismiss would
  silently not happen. Phase 5 adds more client-to-daemon writes, so this seam is worth
  having before there are three callers rather than one.

### Retention is unchanged

`failedRetention` stays at ten minutes. With a key to clear it, a long window stops being a
problem and becomes the feature it was meant to be: a failure that happened while you were
looking elsewhere is still there when you look back.

## Part B: a client that notices its binary changed

### The stamp

At startup each process resolves `os.Executable()` and stats it, recording **size and
modification time**. That pair, not `main.version`: the version string comes from
`git describe --tags --always --dirty`, which is identical across two consecutive dirty
builds, so it cannot see the change that matters most during development.

Stat'ing the path is the right signal because of how `make install` works - it renames a new
file over `~/.local/bin/vigil` rather than writing in place, so the running process keeps its
old inode while the path resolves to the new file. The same check catches `make build`
overwriting `./vigil` in place.

**Failure means "unchanged", never "changed".** A failing `os.Executable`, a failing stat, or
a path that has been deleted all leave the feature dormant. It fails closed.

### Cadence

The check piggybacks on the existing per-tick message, on both the daemon-fed and the
self-polling path, rate-limited internally to roughly ten seconds. No new tick generation,
and therefore nothing to do with the epoch machinery that exists because Bubble Tea ticks
cannot be cancelled.

### Re-exec

The exec cannot happen inside `Update`. Bubble Tea owns raw mode and the alt screen there,
and a process exec'd from inside it inherits a terminal nobody restored. So:

1. The model sets a restart flag and returns `tea.Quit`.
2. `p.Run()` returns and Bubble Tea restores the terminal.
3. The caller inspects the returned model and `syscall.Exec`s the same path with the same
   `os.Args` and `os.Environ()`.

The exec goes through an injectable seam rather than calling `syscall.Exec` directly, so a
test can reach that branch without replacing the test binary with a second copy of vigil.
This matters for `run(args []string, stdout, stderr io.Writer) int`, which exists precisely
so this kind of thing stays assertable.

### Deferral

The re-exec waits while a confirm prompt, a dispatch prompt, or a multi-selection is open -
all three are unsaved user intent, and all three are already the things the esc cascade
protects. Panel mode has none of these states, so panels restart promptly, which is the case
that actually bit.

No indicator for a deferred restart. The states that defer it are all states the user is
actively in and about to leave.

### Anti-loop guard

A re-exec'd process stamps at its own startup, so a stable binary never fires twice: that is
the primary defence and it is structural. Against a stat that is somehow nondeterministic,
a floor - **no re-exec within 30 seconds of this process's own start**. That converts a
pathological loop from a hot spin that makes a panel unusable into one exec per 30s, which is
survivable and diagnosable.

## Part C: a daemon that is visibly, not silently, old

**The daemon is not restarted, not re-exec'd, and not touched.**

The rejected alternative and why it is rejected is worth recording, because it was the
original design and it was wrong. The daemon would notice its own stamp change and, with no
job in flight, run its normal shutdown - clients closed, `pendingEffects` waited on, socket
unlinked, flock released - and then either let the next client probe respawn it, or hand off
by calling `daemon.Spawn()` itself after fully releasing the lock. (The handoff ordering is
forced: `acquireLock` is `LOCK_EX|LOCK_NB`, so a successor spawned while the old daemon still
holds the lock exits immediately with `ErrAlreadyRunning`.)

Both variants were rejected for the same reason: **`closeClients` drops every connection, so
every panel bounces through daemon-lost and back on every install.** Even a sub-second
handoff produces that stutter. Trading silent staleness for a visible stutter on every
install is a bad trade, and it puts new lifecycle code into the area where the phase 4
Critical showed lifecycle bugs cost the most - a wedged daemon cannot be restarted at all.

Instead, the daemon's staleness becomes something the client can state.

`Snapshot` gains one additive field carrying the stamp the daemon recorded at **its own**
startup. Same version-stays-1 argument as `Jobs`: an old daemon omits it, a new client reads
the zero value. A daemon startup stamp that differs from what is on disk now means the daemon
is running an older image.

The comparison costs nothing new. Part B already stats the binary on each rate-limited check
and the client caches that result; Part C reads the same cached value rather than stat'ing
again. The comparison is therefore against **what is on disk**, not against the client's own
startup stamp - so in the seconds between an install and the client's own re-exec, both are
correctly reported as behind.

`Model.daemonHealth()` gains a third case at the **lowest** precedence, after `no daemon` and
`daemon stale Ns`:

```
daemon outdated
```

**An absent stamp counts as outdated.** This is not a compromise: a daemon too old to send
the field is too old. It fires once on the upgrade that introduces this feature, until the
daemon is restarted, and then never spuriously again.

No key to restart the daemon. Killing it is already sufficient, because a client respawns it
on the next probe through the existing `spawnDaemonOnce` path. The marker is the whole
feature.

### The tradeoff, stated

The daemon keeps running its old image until the user restarts it, so daemon-side changes -
the job runner, transition effects, phase 5's pollers - do not take effect at install time.
What changes is that this is now visible and therefore a decision, rather than something
discovered four minutes later when a job line never appears.

## What this deliberately does not do

- **No cancel for a running job.** Dismissal only touches terminal states. Cancelling means
  signalling a process group mid-dispatch and owning the half-made worktree it leaves, which
  is the same debris a dismissed popup left before phase 4.
- **No dismissal of succeeded jobs.** Ten seconds of retention needs no key.
- **No daemon restart, automatic or keyed.** Part C above.
- **Nothing in `make install`.** Putting tmux pane knowledge in the build file was considered
  and rejected: it only covers installs done through make, and the runtime check is needed
  anyway for the daemon-was-restarted-but-panels-were-not case.
- **`ExecCommander.Run`'s missing `WaitDelay` is still not fixed.** It remains the phase 4
  handoff's top landmine and is out of scope here.

## Testing

The phase 4 handoff's process note governs: **a test is not evidence until it has been seen
to fail.** Every test below is written against a deliberately broken implementation first, or
mutated after the fact, and the mutation is recorded.

**Part A**

- `dismissTerminal` removes failed and refused jobs and leaves queued, running and succeeded
  ones. Mutation: make it remove everything, and make it remove nothing.
- The daemon routes a `dismiss` frame to `dismissTerminal` and every other type to `submit`.
  Mutation: delete the route; the unknown-type refusal test must still pass, which is what
  proves the two paths are independently pinned.
- A `dismiss` frame with an empty ID reaching `submit` (i.e. an old daemon) registers no job
  at all. This is the test that pins the reason the ID is empty, and it belongs with the
  protocol rather than with the daemon.
- The esc cascade: with a confirm prompt open esc clears the prompt and does not dismiss;
  with only a failed job present esc dismisses and does not quit; with neither, esc quits.
  Mutation: reorder the layers.
- A client with no daemon connection and no jobs quits on esc.

**Part B**

- The stamp comparison: unchanged size and mtime does not trigger; either one changing does.
- Stat failure and `os.Executable` failure both leave it dormant. Mutation: invert the guard.
- The restart flag is set and `tea.Quit` returned, and the exec seam is called with the
  original path, argv and environ. The seam is what makes this assertable at all.
- Deferral: with a confirm prompt, a dispatch prompt, or a multi-selection open, a changed
  stamp does not set the flag.
- The 30-second floor suppresses a re-exec from a freshly started process.

**Part C**

- `daemonHealth` returns `daemon outdated` when the snapshot's stamp differs from the
  on-disk stamp, and returns `no daemon` and `daemon stale Ns` in preference to it.
- An absent stamp reads as outdated.
- A snapshot round-trip carries the new field, and a snapshot encoded without it decodes to
  the zero value at version 1. This is the additive-field claim and it must be tested, not
  asserted in a comment.

`make test` is `go test -race ./...` and `-race` is not optional: the mutex added in Part A
is a concurrency claim.

## Landmines this creates

- **A dismiss racing a `vigil dispatch` ack.** The CLI waits up to 15s for its job id to
  appear in a snapshot. Dismissal only touches terminal states, and a job the CLI is still
  waiting on is queued or running, so the race is closed by construction rather than by
  timing. If dismissal is ever extended to succeeded jobs, this reopens: a success dismissed
  within the ack window makes the CLI report failure for work that succeeded.
- **A re-exec drops the dashboard's cursor, filter and sort.** They are not persisted. For a
  panel this is invisible; for a dashboard it is a small, real cost paid at install time.
- **`daemon outdated` depends on the daemon and the client resolving the same path.** A
  daemon spawned from a different binary than the client is running - a `./vigil` in a
  worktree, say - will read as outdated forever. That is arguably true, but it is a
  confusing way to be told.

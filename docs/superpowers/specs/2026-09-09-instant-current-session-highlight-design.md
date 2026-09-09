# Instant current-session highlight

## Status: designed, not implemented

Approved 2026-09-09. The user's complaint was a "slight lag in vigil when
changing tmux sessions", with one constraint stated up front: **not by checking
more often than vigil already does.** This design adds zero polls.

## The problem, traced

Three facts, each read from the code rather than assumed:

1. **`annotateClientFlags` (`internal/model/client.go:114-124`) re-resolves which
   session is current from tmux state on every arriving snapshot.** It runs
   `fetch.CurrentSession` and `fetch.LastSession` - two `tmux display-message`
   calls - and sets `IsCurrent` / `IsLast` on every session in the list. `newModel`
   also sets `IsCurrent` once from the session cache at startup (`model.go:271`).
   (corrected 2026-09-09 after tracing to code)
2. **It only runs when a snapshot arrives.** On the daemon-fed path that is
   `listenDaemonCmd`'s closure, re-issued by `Update` on every `SnapshotMsg`;
   on the self-polling path it is inside `collectCmd`.
3. **The daemon broadcasts one snapshot per `tmux_interval`, default 1s.**

So the highlight can only move on a snapshot boundary. The floor is one tick,
and the ceiling is worse: a poll that runs long delays the broadcast behind it,
and this machine's daemon log carries `slow poll` lines of 1.0-1.6s on freshly
dispatched worktrees.

`Snapshot` carries **no** current-session field - its fields are `Version`,
`Timestamp`, `Sessions`, `Jobs`, `Queue`, `QueueHidden`, `DaemonBin`. That is
deliberate and stays that way: `client.go:126-129` records that which session is
current or last "belongs to this tmux client rather than to the daemon". A
daemon-side answer would be wrong for any client that is not the most recently
active one.

`Model.currentSessionName` is written once, in `newModel` (`model.go:229`), and
never updated. It is only the fallback for when `CurrentSession` returns empty.
It is **not** the source of the rendered highlight, and this design does not
change it.

## What this changes

tmux's `client-session-changed` hook fires exactly when a switch lands. It runs
`vigil poke`, which writes one frame to the daemon socket. The daemon
**rebroadcasts the snapshot it already holds**. Every connected client's
`listenDaemonCmd` returns, re-runs `annotateClientFlags`, and repaints.

The key economy: rebroadcasting does not poll. No tmux metadata is re-read
daemon-side, no git runs, no `gh` runs, and the remote pollers are not nudged -
so "one daemon means one `gh` budget" is untouched, and the tickerless poller
design is not disturbed. The only new work per session switch is one socket
write plus the two `display-message` calls each client already makes per
snapshot.

Because a poke never polls, it also cannot be *slower* than waiting for the
tick, which the rejected alternative below could be.

**Deliberately not fixed:** a session created or destroyed elsewhere still
appears or disappears on the normal 1s tick. Only the highlight is immediate.
The session list barely changes on a switch - the session you switched to
already existed - so making the list instant buys nothing for this complaint.

## Architecture

### protocol

Add one constant:

```go
const RequestPoke = "poke"
```

`Version` stays **1**. This is additive in exactly the sense
`Snapshot.Jobs`, `Snapshot.Queue` and `Request.Detached` already are.

The frame is sent with an **empty `ID`**, following the `RequestDismiss`
precedent. `jobs.submit` (`internal/daemon/jobs.go:97-100`) opens with
`if req == nil || req.ID == "" { return }`, so an **old** daemon that has never
heard of `"poke"` registers no refused job nobody can dismiss. Without the
empty ID, upgrading vigil while an old daemon is still running would litter
every panel's job line. **The frame itself is not dropped**: `handleRequest`'s
`default` arm still falls through to the unconditional `publishJobs` call at
the bottom of the handler, so an old daemon rebroadcasts its held snapshot
just as a new one does - verified 2026-09-09 against a live pre-feature
daemon binary, which answered a poke with a second snapshot carrying the
identical timestamp and session list and zero jobs. (corrected 2026-09-09,
verified against a live pre-feature daemon)

`RequestDecoder.Next` still must not reject an unknown version; that rule is
unchanged and untouched here.

### daemon

**Corrected 2026-09-09, while writing the implementation plan.** This section
originally specified a new `repaint` method. That was redundant, and the
correction is recorded here rather than silently dropped, because a reader
comparing this spec to the shipped code needs to know which is right: the code
is.

`handleRequest` (`daemon.go:388-404`) already ends in an **unconditional**
`s.publishJobs(s.jobs.snapshot())`, and `publishJobs` (`daemon.go:352-382`) is
already the rebroadcast primitive. So the whole daemon change is one arm that
does nothing but let control reach the call that is already there:

```go
switch req.Type {
case protocol.RequestDismiss:
    if !s.jobs.dismissTerminal() {
        return
    }
case protocol.RequestPoke:
    // Nothing to change: the rebroadcast below is the whole point. An
    // explicit arm rather than falling through `default`, which would reach
    // the same rebroadcast only by way of submit dropping an empty ID - a
    // coupling that would break silently if either end moved.
default:
    s.jobs.submit(req)
}
```

Reusing `publishJobs` inherits three properties that a `repaint` would have had
to reimplement, and therefore to keep in sync:

- **It never invents a snapshot.** Nil `latest` means no successful poll has
  happened yet; a frame with nil `Sessions` would blank every client's table,
  which is far worse than a highlight arriving one tick late.
- **It does not refresh `Timestamp`.** The carry-over is deliberate because a
  rebroadcast must not make a stalled collector's data look fresh. `Snapshot.Timestamp`
  currently has no client-side reader, so the carry-over matters only to the daemon's
  own reasoning about data freshness. (corrected 2026-09-09 after tracing to code)
- **It is documented as Run's-goroutine-only**, which is what makes touching
  `clients` safe. `broadcast` (`daemon.go:511-525`) mutates `s.clients`
  unguarded, and `daemon.go:56-57` states `clients` is owned by Run's
  goroutine. `Run`'s select loop contains
  `case req := <-s.requests: s.handleRequest(req)` (`daemon.go:209-210`), so
  `handleRequest` already runs there. No channel, no mutex, and no interaction
  with the synchronous poll.

`publishJobs` attaches the current job list to the copy it broadcasts. For a
poke that list is unchanged, so the effect is a rebroadcast of identical data.

**A poke requires a jobs runner, and that is accepted.** `handleRequest` opens
with `if s.jobs == nil || req == nil { return }`, so a poke is a no-op on a
`Server` with no jobs runner. That guard protects a real nil dereference in
`publishJobs(s.jobs.snapshot())`, and `New` always wires a runner - only a bare
`Server` literal in a test lacks one. Restructuring the guard to serve a
configuration that never occurs in production would mean touching code whose own
comment says "that behaviour must not move", in order to pin an unreachable
branch. Not done, and no test for it.

### client

**No changes.** This is the point of the design. On the daemon-fed path, `listenDaemonCmd` reads one
snapshot per invocation, `Update` re-issues it on every `SnapshotMsg`, and
`annotateClientFlags` runs inside that closure (`client.go:142`). On the self-polling path,
`annotateClientFlags` runs inside `collectCmd` (`client.go:94`). An extra arriving snapshot is
already a full re-resolution of `IsCurrent` / `IsLast`.

A self-polling client gets no benefit from a poke, and that is accepted. It has no daemon to
poke, and adding a client-side listener would be a second mechanism for a case
that barely exists now that every mode spawns a daemon.

### CLI

`vigil poke`: resolve the socket, dial, write one `Request`, exit 0.

- **Silent**, always exit 0 even with no daemon listening. A tmux hook must
  never emit noise into a pane or fail a switch.
- **No ack wait.** `vigil dispatch` waits for its job id to appear in a snapshot
  because a drop is indistinguishable from a daemon that never read; a poke has
  no such contract - a lost poke costs one tick of staleness.
- **Does not spawn a daemon**, unlike `dispatch`. A hook that fires on every
  session switch must not be able to start processes.

Dispatched in `main.go` alongside `dispatch`. It needs no `LookPath` check for
`tmux`/`git`/`gh`, so like `config get` it should run before that gate.

### dotfiles

```tmux
set-hook -g client-session-changed 'run-shell -b "vigil poke >/dev/null 2>&1 || true"'
```

`-b` backgrounds it so a switch is never delayed behind the socket write.
Fail-soft so **tmux navigation still works on a machine with vigil
uninstalled** - the standing rule that also keeps `tmux-hop` free of any vigil
reference. The hook is not in `tmux-hop` and must not be moved there.

## Rejected alternatives

**An immediate real poll on poke.** More correct in that a session created
elsewhere would also appear at once, but it does tmux and git work on every
session switch, and on a cold worktree this machine's own logs put that at
1.0-1.6s - so the "fast path" would sometimes be slower than waiting for the
tick it replaced. It also has to avoid running concurrently with the
synchronous per-tick poll, which the rebroadcast shape sidesteps entirely.

**SIGUSR1 to every vigil process.** No protocol change and the lowest possible
latency, since the client re-annotates without a round trip. Rejected on a
footgun: **the default action for an unhandled SIGUSR1 is to terminate the
process**, and the daemon is the same `vigil` binary. Any version skew - an
older daemon still running after `make install`, which this repo's design makes
normal since the daemon never restarts itself - means the hook kills the daemon
on the next session switch. It also needs `pkill` by name and new Bubble Tea
external-message plumbing that does not exist.

**Putting current/last in `Snapshot`.** Does not reduce latency by itself, and
contradicts the recorded reason those are client-side: a daemon's answer is the
most recently active client's answer, which is wrong for every other client.

## Testing

`-race` throughout. Every test gets a mutation check - break the subject, watch
it fail, restore, paste the output - per this repository's standing warning about
tests that pass with their subject deleted.

- `protocol`: a poke frame round-trips; `Version` is still 1.
- `daemon`: a poke rebroadcasts `latest` to a connected client.
- `daemon`: a poke **before the first successful poll** broadcasts nothing. The
  mutation is deleting the nil-`latest` return, which must blank the client.
- `daemon`: a poke registers no job and no refusal.
- `daemon`: a poke **runs no poll** - zero additional tmux or git calls on the
  mock Commander. This is the test that pins the whole economy of the design.
- `daemon`: a poke does not refresh `Timestamp`.
**The explicit `case protocol.RequestPoke:` arm has no behavioural signature,
and no test should claim to pin it.** Deleting that line drops a poke into
`default`, where `submit` discards the empty ID and control still reaches the
same rebroadcast - identical observable behaviour. The arm is a readability and
robustness choice: it stops the poke path depending on two unrelated pieces of
code continuing to line up. Writing a test that appears to cover it would be
another entry in this repository's list of tests that pass with their subject
deleted. It is covered by review, not by the suite - the same defence the
tickerless remote pollers rely on.

What the "registers no job" test *does* pin is the **empty-ID contract**: give
the poke a generated ID and it becomes a refused job on every session switch
against an old daemon. That is the landmine below, and it is testable.
- `model`: a repeat identical snapshot produces **no duplicate toasts**. The
  detector compares against previous state and should return no events, but a
  poke makes repeat snapshots routine for the first time, so this is worth
  pinning rather than reasoning about.

The dotfiles half is a `tmux.conf` line, not a script, so it has no bats
coverage. It is verified by hand: switch sessions and watch the highlight.

## Landmines

- **A poke against an old daemon must register no refused job, not one on
  every switch.** The empty `ID` is what does that, via a guard in
  `jobs.submit` rather than anything in the poke path. Someone "tidying up" by
  giving the poke a generated id would reintroduce undismissable refused jobs
  on every session switch during a version skew. This does **not** mean an old
  daemon does nothing with the frame - it still rebroadcasts, via the
  unconditional `publishJobs` call `handleRequest`'s `default` arm falls
  through to. (corrected 2026-09-09, verified against a live pre-feature
  daemon)
- **The poke must stay on Run's goroutine.** `publishJobs` reaching `broadcast`
  is only safe because `handleRequest` is called from Run's select loop.
  Handling a poke on a reader goroutine, or making the poke path asynchronous
  to "avoid blocking the loop", races on `s.clients` - and `-race` will only
  catch it if a test has a client connected.
- **The hook must stay fail-soft and out of `tmux-hop`.**

## Documentation

- `README.md`: `vigil poke` under Usage, and a line in the panel-outside-tmux
  section about the highlight being event-driven.
- `CLAUDE.md`: a Key Conventions bullet - what the poke does and does not
  refresh, why it rebroadcasts rather than polls, the empty-ID reason, and the
  Run's-goroutine constraint.
- `~/dotfiles/CLAUDE.md`: the hook, and why it is a hook rather than part of
  `tmux-hop`.

## Files

vigil:

- `internal/protocol/protocol.go` - `RequestPoke`
- `internal/daemon/daemon.go` - one `handleRequest` switch arm; no new method
- the poke client - reusing `internal/dispatch`'s dial, or a minimal sibling
- `main.go` - subcommand dispatch
- `README.md`, `CLAUDE.md`
- tests in `internal/protocol`, `internal/daemon`, `internal/model`

~/dotfiles:

- `tmux/.tmux.conf` - the hook
- `CLAUDE.md`

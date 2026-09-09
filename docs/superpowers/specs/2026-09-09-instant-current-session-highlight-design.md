# Instant current-session highlight

## Status: designed, not implemented

Approved 2026-09-09. The user's complaint was a "slight lag in vigil when
changing tmux sessions", with one constraint stated up front: **not by checking
more often than vigil already does.** This design adds zero polls.

## The problem, traced

Three facts, each read from the code rather than assumed:

1. **`annotateClientFlags` (`internal/model/client.go:114-124`) is the only
   thing that decides which session is current.** It runs
   `fetch.CurrentSession` and `fetch.LastSession` - two `tmux display-message`
   calls - and sets `IsCurrent` / `IsLast` on every session in the list.
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
heard of `"poke"` drops the frame instead of falling through to its
unsupported-type arm and registering a refused job nobody can dismiss. Without
the empty ID, upgrading vigil while an old daemon is still running would litter
every panel's job line.

`RequestDecoder.Next` still must not reject an unknown version; that rule is
unchanged and untouched here.

### daemon

A new arm in `handleRequest`:

```go
case protocol.RequestPoke:
    s.repaint()
```

`repaint` mirrors `publishJobs` (`daemon.go:352-382`) and inherits two of its
decisions for the same reasons:

- **It never invents a snapshot.** Nil `latest` means no successful poll has
  happened yet; a frame with nil `Sessions` would blank every client's table,
  which is far worse than a highlight arriving one tick late.
- **It does not refresh `Timestamp`.** That field is what the status bar's
  `daemon stale Ns` reads. These sessions are exactly as old as they were, and
  refreshing it would make a stalled collector look healthy.

Unlike `publishJobs` it does not copy the snapshot, because it changes nothing
in it. Rebroadcasting the same pointer is safe: each client has its own writer
goroutine and a one-deep latest-wins queue.

**Why no new concurrency machinery.** `broadcast` (`daemon.go:511-525`) mutates
`s.clients` unguarded, and `daemon.go:56-57` states that `clients` is owned by
Run's goroutine. `Run`'s select loop contains
`case req := <-s.requests: s.handleRequest(req)` (`daemon.go:209-210`), so
`handleRequest` **already runs on Run's goroutine**. A poke arm may therefore
call `broadcast` directly. No channel, no mutex, no interaction with the
synchronous poll - which is the whole reason this shape was chosen over an
immediate poll.

**The one trap.** `handleRequest` opens with
`if s.jobs == nil || req == nil { return }`. A poke needs no jobs runner, so
that guard would silently turn it into a no-op on a `Server` without one - which
is what a bare `Server` literal in a test gets. The poke arm must sit **ahead**
of that guard. Moving or loosening the guard itself is out of scope: the
`handleRequest` doc comment records that "the default arm stays submit rather
than becoming a refusal ... and that behaviour must not move." A test pins the
no-jobs-runner case rather than a code reading.

### client

**No changes.** This is the point of the design. `listenDaemonCmd` reads one
snapshot per invocation, `Update` re-issues it on every `SnapshotMsg`, and
`annotateClientFlags` runs inside that closure. An extra arriving snapshot is
already a full re-resolution of `IsCurrent` / `IsLast`.

A self-polling client gets no benefit, and that is accepted. It has no daemon to
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
- `daemon`: a poke works on a `Server` with **no jobs runner**, pinning the
  `s.jobs == nil` guard trap.
- `daemon`: a poke does not refresh `Timestamp`.
- `model`: a repeat identical snapshot produces **no duplicate toasts**. The
  detector compares against previous state and should return no events, but a
  poke makes repeat snapshots routine for the first time, so this is worth
  pinning rather than reasoning about.

The dotfiles half is a `tmux.conf` line, not a script, so it has no bats
coverage. It is verified by hand: switch sessions and watch the highlight.

## Landmines

- **A poke against an old daemon must be inert, not refused.** The empty `ID` is
  what does that, via a guard in `jobs.submit` rather than anything in the poke
  path. Someone "tidying up" by giving the poke a generated id would reintroduce
  undismissable refused jobs on every session switch during a version skew.
- **`repaint` must stay on Run's goroutine.** It is only safe because
  `handleRequest` is called from the select loop. Calling it from a reader
  goroutine, or making the poke path asynchronous, races on `s.clients`.
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
- `internal/daemon/daemon.go` - `handleRequest` arm, `repaint`
- the poke client - reusing `internal/dispatch`'s dial, or a minimal sibling
- `main.go` - subcommand dispatch
- `README.md`, `CLAUDE.md`
- tests in `internal/protocol`, `internal/daemon`, `internal/model`

~/dotfiles:

- `tmux/.tmux.conf` - the hook
- `CLAUDE.md`

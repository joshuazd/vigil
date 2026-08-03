# Collector async remote: taking network calls off the publication path

Written 2026-07-31, before phase 5. This is the structural change
`docs/superpowers/2026-07-31-poll-latency-handoff.md` deferred and argued was worth more
before phase 5 than after.

## The problem

`Collector.Snapshot` is synchronous end to end:

```
ListSessions -> BellFlags -> fillGit -> fillPRs -> return
```

Nothing is published until all of it returns. `fillPRs` shells out to `gh`, twice per due
branch, against a rate-limited network API. One slow call stalls every panel's view of
everything, including data that was already in hand before the call started.

Two consequences, both measured or read off the code rather than guessed:

- The no-PR retry ladder cost ~4.5s per poll on every freshly dispatched session until
  `ec70904`. That fix removed the worst instance. It did not change the shape, and the next
  instance - a slow network, a rate-limited `gh`, a hung DNS lookup - has the same cost.
- `poll` runs **inline in `Run`'s `select` loop**. So a slow `gh` does not only delay
  publication; for its whole duration the daemon accepts no new client connections and
  handles no dispatch requests. A panel starting during a slow poll waits it out, and a
  `vigil dispatch` submitted during one is not acknowledged until it ends.

Phase 5 adds two more network pollers - assigned Shortcut stories and review-requested PRs.
Added inside `Snapshot` the way PR fetching is, they make the session list slower for
reasons that have nothing to do with sessions.

## What this changes

`Snapshot` becomes local-only and returns in roughly the time `fillGit` takes. Off-box data
is fetched by background workers and read out of a store on the next call. Publication is
never behind a network call again.

Not in scope: `fillGit`, `ListSessions` and `BellFlags` stay synchronous and stay inside
`Snapshot`. They are local subprocesses. `fillGit` is also the last lock-free
goroutine-owned memo in the collector, and moving it would spend that for no latency.

**Superseded on that last point, then partly restored.** The real-machine verification measured
`fillGit` at 3.0 to 3.5 seconds - 99.7% of `Snapshot` - dominated by `git status --porcelain`
on active portal worktrees, so "not the latency source" was wrong on this machine; see the
`fillGit` finding in `docs/superpowers/2026-07-31-collector-async-remote-handoff.md`.

A 2026-08-03 re-measurement on the same machine got **0.138s cold and 0.009s warm, with the
memo skipping every poll**. The attribution held - `status --porcelain` is still effectively all
of `fillGit` - but the magnitude did not reproduce, and the gap is not fully explained. So the
sentence above is right again today and was wrong on 2026-07-31, which makes it conditional on
the repository rather than either true or false. `git status --porcelain` is the one call that
can make it wrong, and the daemon now logs a `slow poll` breakdown when it does. Full account:
`docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md`.

## Architecture

### The poller seam

`internal/collect/remote.go` (new) defines the one abstraction:

```go
// A poller owns one class of off-box data: its own store, its own locking, and
// its own idea of what is due. Nothing it does can block Snapshot.
type poller interface {
	pass(ctx context.Context)
	invalidate()
}
```

`pass` runs one fetch cycle and returns quickly when nothing is due. `invalidate` drops
due-ness so the next pass refetches.

Today there is exactly one implementation, `prPoller`, holding the mutex-guarded successor
to `prMemo`. Phase 5 adds `storyPoller` and `reviewRequestPoller` as siblings and does not
touch `Snapshot` to do it. That is the point of the seam: the thing phase 5 needs to add is
a poller, not a stage in a pipeline.

A poller's session-facing methods are its own, not the interface's. `prPoller` has `track`
and `fill` because PR data is keyed by branch and root, which comes from the session list;
phase 5's two pollers are global lists and will have neither. So `Collector` holds the
runner (for `nudge`, `Start`, `Wait`, `invalidate`) *and* a typed `prs *prPoller` field that
`Snapshot` calls directly. The interface carries only what scheduling needs.

### Scheduling

`Collector` gains:

```go
func (c *Collector) Start(ctx context.Context)          // one goroutine per poller
func (c *Collector) Wait()                              // joins them
func (c *Collector) RefreshRemote(ctx context.Context)  // one synchronous pass of every poller
```

One goroutine **per poller**, not one shared worker: a slow poller must not delay another,
and phase 5's Shortcut API has no reason to be behind `gh`.

`RefreshRemote` is the synchronous seam. The workers are a scheduler over it, and tests
drive it directly rather than racing a goroutine.

### The workers have no tickers, and that is load-bearing

Each worker blocks on a cap-1 wake channel. Cadence comes from whoever calls `Snapshot`,
which nudges at the end of every call; per-key intervals (`pr_interval`) are enforced inside
`pass`.

The reason is not simplicity. **A daemon-fed client never calls `Snapshot`** - `startPoll`
refuses while `daemonConn != nil` - so its workers block forever and it spends no `gh`
budget. That is exactly the property the daemon exists to provide: one `gh` budget regardless
of how many panels are on screen. A ticker inside the remote layer would restore per-panel
polling for every open panel, silently, and only one test would notice.

### Snapshot

```
ListSessions -> BellFlags -> fillGit     (local, unchanged, synchronous)
groupByBranchRoot
prs.track(branches)                      (working set, latest-wins)
prs.fill(sessions)                       (locked read: sets PR, or PRPending)
remote.nudge()                           (coalesced, non-blocking)
return
```

`track` before `nudge`, so the woken pass sees the current working set. `pass` prunes its
store to that set, which is where today's per-`Snapshot` memo rebuild goes.

The error path returns early without nudging: with no session list there is no working set
to post.

## Pending

`session.Session` gains:

```go
PRPending bool `json:"pr_pending,omitempty"`
```

Additive, so `protocol.Version` stays **1**: an old panel ignores the key, a new one sees
false against an old daemon.

It is true only when the branch has **no store entry at all**. A session with no branch or
no git root is never pending - it can never have a PR, and marking it pending would skip it
in the detector forever.

### Why it exists

`transition.Detector` seeds a session it has not seen without emitting an event
(`!seen -> continue`). Today the daemon's first poll is complete, so the seed is at the
session's true state. Async PR fetching breaks that: tick 1 would seed every session
PR-less, and tick 2 - when the data lands - every session with a PR transitions. On the
daemon that is a burst of `notify` hooks on every daemon start, and for an already-merged
session a `-> Done` event, which is the one event that runs `auto_cleanup`.

`Detector.Detect` therefore skips a session where `PRPending && PR == nil`: no seed, no
event. The first observation that seeds it is a true one.

The `PR == nil` half matters on the client: `Model`'s `prCache` fills the last known PR for
a branch before transitions are checked, so a client with history is never skipped.

### What it costs, stated plainly

A session whose state genuinely changes inside the pending window loses its `notify` hook.
The window is process start, and the first pass of a newly created session's life. A merged
session that a bell flips through `Attention` during that window is the realistic case.

That is the trade against firing a burst of hooks, and `auto_cleanup`, on every daemon
restart. It is not close.

### Rendering

A pending session renders as `Idle`, since `State()` reads `PR == nil`. The table and the
sort order flicker once at cold start and settle within a tick. No view change: a dim
placeholder is a second thing to keep true, for a condition that is visible for one tick.

## Lifecycle

`Start(ctx)`'s context is the owner's - `Run`'s in the daemon, `m.ctx` in the Model. Both
call it unconditionally; idleness on the daemon-fed path is guaranteed by the nudge rule
rather than by a gate.

The daemon's shutdown arm calls `s.Collector.Wait()` alongside `s.pendingEffects.Wait()`, so
`Run` does not return - and does not release the flock or unlink the socket - with a `gh`
child still running. Same pattern as effects, for the same reason.

## Error handling

Semantics are unchanged, relocated:

- A failed `gh` keeps the last known PR for that branch and still moves `fetchedAt`, so a
  rate-limited `gh` is not retried on every nudge.
- A branch whose **first** fetch fails gets an entry with `pr == nil`, so `PRPending` goes
  false and it seeds as `Idle`. That is exactly today's behaviour for a failed first poll.
- A poller goroutine panicking takes the process down, as `fillPRs`' fan-out does today. No
  new recover, no new behaviour.
- `Invalidate` clears the git memo as it does now (goroutine-owned, no lock) and calls
  `invalidate` on every poller, then nudges. A forced refresh therefore lands a tick later
  than it does today rather than inside the same call. `r` is silently refused on a
  daemon-fed client either way.

## Testing

Three that carry the change, each to be watched fail first:

1. **`Snapshot` issues zero `gh` invocations.** Recording commander, assert on argv. This is
   the entire point of the change and nothing else pins it.
2. **Cold start runs no effects.** Daemon starts against a session with a `MERGED` PR, PR
   resolution delayed one pass; assert `EffectRunner.Run` is never called. This is the
   `auto_cleanup`-on-restart regression the pending flag exists to prevent.
3. **A daemon-fed client spends no `gh` budget.** `Start(ctx)`, never call `Snapshot`,
   assert no `gh` invocation after a bounded wait. Pins the no-ticker property, which is
   otherwise invisible and easy to undo.

Then:

- `PRPending` true on the first `Snapshot`, false after a `RefreshRemote` and a second one.
- Per-branch `pr_interval` gating survives the move into `pass`.
- A failed fetch keeps the last known PR and does not re-mark the branch pending.
- A branch that leaves the working set is evicted from the store.
- `Invalidate` causes a refetch inside `pr_interval`.
- `-race`: a `Snapshot` loop concurrent with a `RefreshRemote` loop.
- `Detector` skips a pending session, seeds silently on the next complete observation, and
  never marks a branchless session pending.
- The daemon accepts a connection and handles a dispatch request while a pass is in flight.

## Landmines

- **The absence of tickers in the remote layer is load-bearing.** Adding one restores
  per-panel `gh` polling for every daemon-fed client, which is the cost the daemon exists to
  avoid. Only test (3) would fail.
- `RefreshRemote` is reached in production only through the worker, so the tests that drive
  it directly do not cover the scheduling at all. A `nudge` that never reached a worker
  would leave every `RefreshRemote`-driven test green; only the daemon-level tests, which go
  through a real `Start`, would catch it. Those are the ones to keep honest.
- The pending skip costs at most one `notify` hook per session, at daemon start only. If a
  user reports a missing notification right after a restart, this is why.
- **An old panel against a new daemon bursts toasts on daemon start.** It does not know
  `pr_pending`, so it seeds PR-less and reacts to the fill. Self-limiting now that panels
  re-exec, but per the binary-refresh handoff **the first install after this lands cannot
  re-exec anything** - a panel must already be running the feature to notice.
- `pr_pending` is written into the session cache. Additive, `omitempty`, and overwritten a
  tick later; the Python-era cache reader ignores unknown keys.

## Files

- `internal/collect/remote.go` - new: the `poller` seam, `prPoller`, the wake plumbing.
- `internal/collect/collect.go` - `Snapshot` loses `fillPRs`, gains `track`/`fill`/`nudge`;
  `Start`, `Wait`, `RefreshRemote`; `Invalidate` forwards to the pollers.
- `internal/session/session.go` - `PRPending`.
- `internal/transition/transition.go` - the skip in `Detect`.
- `internal/daemon/daemon.go` - `Start` in `Run`, `Wait` in the shutdown arm.
- `internal/model/model.go` - `Start` in `newModel`.

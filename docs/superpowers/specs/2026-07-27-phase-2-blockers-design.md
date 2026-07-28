# Phase 2 blockers: design

Written 2026-07-27. Closes the three items the phase 2 handoff lists under "Must be
resolved before phase 3 ships", plus one adjacent gh-budget reduction that lives in the
same code.

- Handoff: `docs/superpowers/2026-07-27-phase-2-handoff.md`
- Parent design: `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`

Phase 3 makes the panel the default for new sessions, which turns N attached clients from
exotic into normal. Two of these three blockers are only latent because N is usually 1.

## Scope

1. State-transition side effects fire once per attached client instead of once per event.
2. `internal/collect` and the TUI's self-polling implement the same job twice.
3. `colIndex` and `colState` reserve 2 columns where the renderers emit 1.
4. The review-threads GraphQL query fetches comment bodies on every poll that only the
   detail panel ever reads.

Explicitly out of scope: the `visibleLen` display-width bug. It counts runes rather than
display columns, so a CJK or emoji session name still overflows a pane, and the width
tests assert against the same broken metric. It needs a width library and it is not a
phase 3 blocker.

## Correction to the handoff

The handoff and the parent spec both claim the review-thread GraphQL call is "only
consumed by the detail panel's review-comments mode" and that making it lazy would
"roughly halve the daemon's cost". Both are wrong, and the fix below is scoped to what is
actually true.

The call returns two things:

- `UnresolvedComments`, a count, which drives `session.State() == Unresolved`
  (`internal/session/session.go:145`), the `☐ N` badge (`internal/view/format.go:202`),
  the state dot, auto-focus, and state-transition notifications. Needed for every session
  on every cycle.
- `ReviewComments`, the bodies, read only by `internal/view/detail.go:199`.

The call therefore cannot be skipped without every session losing the Unresolved state.
What can be dropped is the inner `comments` connection. That leaves the call count
unchanged and cuts the nodes requested per query by roughly six times; GitHub scores the
GraphQL limit on nodes requested, so the budget does fall, but this is not a halving.

Halving the call count would mean reimplementing what `gh pr view --json` returns as a
hand-written GraphQL query merged with `reviewThreads`. Considered and declined: it means
maintaining our own reproduction of `gh`'s `mergeable` and `statusCheckRollup` handling.

## Decisions

**Side effects belong to whoever owns the poll loop.** The daemon when a client is
connected to one; the self-polling client when there is no daemon. The alternative -
daemon-only - either regresses a plain `vigil` dashboard to no hooks and no auto-cleanup,
or forces the dashboard to spawn a daemon, reversing the deliberate phase 2 choice that
only panels do so. Duplication survives only in the window where the daemon is down and N
panels self-poll, which is bounded by the 2s probe that respawns one, and is no worse than
today.

**Auto-cleanup failures go to the daemon log, not to clients.** The snapshot protocol
stays a pure state feed. Its one-deep latest-wins queue drops frames by design, so an
event channel layered on it would silently lose events - worse than a log line. The daemon
log is already the place failures go and is deliberately silent when healthy.

**The collapse converges both paths on one message.** Rejected: extracting shared fetch
primitives while keeping the model's three tick cadences, which narrows the drift surface
rather than removing it and does not close the blocker. Also rejected: deleting
self-polling entirely, which contradicts "both paths are permanently supported".

## Blocker 1: transition ownership

New package `internal/transition`. Detection has to be shared, or blocker 1 recreates
blocker 2 in a new place.

```go
type Event struct {
    Session, PanePath, Branch, GitRoot string
    Old, New                           session.SessionState
}

type Detector struct { prev map[string]session.SessionState }

// Detect returns one Event per session whose state changed. The first call
// primes and returns nil. Sessions absent from the input are pruned, so a
// recreated session primes rather than fires.
func (d *Detector) Detect(sessions []*session.Session) []Event

// RunEffects runs the notify hook and auto_cleanup for one event.
func RunEffects(ctx context.Context, cfg *config.Config, cmd fetch.Commander, ev Event, logf func(string, ...any))
```

`Detect` is pure state comparison: no config, no side effects, no toasts. Priming on the
first call replaces the `initialLoad` flag currently threaded through four call sites.

`transition` imports `session`, `config`, `action` and `fetch`. Neither `action` nor
`config` imports `transition`, so there is no cycle.

Three policies over two consumers:

| Consumer | Detector | Toasts + auto-focus | Hooks + auto_cleanup |
|---|---|---|---|
| Daemon poll loop | one | no | yes |
| Client, daemon-fed | one per client | yes | no |
| Client, self-polling | one per client | yes | yes |

Toasts and auto-focus stay per-client and unchanged: each panel has its own screen and its
own cursor.

Whether a client owns the loop is carried on the message as `SnapshotMsg.Local`, not
derived from inspecting `m.daemonConn` at the point of use. That makes the condition
explicit at the call site and testable without a socket.

`checkStateTransitions` in `internal/model/model.go` shrinks to: call `Detect`, add a
notification per event, call `RunEffects` per event when `Local`, then auto-focus.

## Blocker 2: collapse self-polling onto collect

`model.New` builds a `*collect.Collector` alongside the daemon dial. Fallback becomes one
command:

```go
func (m Model) collectCmd() tea.Cmd  // Snapshot → annotate IsCurrent/IsLast → SnapshotMsg{Local: true}
```

self-scheduled from `handleSnapshot` when `Local` and the epoch still matches. Exactly one
poll is ever in flight, which is what keeps `Collector`'s memos single-goroutine - they are
not safe against a reentrant `Snapshot`. No ticker in fallback mode.

The `IsCurrent`/`IsLast` annotation moves out of `listenDaemonCmd` into a helper both
paths call. It is per-tmux-client on either path and is the one thing the daemon cannot
know.

Deleted from `internal/model`:

- `fetchTmuxCmd`, `fetchGitCmd`, `fetchPRsCmd`
- `TmuxUpdatedMsg`, `GitUpdatedMsg`, `PRUpdatedMsg` and their handlers
- `gitTickCmd`, `prTickCmd`
- `initialPRDone`, `initialLoad`, `gitCache`

Kept: `prCache` and `warmCaches`, as the client-side PR backstop the handoff documents.
`cache.Save` moves from `handleGitUpdated` to `handleSnapshot`, guarded on `Local`; the
daemon already writes the cache from its own poll, so writing on both paths would be a
duplicate write of identical data.

`handleDaemonLost` drops from six commands to two: `collectCmd` and `probeTickCmd`.

Accepted behaviour change: a hung `git` now blocks the tmux refresh in fallback, where
three independent commands previously insulated it. This is already true of the daemon
path, so the change makes the primary path's behaviour the only behaviour.

## Blocker 3: layout constants

`colIndex` 2 → 1 and `colState` 2 → 1, matching `indexCol` and `StateIndicatorWithBg`,
which each render one column. `colIndicator` stays 3; `IndicatorWithBg` really does render
three.

Tier constants and the widths at which each tier is selected:

| Constant | Now | After | Tier selected at width | Now | After |
|---|---|---|---|---|---|
| `fullFixed` | 52 | 50 | `fullFixed + nameMin` | 60 | 58 |
| `noGitFixed` | 33 | 31 | `noGitFixed + nameMin` | 41 | 39 |
| `compactFixed` | 20 | 19 | `compactFixed + nameMin` | 28 | 27 |
| `noPRFixed` | 7 | 6 | `noPRFixed + nameMin` | 15 | 14 |
| `bareFixed` | 3 | 2 | `bareFixed + 1` | 4 | 3 |

Narrow panes gain 2 columns of name at the two widest tiers and 1 at the rest. At width
>= 104 nothing changes: the name column is already capped at 52 and reaches that cap at
width 102 after the change.

The durable part is a test asserting `VisibleWidth(renderRow(...)) == layout.Total()` at
every tier. The constants drifted from the renderers precisely because nothing compared
them, and the existing `Total() <= width` invariant passes happily while rows come out
short.

## Blocker 4: trim the polling query

`reviewThreadsQuery` loses its inner `comments(first: 5) { nodes { author { login } body } }`
connection, keeping `isResolved` and `isOutdated`. `fetchReviewThreads` returns only the
count. `PRStatus.ReviewComments` is no longer populated by polling.

The detail panel's comments mode fetches bodies on demand for the selected session, via a
`PRCommentsMsg` shaped like the existing `PaneCapturedMsg`. That path already exists and
already handles "not loaded yet".

Accepted consequences: switching to comments mode costs a fetch where it used to be
instant, and PR data replayed from the cache at startup carries no bodies.
`UnresolvedComments` keeps polling exactly as now, so no state, badge, or transition
behaviour changes.

## Error handling

Self-scheduling introduces one hazard a ticker did not have: a failed poll that does not
reschedule stops fallback permanently and silently. `collectCmd` therefore returns a
message on every outcome, `Snapshot` error included, and `handleSnapshot` reschedules
regardless of outcome. Today's `fetchTmuxCmd` returns `nil` on error, which was survivable
only because a ticker was driving it.

`RunEffects` runs in one goroutine per event so a hanging `notify` hook cannot stall the
daemon's ticker. Hooks already carry a 5s bound. `CleanupSession` gains a context timeout;
it has none today and now runs unattended.

Double cleanup cannot happen by construction. `Detect` fires on change, so while a slow
cleanup is in flight the next poll still sees `Done` and emits nothing.

A `Snapshot` that fails in the daemon is unchanged: `poll` logs once on the transition into
failure and once on recovery.

## Testing

Every test below has a stated mutation. Per the phase 2 retro, "would this fail if the code
it names were removed?" is answered by mutating the code, not by reading the test.

| Test | Mutation that must break it |
|---|---|
| Two `Model`s fed one snapshot produce two toasts and zero hook invocations | Move side effects back into the model |
| `Local: false` runs no hooks or cleanup; `Local: true` runs both | Drop the `Local` guard |
| Daemon fires `RunEffects` once for one transition with three clients connected | Fire per client |
| `Detect` primes silently on first call | Return events on the priming call |
| `Detect` yields exactly one event per change and none when unchanged | Compare against a stale map |
| `Detect` primes a vanished-and-returned session rather than firing | Drop the prune |
| A failed `Snapshot` still reschedules the next poll | Drop the reschedule on the error path |
| Daemon path and fallback path yield identical `m.sessions` from one stubbed `Commander` | Diverge either path |
| `VisibleWidth(renderRow(...)) == layout.Total()` at every tier | Restore `colIndex` or `colState` to 2 |
| The polling query contains no `comments(`, and `UnresolvedComments` is still populated | Restore the inner connection |

The identical-paths test is the one that matters most structurally: "both paths must render
identically" becomes a single assertion rather than a convention, and that property has
already drifted once.

Note the standing limit on view tests, so it is not rediscovered a third time: under
`go test` there is no tty and no forced colour profile, so lipgloss emits zero ANSI bytes
and every "styled" cell is a plain string. `VisibleWidth` assertions are still meaningful,
but they are not exercising escape handling.

## Sequencing

Blocker 2 lands before blocker 1. Writing the transition ownership against the current
three-message self-poll path means writing it against code blocker 2 then deletes.
Blockers 3 and 4 are independent of both and of each other.

Suggested order: 2, 1, 3, 4.

## What this does not fix

Carried forward to the phase 3 handoff:

- `visibleLen` counts runes, not display columns.
- A permanently failing `gh` still shows the last known PR indefinitely with no staleness
  marker.
- The daemon only prunes dead clients after a successful poll.
- Panel notifications can be dropped when no padding row is left over.
- `cache.Save` can leave `cache-*.json` litter after a hard kill.

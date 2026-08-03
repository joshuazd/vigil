# Dirty counts off the publication path

Written 2026-08-03 against what was then the first item on CLAUDE.md's "What is open" list.

## Status: not implemented, and that was the conclusion

**Only the "Instrumentation" section below shipped.** The `dirtyPoller`, the `scheduler`
rename and the `FetchGitStatus` split were all designed, approved, and then deliberately not
built.

The reason is the "The problem, re-measured" section, which is the part of this document worth
reading. Re-measuring the claim the work rested on gave 0.138s cold and 0.009s warm against the
~3.0-3.5s on record, with the git memo skipping every poll. On those numbers the change saves
~100ms of a 174ms cold poll and nothing at all on a warm one, and the one un-ruled-out trigger -
a fresh worktree's first `status` - is a one-or-two-poll hiccup after a dispatch rather than the
sustained ~3s cadence the item claimed. That is not worth a fourth poller, a scheduler rename, a
stats accessor and ten tests.

What shipped instead was the instrumentation: `Collector.GitStats()` and a rate-limited
`slow poll` line in `daemon.poll`, so the next person to suspect this reads a number off the
machine where it happens instead of re-deriving all of this a third time. Verified end to end
against a real daemon with a deliberately slowed `git` on the `PATH`:

```
vigil: slow poll: 4.019s total, 4.01s in git, slowest 4.01s at /Users/joshua.zink-duda/sc-223374
```

**If a `slow poll` line does show up in anger, this design is ready to implement as written.**
Everything below the problem section is unchanged from the approved version - treat it as a
plan on the shelf, not as a description of the code. Nothing in it has been built, so where it
and the code disagree, the code is right by default.

## The problem, re-measured

`docs/superpowers/2026-07-31-collector-async-remote-handoff.md` measured `fillGit` at 3.0 to
3.5 seconds per poll, 99.7% of `Snapshot`, and concluded that because `fillGit` exceeds
`git_interval` the git memo can never skip, so the real publication cadence is ~3s rather
than the 1s `tmux_interval` the design assumes.

That measurement was re-run on 2026-08-03 on the same machine. **The attribution reproduced.
The magnitude did not.**

Every subprocess `Snapshot` issued was timed across five real polls, logging anything over
30ms. `git status --porcelain` was the only git call that ever appeared - `rev-parse
--show-toplevel`, `branch --show-current`, `rev-parse --verify`, `rev-list --count`,
`merge-base` and `log -1 --format=%ct` never crossed the threshold once. So the 2026-07-31
attribution is correct: within `fillGit`, `status --porcelain` is effectively the whole cost.

The magnitudes:

| | 2026-07-31 | 2026-08-03 |
|---|---|---|
| `fillGit`, cold memo | ~3.0-3.5s | **0.138s** |
| `Snapshot`, warm memo | ~3s, never skips | **0.009s**, skips every poll |
| `status --porcelain` per worktree | 0.7-1.6s | 0.035-0.116s |
| live tmux sessions | 9 | 2 |

The 20x is the untracked cache. On the portal monorepo (28,769 tracked files),
`git -c core.untrackedCache=false --no-optional-locks status --porcelain` takes 0.67-0.91s -
squarely the 2026-07-31 range. With the untracked cache it is 0.066s; with `core.fsmonitor`
as well, 0.035s.

That does not fully explain the gap, and the spec should say so rather than assert a tidy
cause. `~/.gitconfig` sets both `core.untrackedCache` and `core.fsmonitor`, and its mtime is
2026-01-02 - months before the original measurement. The settings were already on.

Two candidate explanations were tested and **ruled out**:

- **Concurrency contention.** 1 versus 9 simultaneous `status --porcelain` calls against
  portal: 0.065s versus 0.067s wall. No contention.
- **Dirty-tree cache invalidation.** 50 file writes per iteration across five iterations left
  `status` at 0.035s throughout.

Two remain **un-ruled-out**, and both are ordinary for this user:

- **Cold worktrees.** The untracked cache lives in each worktree's own index, so a freshly
  dispatched worktree pays the full ~0.9s until its first `status` populates it. Every
  `shortcut-implement` and `gh-review` run cuts one.
- **Machine load.** Nine agent worktrees building and running tests at once, versus an idle
  machine with two sessions now.

**So the shape is real and its trigger is routine here, but "~3s per poll, 99.7%, the memo
can never skip" is not the current steady state.** The value of this change is bounding a bad
case, not speeding up the common one. Stated plainly, so no later reader mistakes it: on
today's numbers this saves roughly 100ms of a 174ms cold poll and nothing at all on a warm
one. With nine worktrees at 0.9s each over eight workers it takes ~1.0-1.8s of blocked
publication down to ~100ms.

## What this changes

`git status --porcelain` - and only that call - moves off the publication path into a poller.
Everything else `FetchGitStatus` does stays synchronous inside `Snapshot`.

That split is deliberate and load-bearing. `Snapshot` must keep resolving `Branch` and
`GitRoot` synchronously, because `groupByBranchRoot` feeds `prPoller`'s working set from them
and `transition.Detect` reads them off the event. Moving them would delay PR resolution by a
tick and blank the branch on the first poll, for no gain: they are not the latency source.

`Unpushed` and `RebaseAgeSecs` also stay synchronous. Measured at or under 30ms each, and
`unpushedCount` needs the branch name, so moving it would introduce an ordering dependency
inside the poller for no measured benefit. If either ever measures slow it joins the poller;
nothing in this design has to change for that.

Not in scope: `ListSessions`, `BellFlags`, the remote pollers, `Collector.Queue`.

## Architecture

### The scheduler becomes shared, by relocation not redesign

`internal/collect/scheduler.go` (new file) takes the `poller` interface and the `remote`
struct out of `remote.go`, renaming the struct to `scheduler` and `newRemote` to
`newScheduler`. Nothing about its behaviour changes: same absent ticker, same sticky
`sync.Once` context, same cap-1 coalescing nudge, same `refresh` seam. `remote.go` keeps
`prPoller`.

`Collector` then holds two instances:

```go
remote *scheduler  // prPoller, storyPoller, reviewPoller - off-box
local  *scheduler  // dirtyPoller - local subprocesses
```

Two instances rather than one list, because folding the git poller into `c.remote` would make
`RefreshRemote` a lie. That method has 52 test call sites across three packages and is
load-bearing vocabulary in six documents; renaming it is churn with no behavioural payoff.
Two schedulers cost one extra goroutine and keep both names honest.

**The absent ticker now covers subprocesses as well as API budget.** The existing argument is
that a daemon-fed client never calls `Snapshot`, so it never nudges, so its workers block for
the life of the process and spend no `gh`. The same mechanism means a daemon-fed client
spawns no `git status` either. Adding a ticker to `local` would give every open panel its own
`git status` loop against the monorepo - strictly worse than the `gh` case it was written
for, because there is no rate limit to notice it.

### dirtyPoller

`internal/collect/dirty.go` (new). Owns exactly three fields: `Modified`, `Added`, `Deleted`.
The name is chosen to make the boundary obvious - it is not a git poller, and `Branch`,
`GitRoot`, `Unpushed` and `RebaseAgeSecs` are not in it.

Structurally a sibling of `prPoller`, deliberately: `passMu` for single-flight, a `mu`-guarded
`entries` map and `working` set, a `gen` counter compared at write-back so an `Invalidate`
that lands mid-fetch leaves the entry due rather than satisfied by an answer that may predate
it, and a prune to the working set as it stands at write-back so a vanished root cannot
linger. Reads `GitInterval`, `GitWorkers` and `clock` through the `Collector`, as `prPoller`
reads `PRInterval`.

**Keyed by git root, not by pane path.** `status --porcelain` runs in the root, so two
sessions inside one worktree ask the same question, and keying by root collapses them to one
subprocess. This is also why the working set is a deduped `[]string` of roots rather than the
`branchKey` triples `prPoller` tracks.

Concurrency is `GitWorkers` (8), not `prWorkers` (4). `prWorkers` is low because each `gh`
call spends a rate-limited quota; local subprocesses have no quota, and 8 is what `fillGit`
already fans out to.

### Snapshot

```
ListSessions + BellFlags
fillGit             -> FetchGitMeta per due path, memoized at git_interval (unchanged)
dirty.track(roots)  -> deduped set of s.Git.GitRoot
dirty.fill(sessions)-> counts read from the store by GitRoot
groupByBranchRoot   -> prs.track / prs.fill (unchanged)
remote.nudge() + local.nudge()
```

**`fillGit` must stay ahead of `dirty.track`.** The working set is derived from
`s.Git.GitRoot`, which only exists once meta has landed. This is the one new invariant in the
design and it gets its own test; reversing the two lines yields a poller that never has
anything to do, and every other test in the suite would stay green.

A session whose `GitRoot` is empty - not a repository, or git unreachable - is excluded from
the working set and gets zero counts, which is what it gets today.

`Snapshot` remains non-reentrant and `gitMemo` remains lock-free and goroutine-owned. The
dirty store is mutex-guarded, for the same reason the PR store is: a worker writes it while
`Snapshot` reads.

### fetch

`FetchGitStatus` splits:

```go
func FetchGitMeta(ctx, cmd, panePath) session.GitStatus      // root, branch, unpushed, rebase age
func FetchDirtyCounts(ctx, cmd, gitRoot) (m, a, d int)       // status --porcelain
```

Renamed rather than quietly narrowed. `FetchGitStatus` returning a struct with three fields
silently left at zero is a trap; making the one production caller a compile error is the
forcing function. `parsePorcelain` becomes `FetchDirtyCounts`'s body and keeps its existing
XY-code tests unchanged.

`FetchGitMeta` keeps the existing early returns: no git root yields a zero `GitStatus`, and a
root with no branch yields `GitStatus{GitRoot: root}`. Sessions with no root get no dirty
counts, exactly as today.

## Pending, and what it costs

There is no `DirtyPending` flag, and that is a decision rather than an omission.

`PRPending` exists because `transition.Detect` must distinguish "never resolved" from "known
to have no PR" - without it, async fill turns every daemon start into a burst of `notify`
hooks and an `auto_cleanup` pass over every already-merged worktree. Dirty counts have no
such consumer. `Session.State()` never reads them; `GitStatus.IsClean()` has no non-test
caller anywhere in the tree. No transition can fire from a count changing, so no hook and no
cleanup can be provoked by one arriving a tick late.

**The accepted cost is a one-tick zero.** Before the poller's first pass lands for a root, the
Git column renders unpushed and rebase age but no `~N +N -N`. It shows in two places: daemon
start, and a session appearing for the first time. Client-side it reads as a brief flicker -
cache-seeded counts on first paint, blank for one tick, then real.

Two ways to hide it were considered and rejected. Running the first pass synchronously when
the store is empty is three lines, but it is a special case that only covers daemon start -
a newly appearing session still flashes - so it buys little and costs a mechanism claim that
is harder to verify later. Seeding the store from the session cache is more complete, but
`internal/collect` must not import `internal/cache`, so it needs an exported seed method
called from both the daemon and a self-polling client. Neither is worth it to hide a
one-second flicker on a column nothing gates on.

## Invalidate

Today's doc comment says "git comes back inside the next `Snapshot`, because `fillGit` is
synchronous". After this change that is half true: meta comes back inside the next `Snapshot`,
counts a tick later, like remote data.

`dirtyPoller.invalidate` zeroes `fetchedAt` rather than dropping entries, matching
`prPoller`. The reason there was that dropping re-marks branches pending and `Detect` skips a
pending session; here there is no pending flag, but zeroing keeps the last known counts on
screen through a forced refresh instead of blanking the column while the pass runs, which is
the same property by a different argument.

`Invalidate` fans out to both schedulers and nudges both. The `Invalidate` doc comment and the
corresponding CLAUDE.md bullet both need rewriting rather than amending, since the sentence
they turn on stops being true.

## Instrumentation

The 2026-07-31 number could not be checked against anything, which is why this spec had to
re-derive it from scratch. That should not happen twice.

**This is the only section that was implemented.** It is described as shipped; the rest of the
document is not.

`fillGit` records its wall time and its slowest pane path - pane path, not root, because
`gitMemo` is keyed by pane path and that is what `fillGit` iterates. `Collector.GitStats()`
returns them. `daemon.poll` times `Snapshot` and, when the total exceeds `s.Interval`, logs the
total, the git portion, the slowest duration and the pane path that owned it through `s.logf`.

Had the `dirtyPoller` been built, its last pass duration would belong here too, since a pass
exceeding `git_interval` means the counts are staler than intended even though it no longer
blocks publication.

`GitStats` inherits `Snapshot`'s threading rule: it is goroutine-owned and unguarded, like
`gitMemo`, so only the goroutine that called `Snapshot` may read it, and only after `Snapshot`
has returned. `daemon.poll` satisfies that by calling it inline immediately after `Snapshot`.

Two contract details that a test caught rather than the design:

- It is reset at the top of every `Snapshot`, so a `Snapshot` that fails before `fillGit` runs
  reports zero rather than the previous poll's numbers. A stale measurement attached to a fresh
  failure is worse than none.
- It stays zero when every path was memoized, because `fillGit` then issues no subprocesses at
  all. The first draft recorded `time.Since` around an empty fan-out and reported 416ns, which
  made "zero means no work" false; `TestGitStatsAreZeroWhenEveryPathIsMemoized` failed on it.

Rate-limited to one line a minute. `pollFailing`'s edge-triggered log-once shape is wrong
here: a genuinely slow machine would emit one line and then go quiet for hours, and what a
diagnosis needs is a time series. Once a minute gives that without a line per second.

Daemon-side only. A self-polling client has no log, and inventing one for it is out of scope.
Nothing is user-visible: a slow poll is not a failure, and a persistent warning about a 100ms
poll is noise.

## Testing

Nineteen briefs across three plans in this repository have contained tests that would pass
with their subject deleted. **Every test below gets its subject deleted, the failure watched,
and the output pasted into the task report.**

1. `Snapshot` issues **zero** `status --porcelain` calls. The core claim. Restoring the
   `parsePorcelain` call inside `fillGit` must fail it.
2. `Snapshot` -> `RefreshDirty` -> `Snapshot` lands the counts. Deleting `dirty.fill` must
   fail it.
3. One `Snapshot`, no refresh, still fills `Branch`, `GitRoot`, `Unpushed` and
   `RebaseAgeSecs`. Pins what did *not* move; moving `unpushedCount` into the poller must
   fail it.
4. Two sessions sharing a git root cost one `status` call. Keying the store by pane path must
   fail it.
5. `fillGit` runs before `dirty.track`. Swapping the two lines must fail it, and must fail
   *only* it - if any other test also fails, this one is not isolating the invariant.
6. A root absent from the working set is pruned at write-back, including on a pass where
   nothing was due.
7. An `Invalidate` landing between a pass reading the working set and writing it back leaves
   the entry due. Mirrors `prPoller`'s `gen` test.
8. `-race`: a `Snapshot` loop concurrent with a `RefreshDirty` loop.
9. A daemon-fed client spawns **zero** git subprocesses. Must go through a real
   `Collector.Start` rather than `RefreshDirty` - per the collector-async-remote handoff's
   landmine, a nudge that never reaches a worker leaves every `RefreshDirty`-driven test
   green. Adding a ticker to `local` must fail it.
10. A poll exceeding `Interval` logs once with the breakdown; a second slow poll inside the
    rate-limit window logs nothing.

Existing tests expected to need edits: `internal/fetch/git_test.go`'s
`TestFetchGitStatusBasic`, whose dirty-count assertions move to a `FetchDirtyCounts` test
while its branch and unpushed assertions stay with `FetchGitMeta`, and
`TestFetchGitStatusNoGitRoot`, which is a rename.
`internal/collect/collect_test.go`'s `countGitCalls` helper needs **no** change: it counts
`git rev-parse --show-toplevel`, which stays the first call `FetchGitMeta` makes, so every
memo-gating test that depends on it keeps its meaning.

## Landmines

- **Reversing `fillGit` and `dirty.track` is silent.** The poller gets an empty working set,
  never fetches, and the counts render as zero forever. Only test 5 catches it.
- **Test 9 is the only thing standing between this and per-panel `git status` loops.** It has
  to go through `Start`. Do not weaken it to "assert `Start` was called".
- **`git status --porcelain`'s cost is a property of the worktree's index, not of vigil.** A
  reader who cannot reproduce a slow poll has probably got a warm untracked cache, not a
  fixed bug. `-c core.untrackedCache=false` reproduces the slow path on demand.
- Do not set `core.untrackedCache` or `core.fsmonitor` on the user's behalf. It is not
  vigil's business, and both are already set here.

## Documentation

CLAUDE.md's `fillGit` bullet and open-item 1 both assert "~3.0-3.5s per poll, 99.7% of
`Snapshot`, the memo can never skip" as current fact. So does the superseding note at the top
of `docs/superpowers/specs/2026-07-31-collector-async-remote-design.md`. All three get today's
measurement and the untracked-cache finding, so the next reader does not inherit a number
that no longer reproduces.

## Files

| File | Change |
|---|---|
| `internal/collect/scheduler.go` | New. `poller` interface and `scheduler` struct relocated from `remote.go`, renamed from `remote`/`newRemote`. |
| `internal/collect/remote.go` | Keeps `prPoller`; loses the interface and scheduler. |
| `internal/collect/dirty.go` | New. `dirtyPoller`, keyed by git root. |
| `internal/collect/collect.go` | `Collector` gains `dirty`, `local`, `RefreshDirty`, `PollStats`. `fillGit` calls `FetchGitMeta` and records timing. `Snapshot` gains `dirty.track`/`dirty.fill`. `Start`, `Wait`, `Invalidate` fan out to both schedulers. |
| `internal/fetch/git.go` | `FetchGitStatus` splits into `FetchGitMeta` and `FetchDirtyCounts`. |
| `internal/daemon/daemon.go` | `poll` times `Snapshot` and logs a slow poll, rate-limited. |
| `CLAUDE.md` | `fillGit` bullet and open item 1 corrected. |
| `docs/superpowers/specs/2026-07-31-collector-async-remote-design.md` | Superseding note corrected. |

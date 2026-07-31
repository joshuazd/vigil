# Poll latency: one fix, and the structural finding phase 5 inherits

Written 2026-07-31, after the binary-refresh work merged. Small in code, but the finding
underneath it is the most phase-5-relevant thing in this directory, so read the last section
even if the fix itself is uninteresting.

Merged into local `main` as `6874acf`. Installed and live; the daemon was restarted once to
pick it up.

## The symptom

A freshly dispatched session took **5 to 10 seconds** to appear in the panel created inside
that very session.

## The cause, measured rather than reasoned

Not the dispatch script, and not the tick cadence.

A new session's branch has no PR yet. `gh pr view <branch>` answers that by **exiting 1**,
which `runWithRetry` in `internal/fetch/pr.go` could not tell apart from a transient
failure. So it asked three times, with 1s and 2s of backoff in between:

| Step | Measured |
|---|---|
| `gh pr view` on a branch with no PR | 0.45s |
| sleep, retry | +1.0s, +0.45s |
| sleep, retry | +2.0s, +0.45s |
| **total, inside a synchronous `Snapshot`** | **~4.5s** |

`fillPRs` blocks `Snapshot`, and `Snapshot` publishes nothing until it returns. So for those
4.5 seconds **no client received any update at all** - the new session's row, which tmux
already knew about, was held behind a `gh` call whose answer was "there is no PR". Add the
next tick and the new panel's own connect and you get the observed 5 to 10 seconds.

It also recurred every `pr_interval` (30s) for every session without a PR, quietly stalling
every poll for every client.

## The fix

`definitiveAnswer` reads gh's stderr off the `*exec.ExitError` that `cmd.Output()` already
populates, and `runWithRetry` returns immediately on a definitive answer. A genuine failure
still gets its three attempts.

The retry ladder **had no test at all** before this. It now has two, and both were watched to
fail: without the fix the no-PR test reports 3 invocations and takes 3.03s, and with
`noPRMarker` widened to match everything the transient-failure test reports 1 invocation
instead of 3.

Expected new-session latency after the fix is roughly **1.5 to 2 seconds**. That number has
**not been observed on a real dispatch** - the mechanism is unit-tested and the daemon is
confirmed running the fixed code, but no real story has been dispatched since. If it is still
slow, the remainder is the next section.

## What phase 5 inherits, and this is the important part

**`Collector.Snapshot` is fully synchronous, and everything in it blocks publication.**
`ListSessions` → `fillGit` (waits for every due git fetch) → `fillPRs` (waits for every due
`gh` call) → only then is a snapshot broadcast. One slow network call anywhere in there stalls
every panel's view of everything, including data that was already in hand.

The fix above removed the worst instance. It did not change the shape.

**Phase 5 adds two more pollers to exactly this path** - assigned Shortcut stories and
review-requested PRs. Both are network calls, both against rate-limited APIs, and if they are
added inside `Snapshot` the way PR fetching is, then every poll blocks on them too and the
session list gets slower again for reasons that have nothing to do with sessions.

So the structural change - publish tmux and git state immediately, fill remote data
asynchronously - is worth **more before phase 5 than after**. It was deliberately deferred
here rather than bundled into a latency bugfix, because it changes the collector's contract
and its single-goroutine memo ownership rule, which is load-bearing and documented in
`CLAUDE.md`.

Two options when that is picked up, neither designed yet:

- Let `Snapshot` return as soon as tmux and git are in hand, with remote data filled by a
  later pass that publishes again when it lands. Panels get a fast, slightly incomplete
  first paint and a complete second one.
- Keep `Snapshot` synchronous but move remote fetching to its own cadence entirely, off the
  poll loop, writing into the memos that `Snapshot` reads. Preserves the current contract's
  shape at the cost of a second owner for the memos, which is precisely the rule that
  currently makes them safe without a lock.

## Landmines

- **`definitiveAnswer` matches on gh's English stderr text** (`no pull requests found`). A gh
  release that rewords that message silently restores the old 4.5s behaviour, and no test
  would catch it, because the tests construct the message themselves. A `gh` upgrade is the
  thing that breaks this.
- **It applies to both `runWithRetry` callers**, `gh pr view` and `gh api graphql`. Harmless
  today - graphql never emits that message - but the helper is now slightly gh-PR-aware
  despite its generic name.
- **The 1.5 to 2 second estimate is arithmetic, not a measurement.** Treat it as a prediction
  until a real dispatch confirms it.

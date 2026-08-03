# Phase 5: the work queue, and what verification found that ten reviews did not

Written 2026-08-01 with `phase-5-work-queue` complete, `make test` green on all 14 packages,
`make lint` clean, and the daemon half verified on the real machine. 23 commits, from `main`
at `154f055` to `d9f62be`, 32 files, +3030/-113.

- Design: `docs/superpowers/specs/2026-07-31-phase-5-work-queue-design.md`
- Executed plan: `docs/superpowers/plans/2026-07-31-phase-5-work-queue.md`
- Verification record: `.superpowers/sdd/2026-07-31-phase-5-work-queue/verification-results.md`
  (committed, unlike the rest of that directory)

**The two most important things on this page are the `%self%` finding and the process note at
the bottom.** The first is a bug that ten per-task reviews missed and only real data caught.
The second is that this plan produced **nine tests that would have passed with their subject
deleted**, three of which reached the final review still shipping.

## What shipped

`vigild` polls two new off-box sources on their own goroutines, behind the `poller` seam that
`7b89c0e` landed for exactly this: `storyPoller` (`short api /search/stories`) and
`reviewPoller` (`gh search prs`). **`Collector.Snapshot` was not touched.** `remote` still has
no ticker.

`Collector.Queue(sessions)` merges both stores, hides anything a live tmux session already
covers, sorts stories-before-reviews and newest-first within each, caps at `queue_limit`, and
returns the items plus a count of what it hid. The daemon publishes both as
`Snapshot.Queue`/`Snapshot.QueueHidden` - additive with `omitempty`, so **`protocol.Version`
stays 1**.

The dashboard renders a QUEUE section below the session table. **The panel renders no queue
rows at all**, only a `⚡N` badge in its status bar, because a panel is 152x9 on this machine
with five sessions already live. `enter` on a queue row dispatches it **detached**, so picking
a review off the list is not a context switch.

Six new settings: `queue_enabled`, `queue_pr_query`, `queue_pr_age_days`, `queue_story_query`,
`queue_interval`, `queue_limit`.

## Added after the phase merged

Two changes landed on `main` after this document was first written, both in `b48979a`.

**Review rows carry the PR author.** `gh` already returned `author.login`, so this was a
field, a mapping and a column. It is 16 wide (longest real login observed is 15, GitHub
allows 39 so it truncates) and is **dropped for the whole section** when the title would fall
below 24 columns, so a narrow dashboard degrades to titles rather than wrapping. Stories
render a blank author deliberately: Shortcut carries a requester, but resolving it costs a
lookup per story and the column exists to answer "whose PR is this".

The column decision and the age width are computed **once per section**, not per row. Per-row
would put the author on some rows and not others depending on their age string, sliding the
title column with it.

**`truncateVisible` had an off-by-one, and it was pre-existing.** It returned `maxW`
characters and *then* appended `…`, so any truncated string was `maxW+1` cells and every
caller padding to `maxW` overflowed its column by one. This is the cause of the deferred
minor recorded below as "queue rows can exceed their width at narrow widths" - that finding
named the symptom, not the mechanism.

Worth noting how it surfaced: the width sweep written for the **new** author column failed at
width 40, and not because of the author. Four tests already covered `truncateVisible` and
none of them pinned its width contract, so fixing it broke nothing and would have been
invisible either way. The width contract is now pinned in `internal/view/detail_test.go`.

## The `%self%` bug, which is the consequential finding

The shipped default was:

```
"queue_story_query": {"VIGIL_QUEUE_STORY_QUERY", "owner:%self% !is:done !is:archived"},
```

**Nothing substituted `%self%`.** It is a feature of the `short search` subcommand, whose
`--help` says "Passing '%self%' as a search operator argument will be replaced by your mention
name". vigil uses `short api`, a raw passthrough to the Shortcut REST API that templates
nothing. The idiom was carried from one command's documentation into another command's query.

So **the story half of the work queue was permanently empty for every user**, and silently:
per the design's own landmines, a failed or empty story fetch has no log, so "no assigned
stories" and "the query is broken" render identically.

Measured:

```
$ short api "/search/stories?query=owner%3A%25self%25%20%21is%3Adone%20%21is%3Aarchived"
{"next": null, "data": [], "total": 0}
$ short api "/search/stories?query=owner%3Ajoshuazd%20%21is%3Adone%20%21is%3Aarchived"
total: 2
```

Fixed in `24079de`: `fetch.SearchStories` resolves `%self%` through `short api /member`,
cached in a package-level `sync.Map` following the `nwoCache` precedent, looked up **only**
when the query contains `%self%`, and returning an error rather than issuing a query with an
empty or literal owner - a failed pass keeps the last known list, which is the correct
degradation.

Re-verified against the **shipped default**: `queue_hidden: 1`, zero stories rendered, against
exactly one assigned story with a live `SC-223479` session. That is conclusive rather than
suggestive, because before the fix the query returned `total: 0`, so a nonzero `queue_hidden`
is unreachable without the substitution having happened.

**Ten per-task reviews did not catch this.** Every one of them read a diff. The default's value
was correct-looking in isolation and only wrong against a real API.

## Verification, with the numbers

Every daemon ran isolated: its own `HOME`, its own `XDG_RUNTIME_DIR` and therefore its own
socket. The user's real daemon (PID 13320) and all three of their panels were confirmed alive
and untouched throughout, and nothing in their workspace was modified.

**Publication latency: no regression.**

| | trials | mean |
|---|---|---|
| baseline `154f055` | 9 | 1059.7ms |
| phase-5 `074ee2d` | 10 | 1062.6ms |

~3ms, inside single-binary noise. This is the number that proves the pollers really are off
`Snapshot`.

**A daemon-fed panel spends zero queue budget.** Polled every 0.3s for 95 seconds, longer than
the 60s `queue_interval`: **zero child processes of any kind** from the panel. The only `gh`
seen system-wide had the isolated daemon as parent. This is the property the whole no-ticker
design rests on, now confirmed on real hardware rather than only in a 200ms test window.

**Age filtering correct to the day.** 16 raw review-requested PRs, 12 after
`queue_pr_query`/`queue_pr_age_days`; all 4 dropped were older than the 14-day cutoff, and the
remaining 12 IDs matched the daemon's output exactly.

**Teardown clean.** Socket unlinked, and the flock genuinely released rather than orphaned - a
fresh daemon bound the same socket immediately after.

## What was NOT verified

- **A real dispatch of a queue item never happened.** It is not reachable non-interactively:
  `vigil dispatch` from the CLI sets `Request.Detached = false`, so it expands `{flags}` to
  the empty string and **would teleport the user**, which was the single condition placed on
  authorizing the test. Reaching the `enter` path needs `tmux send-keys` against a live TUI,
  where a mis-aimed key dispatches the wrong item.

  What is pinned instead: `TestEnterOnAQueueRowDispatchesOverTheWire` drives `enter` through
  `m.Update`, stands up a real unix listener, decodes an actual `protocol.Request` and asserts
  `Detached == true`; `TestDetachedJobPassesTheFlag` asserts `--detached` reaches the expanded
  hook script. The final reviewer additionally read the dotfiles side and confirmed
  `--detached` is parsed by `dispatch:80` and gates nothing but `teleport_client_to` in both
  `gh-review:98,216` and `shortcut-implement:99,229`, and that `session_name_from_title`'s
  output matches `QueueItem.SessionPrefix()` exactly, so post-dispatch dedup will work.

  Residual risk is a behaviour change in a repository this branch does not touch.
- **The `fillGit` ~3s worst case** was not reproduced; the worktrees present answered
  `git status` in well under 1s, so this run says nothing about publication cadence under that
  slower condition.
- **`queue_limit`'s cap** was never exercised - both raw lists were under 20.

## Landmines

- **A missing or broken `short` is completely silent.** No log, no toast. "No assigned stories"
  and "Shortcut is unreachable" render identically. Deliberate on two counts - `short` is not
  a startup dependency (vigil must run without Shortcut, pinned by
  `TestShortIsNotAStartupDependency`) and `prPoller` does not log a failed `gh` either. First
  diagnostic step: run the poller's exact command by hand,
  `short api "/search/stories?query=<your queue_story_query>"`.
- **Dedup hardcodes a cross-repository naming convention.** `SC-<id> ` / `PR-<number> ` comes
  from `session_name_from_title` in `~/dotfiles`, with a test on this side only. If that format
  changes, dedup degrades **silently** and the queue advertises work already in flight. The
  `PR.Number` secondary key covers reviews; **stories would be fully exposed.**
- **`{flags}` is the one hook placeholder `ExpandHook` does not shell-quote.** Safe because it
  carries one of two vigil-chosen constants; `rawPlaceholders` must never be widened. Quoting
  it would pass a stray `''` argument.
- **The `{flags}` migration warning is effectively invisible where it matters.** Both `runTUI`
  and `runPanel` use `tea.WithAltScreen()`, so stderr written before `p.Run()` lands on the
  normal buffer: a panel never exits and the `prefix v` popup is destroyed on close. **A user
  whose `config.toml` lacks `{flags}` will be teleported on their first queue dispatch and will
  not have seen the warning.** `vigil dispatch` from a shell does print it. A toast would be
  the real fix.
- **The status bar budgets width with `lipgloss.Width`, not `visibleLen`.** Changed in
  `eb6bbeb` because `⚡` is two terminal cells and one rune, so the bar wrapped at widths 12,
  25 and 34 - pushing every table row down. `job.go:11-18` had already diagnosed this class
  once; nothing guarded `statusbar.go`. Do not revert that to a rune count.
- **`queueRowBudget` gives the queue everything above `minTableRows = 3`.** With 8 sessions and
  a full queue the session table is squeezed to 3 rows even at height 60. Pre-existing in shape
  and marginally improved by the fix, but it inverts the design's premise that the session list
  is primary. A proportional policy would match the intent better.
- **The session table has no viewport.** `RenderTable` drops sessions past `height` with no
  scroll, so with a long queue some session rows are cursor-reachable and undrawn. Same defect
  class as the queue bug fixed in `d4c0391`, on the surface the design calls primary. `enter`
  on a session is non-destructive and `m`/`x` confirm first, which is the only reason this was
  not blocking.

  **Fixed 2026-08-03 (`5d659e4`)** by `view.TableWindow`, which scrolls rather than truncating -
  the opposite choice from `d4c0391`, because forbidding the cursor is right for queue items and
  wrong for sessions. It also fixed **panel mode**, which had the same defect at 10+ sessions
  and is not mentioned above.
- **80-column dashboards overflow their height by 1-2 lines** because the footer help line
  wraps. Present identically before this branch. The height-sweep test pins width 120 and
  cannot see it.

## Deferred

Sixteen minors were triaged "ship it" by the final review; the full list with per-item
reasoning is in the review record. The ones most likely to matter later:

- `getSelf` is Load-then-Run-then-Store, not single-flight. Mirrors `getNWO`; unreachable today
  because `storyPoller.pass` is serialized by `passMu`.
- An exported `VIGIL_QUEUE_ENABLED` overrides the `queue_enabled: "false"` test fixtures and
  turns the new queue tests into nil-pointer panics rather than named failures. Same class as
  the documented `HOME`/`panel_auto` landmine.
- The cursor clamp in `applySnapshot` is a bounds check, not identity preservation. The
  narrated scenario - 3 sessions plus 2 queue items, a session vanishes, cursor 3 silently
  retargets from one queue item to another - is **not** closed. Nothing crashes, because
  `queueCursor` and `selectedSession` both bounds-check; the residual is a silent retarget.
  **Do not carry forward the claim that the clamp prevents a crash class. It does not.**
- Stories can starve reviews out of the queue: the comparator puts every story ahead of every
  review and `queue_limit` applies to the merged list, so 20 undeduped stories means zero
  review requests shown. Review requests were the original motivation for the feature.

## Process notes, and this is the part worth reading twice

**This plan produced nine tests that would have passed with their subject deleted.** Six were
caught during execution, one was self-caught by an implementer running its own mandated
mutation check, and **three survived all ten per-task reviews and were only found by the
whole-branch review** - all three on the primary render path, where deleting any one of
`collectCmd`'s `Collector.Queue` call, `listenDaemonCmd`'s `Queue` field, or `handleSnapshot`'s
daemon-fed assignment left the entire suite green.

Combined with the ten recorded across the previous two plans, that is **nineteen**. This is no
longer a warning about a pattern; it is the repository's default failure mode, and the
mitigations that actually worked were:

1. **Mandating a mutation check per test, with output in the report.** Every test whose brief
   demanded "delete the subject, watch it fail, restore" came back sound. The one an
   implementer self-caught was caught by exactly this.
2. **Reviewers re-deriving claims rather than reading them.** The reviewer who reimplemented
   the sort comparator in a standalone program found the within-kind key untested. The one who
   re-ran the overflow sweep across 39,900 configurations found the fix worked and then found
   the cursor could still outrun it.
3. **A whole-branch review that specifically distrusts the suite.** The per-task reviews were
   good and still missed all three seam gaps, because a task-scoped diff cannot see a seam.

**What did not work:** trusting a plan's test code. Five of the nine defects were in briefs
written by the plan's author, including tests justified with reasoning that was factually
wrong about the codebase - one brief claimed `dispatchCmd` could not be tested end to end
because it dials a socket, when `dispatch_test.go` already had a fake-listener pattern doing
exactly that.

Two further process notes:

- **The plan's own verification incantation was buggy.** `HOME=... GH_TOKEN="$(gh auth token)"`
  on one line evaluates `HOME` before the token capture, so `gh` runs unauthenticated. Anyone
  reusing that snippet should capture the token into a variable first.
- **A delegated agent spent an hour re-reading a fix without starting the check it was sent to
  run.** The remaining verification was faster done directly. Delegation is not free, and a
  well-specified small check is often not worth a subagent.

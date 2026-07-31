# Phase 5 work queue: real-machine verification

Run 2026-07-31 against branch `phase-5-work-queue` at `074ee2d`, on the real machine, against
the real GitHub account (`joshuazd`) and real Shortcut workspace (`huntress`). Steps 1-4 and 6
run per the task-10 brief. **Step 5 was not run** — it dispatches a real queue item, which
creates a real git worktree and tmux session, and the brief requires the user's explicit
consent and choice of item first. That consent was not sought in this job, so nothing was
dispatched, no `enter` was pressed on a queue row, and no worktree or session was created.

Every daemon below ran under its own `HOME` and `XDG_RUNTIME_DIR` (via `mktemp -d`), so it
bound its own socket and never touched the user's real daemon or the three real panels
already attached to real sessions (`vigil --panel` PIDs 33079, 35509, 80982 — confirmed alive
and untouched throughout, and confirmed with `tmux list-sessions` that the real session list
was unchanged at the end: `SC-223453 ...`, `SC-223479 ...`, `main`). tmux itself is not
namespaced by `HOME`/`XDG_RUNTIME_DIR`, so the isolated daemon read the real tmux server (as
designed — only `list-sessions`/read state, never create/kill) and so saw the real sessions,
which is what makes the dedup finding below meaningful rather than synthetic.

**One incantation bug found and worked around, worth flagging for anyone re-running this
brief**: the brief's exact `HOME="$VDIR/home" ... GH_TOKEN="$(gh auth token)" ./vigil daemon &`
line does not work as written. Bash/zsh evaluate the assignment prefix of a simple command
left to right, so by the time `$(gh auth token)` runs, `HOME` has already been reassigned to
the empty `$VDIR/home`, `gh` finds no `hosts.yml` there, and prints "no oauth token found for
github.com" to `GH_TOKEN` (empty). The daemon then starts but its PR poller runs unauthenticated
`gh`. Fix: capture the token into a shell variable *before* reassigning `HOME`, e.g.
`TOK=$(gh auth token); HOME=... GH_TOKEN="$TOK" ./vigil daemon &`. This was caught immediately
because the daemon log printed the error; used the fixed form for every run below.

## Step 2: queue contents

Attached a small throwaway Go client (built and run from inside the module so it could import
`internal/protocol`, deleted afterward, never committed) that dials the daemon socket and
prints one `Snapshot`'s `Queue`/`QueueHidden` fields as JSON.

**With the shipped default config** (`queue_story_query` left at its default):

- 12 items total, **all `review`**, 0 `story`.
- `queue_hidden`: **0**.

That matches the literal symptom the brief calls "the landmine": `hidden` is 0 while `SC-*`
sessions exist (`SC-223453 ...`, `SC-223479 ...`). **But the root cause is not a dedup
failure — it is upstream of dedup entirely.**

**Root cause: `queue_story_query`'s default value can never return a result.**
`internal/config/config.go:49` ships:
```
"queue_story_query": {"VIGIL_QUEUE_STORY_QUERY", "owner:%self% !is:done !is:archived"},
```
Nothing in the codebase substitutes `%self%` with anything (confirmed by grepping the whole
`internal/` tree — the literal string `%self%` appears exactly once, in this default).
`internal/fetch/queue.go`'s `SearchStories` passes the query straight through to
`short api /search/stories?query=...` with no templating, and `short api --help` confirms
`short api` does no templating of its own — it is a raw passthrough to the Shortcut API.
Verified directly:
```
short api "/search/stories?query=owner%3A%25self%25%20%21is%3Adone%20%21is%3Aarchived"
# -> {"next": null, "data": [], "total": 0}
```
Substituting the real Shortcut mention name (`joshuazd`, from `short api /member`) for
`%self%` returns real data:
```
short api "/search/stories?query=owner%3Ajoshuazd%20%21is%3Adone%20%21is%3Aarchived"
# -> total: 2, ids 223453 and 223479
```
So **with default config, the story half of the queue is permanently empty for every user**,
not intermittently, not just for this account. This is a bigger finding than a dedup miss:
dedup was never exercised because there was nothing for it to hide.

**Dedup itself verified working, once the query is fixed.** Restarted the isolated daemon
with `VIGIL_QUEUE_STORY_QUERY="owner:joshuazd !is:done !is:archived"` (an existing,
already-supported env override — no code changed) and re-read the snapshot:

- `queue_hidden`: **2**
- rendered `story` items in the queue: **0**

Both real stories (`223453`, `223479`) were correctly suppressed because their titles match
live tmux session names via `QueueItem.SessionPrefix()`/`MatchesSessionName` (`SC-223453 ...`,
`SC-223479 ...`). **Dedup is not broken. The default config that feeds it is.**

**Review-requested PR count and `queue_pr_age_days` filtering**, checked against
`gh search prs --state=open --json number,updatedAt,repository -- "review-requested:@me"`
(no draft/age filters, run directly):

- Raw, unfiltered: **16** open review-requested PRs.
- Queue (`queue_pr_query` default `review-requested:@me -is:draft`, `queue_pr_age_days`=14):
  **12** items.
- Diff (4 dropped): `#205` (updated 2026-07-09), `#31118` (2026-05-29), `#31232` (2026-06-22),
  and `#367` (2026-05-22, also `isDraft=true`) — all older than the 14-day cutoff
  (2026-07-17). Checked `isDraft` for all 16: only `#367` is a draft, and it is also the
  oldest, so the two filters overlap on that one PR rather than compounding to a different
  total. The remaining 12 IDs match exactly what the daemon returned. **Age filtering
  verified correct to the day.**

## Step 3: publication latency (no regression check)

Built the merge-base binary at `154f055` ("docs: plan phase 5, the work queue") in a separate
worktree (`git worktree add`, removed after). Confirmed `git diff --stat b722c73 154f055`
touches only two doc files (2703 insertions, 0 code) — so `154f055` is code-identical to
`b722c73`, the commit the brief names as the expected baseline.

Measured, for each binary: kill/clean the isolated `run` dir, start the daemon fresh
(same isolated `HOME`/`XDG_RUNTIME_DIR` used throughout), wait for the socket to appear, dial,
and time to the first `Snapshot` frame — same real tmux sessions and git worktrees on both
sides, 10 trials each after discarding one go-build-cache-cold outlier per binary:

| | trials | min | max | mean |
|---|---|---|---|---|
| baseline `154f055` | 9 | 1025ms | 1128ms | **1059.7ms** |
| phase-5 `074ee2d` | 10 | 1002ms | 1157ms | **1062.6ms** |

Difference: **~3ms**, fully inside the trial-to-trial spread (~100ms) either binary shows on
its own. **No regression.** This is consistent with the design: the queue pollers
(`storyPoller`, `reviewPoller`) are siblings of `prPoller` off `Snapshot` entirely, woken only
by `Snapshot`'s nudge, and `Collector.Queue` is called by the daemon/self-poller after
`Snapshot` returns, not inside it. The dominant cost on both sides is `fillGit`'s
`git status --porcelain` against the real worktrees backing `SC-223453`/`SC-223479`, matching
the already-documented `fillGit` finding — nothing new here, but reassuring that the ~3s
worst case from the docs did not show up on these particular worktrees today (~1s observed,
not ~3s — worktree-state-dependent, not a contradiction of that finding).

## Step 4: daemon-fed panel spends no queue budget

Started `./vigil --panel` against the isolated daemon's `XDG_RUNTIME_DIR` (via `HOME`/
`XDG_RUNTIME_DIR` env, run inside `script -q` for a pty — **no tmux session created**, per
the constraint). Confirmed the panel connected to the isolated daemon (its own PID, distinct
from the three real user panels, which were separately confirmed alive and untouched via
`ps`).

Polled every 0.3s for 95 seconds (`queue_interval` default is 60s) for any child process of
the panel's PID. **Zero children of any kind appeared** — no `gh`, no `short`, nothing.
The only `gh` process observed system-wide during the window was `gh pr view main ...` with
the **isolated daemon's** PID as parent, not the panel's — i.e., `prPoller`'s existing,
expected activity, unrelated to the panel and unrelated to the queue pollers.

This confirms the property the no-ticker design rests on: a daemon-fed client never runs its
own `Collector`/pollers at all, so it spends no `gh`/`short` budget of its own, for at least
one full `queue_interval` plus margin.

## Step 6: teardown

`kill` on the recorded daemon PID. Confirmed:
- process gone (`ps -p <pid>` exit 1)
- `vigild.sock` removed from the isolated `run` dir
- `vigild.sock.lock` file still present (expected — the design holds an flock across the
  socket's lifetime and never unlinks the lock file itself, only releases the flock)
- **lock actually released, not just the process dead**: started a fresh daemon against the
  same isolated `run` dir immediately after and it bound the socket with no contention error,
  confirming the flock was released rather than orphaned.

The user's real daemon and all three real panels were never signaled, and the real tmux
session list (`SC-223453 ...`, `SC-223479 ...`, `main`) was unchanged start to finish.

## What was NOT verified

- **Step 5, entirely**: no dispatch of a real queue item, no check that the tmux client stays
  put, no timing of the new session appearing, no check that the dispatched item disappears
  from the queue on the next poll (dedup end-to-end via a live `SC-`/`PR-` session created by
  the real dispatch flow, as opposed to the pre-existing sessions used for the Step 2 dedup
  check). Needs the user's explicit consent and choice of item, per the brief.
- Behavior of `queue_pr_query`/`queue_story_query` overrides beyond the one substitution
  tested (`owner:joshuazd ...`) — e.g., whether a workspace-scoped or team-scoped default
  would behave differently.
- The `fillGit` worst case (~3s on the portal monorepo per the existing handoff) was not
  reproduced here; today's worktrees answered `git status` in well under 1s, so this run
  cannot speak to publication cadence under that slower condition.
- Whether the `%self%` bug affects `queue_pr_query` too — it doesn't contain `%self%`
  (`review-requested:@me` uses GitHub's own `@me`, which GitHub resolves natively), so it was
  not implicated, but this was not independently re-derived from the code, only observed to
  work correctly in Step 2.
- No load/stress testing of the daemon under many queue items, and no test of `queue_limit`'s
  20-item cap (both raw lists were under 20 in this run: 16 PRs after `-is:draft`/age
  filtering down to 12, 2 stories with the corrected query).

## Bottom line

- **Dedup mechanism**: works correctly (verified `hidden=2` for both real stories once fed a
  working query).
- **Default config landmine, not previously named in the brief**: `queue_story_query`'s
  shipped default (`owner:%self% ...`) is dead on arrival — `%self%` is never substituted
  anywhere in the codebase, so every user's story queue is empty by default, permanently,
  regardless of dedup. This is a production bug in the phase 5 code, not a test artifact;
  not fixed here per the "don't fix production code in this task" instruction.
- **Age filter**: correct, verified to the day against a raw unfiltered `gh search`.
- **Publication latency**: no regression (~3ms mean difference across 19 total trials,
  inside single-binary noise).
- **Daemon-fed panel budget**: confirmed zero, over 95s (>60s `queue_interval`).
- **Teardown**: clean, lock genuinely released.

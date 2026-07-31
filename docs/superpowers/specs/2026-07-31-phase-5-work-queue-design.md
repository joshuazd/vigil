# Phase 5: the work queue

Written 2026-07-31, from `main` at `b722c73`, with phases 0-4, asserted effect ownership
(`b8afd82`) and the collector async remote layer (`7b89c0e`) all merged.

The cockpit design's one-paragraph sketch of phase 5 is
`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`, "Phase 5: work queue". This
document supersedes it and **narrows it deliberately in one place**: the menu bar does not
present the queue, now or later. See "Rejected alternatives".

The seam this builds on is the `poller` interface in `internal/collect/remote.go`, landed by
`7b89c0e` for exactly this purpose. Phase 5 therefore **does not touch `Snapshot`**.

## The problem

Work arrives from two places and neither is visible from the cockpit. Assigned Shortcut
stories live in a browser tab. Review-requested PRs live in a different browser tab. Starting
either means leaving vigil, finding the URL, and dispatching it - which is what
`dispatch-from-chrome` exists to make bearable, and it is still a context switch away from
the surface that already knows about every session you have running.

The orphaned `scripts/scripts/gh-review-poll` was the previous attempt. It polled
`review-requested:@me` on a 60s timer and **auto-dispatched** every unseen result, keeping a
seen-file so it would not dispatch twice. That is the design phase 5 supersedes, and the
auto-dispatch is the specific part it does not inherit.

## Measurements this design is built on

Taken 2026-07-31 on the developer's machine. These are the numbers that decided the shape;
without them the obvious design is wrong in three separate places.

**The panel is 152x9.** Every vigil panel pane, measured via `tmux list-panes`. Nine rows,
top orientation, and five sessions are already live. A "QUEUE" header plus its rows does not
fit and never will. `panel_geometry` defaults to `-vb 10` for top orientation, so this is the
designed size, not a transient one.

**Review requests: 11 open**, spread across `2026-05-22, 05-29, 06-22, 07-09, 07-17, 07-17,
07-29, 07-29, 07-31, 07-31, 07-31`. Two are drafts. Roughly half are older than two weeks.
An unfiltered list is 11 rows of which ~4 are live work.

**Every assigned story already has a tmux session.** `SC-199420`, `SC-223453`, `SC-223477`
were all both assigned and running at the time of measurement. Dedup against live sessions is
therefore not a nicety at the edges - on a typical day it is most of the filter, and without
it the story half of the queue would be pure noise.

**`short api` is usable and `short search --format` is not.** `short search -f '{id}|{name}'`
prints the template literally; it does not interpolate. `short api
"/search/stories?query=..."` returns clean JSON on stdout in 0.69s with its progress spinner
on **stderr**, so no output scrubbing is needed.

**`gh search prs` rejects a leading-dash qualifier before `--`.** `gh search prs
"review-requested:@me" "-is:draft"` fails with `unknown shorthand flag: 'i' in -is:draft`,
because cobra parses it. With `--` first it works. Flags must precede `--`; query tokens must
follow it.

**GitHub search has no relative dates.** `updated:>-14d` is a hard error. Only absolute
(`updated:>=2026-07-20`) works. An age window therefore cannot live inside a static default
query string.

## Architecture

### The two pollers

New file `internal/collect/queue.go`. Two `poller` implementations, siblings of `prPoller`,
constructed in `collect.New` and passed to `newRemote` alongside it. Each gets its own
goroutine, so a slow Shortcut API cannot delay `gh` and a rate-limited `gh` cannot delay
Shortcut.

**`reviewPoller`** runs, through `fetch.Commander`:

```
gh search prs --state=open --limit <queue_limit>
   --json number,repository,title,url,updatedAt
   -- <tokens of queue_pr_query...> updated:>=<computed date>
```

The `--` is load-bearing and the tests pin it. Every flag precedes it; every config-supplied
token follows it.

**`storyPoller`** runs:

```
short api /search/stories?query=<url-encoded queue_story_query>
```

and reads `.data[]` for `id`, `name` and `app_url`. Stdout only; stderr carries the spinner
and is discarded.

Each poller has the same internal shape as `prPoller` - a `passMu` making a pass
single-flight, an `mu` held only around the store, and a `gen` counter so an `invalidate`
landing mid-fetch is not satisfied by an answer that predates it - **minus `track` and
`fill`**. Those two exist on `prPoller` because PR state is per-branch data grafted onto
sessions. Story and review lists are global; there is no working set to post and nothing to
write onto a session. Due-ness is a single `fetchedAt` per poller, gated on `queue_interval`
read through the `Collector` the same way `prPoller` reads `PRInterval`.

### The read path

```go
func (c *Collector) Queue(sessions []*session.Session) []session.QueueItem
```

Pure over the two stores plus the session list: merge, dedup, sort, cap. **`Snapshot`'s
signature does not change**, and `Snapshot` does not call this. Two callers:

- `internal/daemon`'s `poll`, after `Snapshot` returns, when building the `protocol.Snapshot`
- the Model's self-poll path, in the same place it uses `Snapshot`'s sessions

A daemon-fed client never calls it; it reads `Snapshot.Queue` off the wire. This is exactly
how sessions already work, and it is what keeps "one daemon means one `gh` budget" true for
the queue as well.

The new type lives in `internal/session`, where `PRStatus` lives, because `protocol` already
imports `session` and `collect` does not import `protocol`:

```go
type QueueItem struct {
    Kind      string `json:"kind"`  // "story" | "review"
    ID        string `json:"id"`
    Title     string `json:"title"`
    Input     string `json:"input"` // what gets dispatched, verbatim
    Repo      string `json:"repo,omitempty"`
    UpdatedAt int64  `json:"updated_at"`
}
```

`Input` is stored rather than reconstructed at dispatch time: `dispatch` routes on the shape
of its argument, and the poller that fetched the item is the only thing that knows for
certain whether it is a story or a PR.

### Dedup

An item is hidden when a live tmux session already covers it.

- **Primary:** a session whose name begins `SC-<id> ` or `PR-<number> `, which is what
  `session_name_from_title "SC"|"PR"` produces in `~/dotfiles`.
- **Secondary, reviews only:** any session whose `PR.Number` equals the item's number.

Ordering is stories first, then reviews, each newest-updated first, in one flat list - the
id column (`sc-223480` against `portal#34967`) already carries the kind, so a second
subsection header would be redundant at the widths this renders at.

The section's count line reports only what **vigil** dropped:

```
QUEUE  4 · 3 in progress
```

Not "7 filtered". The query filters server-side and vigil cannot see what it removed;
printing a number that implies otherwise would be a lie the user has no way to check.

### Config

Six new entries in `settingDefaults`, following the existing env-var-plus-default pattern:

| key | env | default |
|---|---|---|
| `queue_enabled` | `VIGIL_QUEUE_ENABLED` | `true` |
| `queue_pr_query` | `VIGIL_QUEUE_PR_QUERY` | `review-requested:@me -is:draft` |
| `queue_pr_age_days` | `VIGIL_QUEUE_PR_AGE_DAYS` | `14` |
| `queue_story_query` | `VIGIL_QUEUE_STORY_QUERY` | `owner:%self% !is:done !is:archived` |
| `queue_interval` | `VIGIL_QUEUE_INTERVAL` | `60` |
| `queue_limit` | `VIGIL_QUEUE_LIMIT` | `20` |

`queue_limit` caps each poller's fetch (`gh --limit`, and a slice of `short api`'s `.data`)
and is applied again to the merged list after dedup, so it bounds both the `gh` page size and
the rendered height. It is deliberately one knob rather than a fetch limit and a render
limit: two would make "why is this item missing" depend on which one bound first.

`queue_pr_age_days` is a separate setting rather than part of the query because GitHub search
has no relative dates (see Measurements). vigil computes `updated:>=<date>` at poll time and
appends it. `0` disables the window.

`queue_enabled = false` means the pollers are **not constructed at all**, not constructed and
skipped. There is then no store to read, no goroutine to schedule and no code path that can
spend budget by accident.

The story query default deliberately names no workflow state. The user's states - `Ready for
Dev`, `In Development`, `Ready for Review` - are Huntress-specific, and a shipped default
that hardcoded them would be a guess presented as a default. `!is:done` plus session-dedup
covers most of it; narrowing further is a config edit.

### Rendering

**Dashboard.** New `internal/view/queue.go`:

```go
func RenderQueue(items []session.QueueItem, cursor int, width int) string
```

Concatenated below the session table by the Model. Columns: repo-qualified id, title, age.
Repo-qualified because `soc-workflows#205` and `portal#205` are otherwise the same string.
The view stays pure, as everywhere else.

`cursor` is an index into `items`, or `-1` when the cursor is on a session row. The Model owns
the single cursor and the translation; the view never has to know how many sessions precede
it.

**Panel.** One `⚡N` segment in `RenderStatusBar`, added at priority just after `health` and
before the state counts. `addSegment` already drops any segment that does not fit, so a
narrow panel loses the badge automatically rather than wrapping. **Zero rows are consumed.**

`RenderStatusBar` gains a `queueCount int` parameter. The Model passes the real count in
panel mode and `0` in dashboard mode, where the section itself already says it. The decision
stays in the Model; the view renders what it is handed.

### Interaction

One cursor. `j`/`k` flows from the last session straight into the first queue row - no focus
region, no `tab`, no mode.

- `enter` on a queue row submits through the existing `dispatch.Submit` path with the item's
  `Input` and `Detached` set.
- `m`, `a`, `c`, `D` are no-ops on a queue row.
- `x` multi-select skips queue rows, so no existing batch action can receive one.

Detached, rather than teleporting, is the point: picking a review off the queue while
mid-edit should not yank the tmux client into a new session. The new session appears under
SESSIONS when it is ready and the user switches when they choose to.

The flag reaches the script through a **`{flags}` placeholder in the `dispatch` hook**:

```
dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}"
```

expanding to `--detached` for a queue selection and to the empty string for the `d` key.
`ExpandHook` already treats every `{...}` as a placeholder, so this needs no new mechanism -
and the existing prohibition on `${VAR}` in hook bodies is unchanged and still applies.

The shipped default gains `{flags}`, but a user whose `config.toml` pins the `dispatch` hook
explicitly overrides that default and must edit their own line. vigil's existing
stale-dispatch-hook startup warning is extended to detect a hook with no `{flags}` and say
that queue dispatch will teleport until it is added.

Detach is **not** implemented by withholding `VIGIL_CLIENT`. That variable is also the source
of the new window's size and the panel's orientation; dropping it produces the headless 80x24
session and the ~175-column panel balloon that phase 4 fixed.

### Protocol

Two additive fields. **`protocol.Version` stays 1**, on the same argument that kept `Jobs` at
version 1:

- `Snapshot.Queue []session.QueueItem` — `omitempty`
- `Request.Detached bool` — `omitempty`

Old daemon, new client: `Detached` is ignored and the dispatch teleports. New daemon, old
client: `Detached` is absent and the dispatch teleports. Both degrade to exactly today's
behaviour and neither errors. Per the binary-refresh handoff, the first install after this
lands cannot re-exec anything, so both directions will occur in practice for one cycle.

## Failure handling

| Failure | Behavior |
|---|---|
| `short` not on `PATH` | The subprocess fails, the pass is a failed pass, and the story half stays empty. **Not** added to the startup `tmux`/`git`/`gh` `LookPath` check - vigil must keep working for anyone without Shortcut, and a spurious "short not found" at startup is the same class of harm as the `gh` case `main`'s dispatch ordering already guards against. The poller does not log this, matching `prPoller`, which does not log a failed `gh` either. See the landmine below: that consistency has a cost. |
| `gh search` or `short api` fails | The last known list is kept rather than blanked, the same rule `prPoller` applies to a failed PR fetch. `fetchedAt` still advances, so a rate-limited `gh` is not retried on every nudge. |
| A malformed query | The subprocess exits non-zero and is treated as any other failed pass: last known list, logged. A query that is merely wrong returns zero results, which is indistinguishable from an empty queue and is the user's to notice. |
| No daemon running | The self-polling client fetches the queue itself, exactly as it fetches everything else. Data path, not an owner - unchanged by this phase. |
| Dispatch of a queue item fails | It is a normal job. It appears refused or failed in every panel and `esc` clears it, per phase 4. |

**Nothing is ever auto-dispatched.** No seen-state, no PID file, nothing persisted. That is
the single behavioural difference from `gh-review-poll` and it is the reason this supersedes
it rather than reimplementing it.

`Collector.Invalidate` zeroes `fetchedAt` on both new pollers, so `r` refreshes the queue one
tick later. Matching `prPoller`: dropping the store instead would blank the section for a
tick, and there is no reason to prefer that.

## Testing

The repo's standing warning applies with full force here: across two plans, **ten briefs have
contained tests that would have passed with their subject deleted**, and most were written by
the plan's author. Every test below is to be watched failing before its subject exists, and
the plan records the specific mutation each one catches.

- **`gh` argv, exactly**, including `--` placement, with a dedicated case pinning that
  `-is:draft` survives it. This is the assertion that catches the cobra parse failure, and it
  is worthless if it only checks that `gh` was called.
- **`short api` path is URL-encoded**, and the JSON parse succeeds with stderr noise present.
- **Dedup by name prefix and by `PR.Number` tested independently**, so neither can carry the
  other. A single combined fixture would let the name-prefix rule rot invisibly.
- **`queue_enabled=false` issues zero `gh` and zero `short` calls** - an absence assertion,
  so it needs a positive control in the same fixture or it goes vacuous the moment the stub
  Commander's format strings drift. See `TestPollIssuesNoGhCalls` in the deferred list of the
  collector async remote handoff for the same defect, already recorded.
- **A daemon-fed client spends no queue budget.** Mirrors `TestADaemonFedClientSpendsNoGhBudget`
  and must go through a real `Collector.Start`, not `RefreshRemote`: per that handoff's
  landmine, a nudge that never reaches a worker leaves every `RefreshRemote`-driven test
  green.
- **`enter` on a queue row submits with `Detached` true**; on a session row, unchanged;
  `m`/`a`/`c`/`D` on a queue row are no-ops.
- **Golden render** for the QUEUE section, and the badge present at 152 columns and absent at
  a width where it cannot fit.

Any test in this set that passes before its subject is written is a defect in the plan, not a
happy accident.

## What this does not do

- **It does not address the `fillGit` finding.** ~3s per poll, at or above `git_interval`, so
  the git memo never skips and real publication cadence is ~3s rather than 1s. Phase 5's
  pollers sit behind the seam and add nothing to `Snapshot`, so they neither cause nor worsen
  it. The precedent is on this side: asserted effect ownership and the async remote layer both
  landed as their own work ahead of the phase that needed them rather than inside it. This
  wants the same treatment.
- **It does not fix `ExecCommander.Run`'s grandchild-holds-the-pipe defect.** Phase 4 fixed
  `RunStream`; `Run` - used by the `notify` and `cleanup` hooks and by `FetchPRStatus` - still
  has it, and the two new pollers use it too. Shipped defaults do not background anything,
  which is the only reason it is still acceptable. Phase 5 inherits it knowingly.
- **It does not touch `~/dotfiles`.** The first phase since phase 2 that is single-repo.

## Landmines

- **Dedup hardcodes a dotfiles naming convention that vigil currently parses nowhere.**
  `SC-<id> ` / `PR-<number> ` comes from `session_name_from_title`, in a different
  repository, with no test on either side tying them together. If that format changes, dedup
  degrades **silently** and the queue starts advertising work already in flight. The
  secondary `PR.Number` key covers reviews; **stories would be fully exposed.** A test pins
  the exact prefix format, which is a tripwire on this side only.
- **`queue_pr_age_days` computes a date at poll time.** A daemon running across midnight
  recomputes it on the next pass, which is correct but means the queue's contents can change
  with no user action and no state change. Expected; worth knowing before diagnosing it as a
  bug.
- **A missing or broken `short` is completely silent.** The story half of the queue is empty
  and **nothing anywhere says why** - not a toast, not a status line, not the daemon log,
  because the pollers do not log a failed pass and neither does `prPoller`. "No assigned
  stories" and "Shortcut is unreachable" render identically. This is the cost of both
  deliberate choices - no startup check, and consistency with `prPoller` - and it is the most
  likely thing to waste someone's afternoon. A first diagnostic step is to run the poller's
  exact command by hand: `short api "/search/stories?query=<your queue_story_query>"`.
- **Two more subprocess classes now run on worker goroutines** and are waited on by
  `Collector.Wait()` before the daemon releases its flock and unlinks its socket. A wedged
  `short` has the same consequence as a wedged `gh`: `Run` never returns and no daemon can
  start again. Same pre-existing shape, one more way to reach it.
- **`Request.Detached` is additive but its failure mode is silent.** An old daemon ignores it
  and teleports. There is no refusal, no log line and no toast - the user simply gets moved.
  Self-limiting once panels re-exec, but the first install cannot re-exec anything.

## Rejected alternatives

**Queue rows in the panel.** The panel is 152x9 with five sessions already live. Any variant -
a section, a collapsed section, rows below the fold reachable by scrolling - either displaces
sessions or is invisible. A status-bar badge conveys the same "there is work waiting" in zero
rows. Rejected on measurement, not taste.

**The menu bar presents the queue.** The cockpit design says both vigil and the menu bar
should. The user does not want it, now or ever, so it is cut - and with it `vigil queue
--json`, which existed only to feed it. `~/scripts/dispatch-bar` keeps doing Chrome-tab
dispatch and phase 5 does not touch `~/dotfiles`. Recorded here so a future reader treats
this as a decision rather than an oversight. Phase 6's "delete one of the two menu bar
implementations" resolves independently: SwiftBar is not installed, nothing runs
`dispatch.1d.sh`, and the native `dispatch-bar` is live under
`com.user.dispatch-bar.plist`. The SwiftBar plugin is the dead one.

**Separate focus region with `tab`.** A clean boundary - no key would mean two things and no
action would need a row-kind guard. Rejected because it adds a mode to a TUI that has only
ever had one list, to save a guard in four action handlers.

**Queue rows joinable in multi-select.** Marking three reviews and dispatching them all
detached is genuinely attractive, and the daemon already serialises jobs so a batch is just an
ordered queue. Rejected for now because it requires a mixed-selection guard on every existing
batch action, and the single-select path has to exist first either way. Cheap to add later.

**Hitting the Shortcut and GitHub APIs over HTTP directly.** Faster than a subprocess and
avoids a node process spawn per poll. Rejected because every subprocess in this codebase goes
through `fetch.Commander` and there is no network code anywhere in it; introducing an HTTP
client means a second stubbing seam for one caller. `short api` at 0.69s on a worker goroutine
at a 60s interval is not a cost worth a new dependency class.

**A second `dispatch_detached` hook** instead of `{flags}`. It ships with a working default,
so the user edits nothing. Rejected because two hook strings drift: customising `dispatch`
alone would silently leave the detached variant on the old shape, producing a teleport the
user did not ask for and cannot easily explain. One string with a placeholder costs a one-line
config edit once.

**Auto-dispatching queue items**, as `gh-review-poll` did. It is the reason that script is
orphaned. Automatic session creation from a remote poll means work appearing in the session
list that the user never asked for, at a moment they did not choose - the precise interruption
this design was shaped to avoid.

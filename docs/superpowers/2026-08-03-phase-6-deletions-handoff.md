# Phase 6: deletions, and the limit of what verified them

Written 2026-08-03 with the dotfiles side of `phase-6-deletions` at `711b7d9` (4 commits from
`master` at `3225047`) and this vigil docs commit on the matching branch here, both pending the
whole-branch review and merge. `make test` and `make lint` green on all 14 vigil packages -
expected, since this phase touches no Go code.

- Design: `docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md:157-165` (the five-item
  deletion list phase 6 executes)
- Plan: `docs/superpowers/plans/2026-08-03-phase-6-deletions.md`
- Ledger: `.superpowers/sdd/2026-08-03-phase-6-deletions/progress.md`

**The most important thing on this page is the verification-limit section below.** This phase
deleted four scripts and no test suite noticed any of them, which is expected and does not mean
the deletions were unchecked - it means the checking was manual and none of it is automated.
Read that section before trusting a green `make lint` on the dotfiles side as evidence of
anything beyond "the Makefile's script list matches the files on disk."

## What shipped

Four scripts deleted from `~/dotfiles/scripts/scripts/`, three `Makefile` `SHELL_SCRIPTS`
entries removed, two `.tmux.conf` bindings removed, one state directory removed outside git:

| Script | Commit | Consumer before deletion |
|---|---|---|
| `tmux-monitor` | `4fab071` | none - orphaned, superseded by vigil |
| `dispatch.1d.sh` | `4fab071` | SwiftBar, which is not installed |
| `worktree-status` | `8331fe7` | `prefix w` / `prefix C-w` bindings |
| `gh-review-poll` | `711b7d9` | a cron/launchd-style poller, cold since 2026-05-09 |

`gh-review-poll`'s `~/.local/state/gh-review-poll/` (log + seen file, ~1.2MB) was removed
outside git in the same task. Nothing else changed in either repository - **`Snapshot.go`,
every Go package, and `~/dotfiles/scripts/scripts/gh-review` are byte-for-byte untouched.**

Each of Tasks 1-3 ran the same shape: confirm no reference outside the Makefile, delete, run
`make lint` and **watch it fail** (shellcheck's `openBinaryFile: does not exist` naming the
missing file, exit 2 - this is the mutation check, and it fired for real every time), remove
the Makefile entry, confirm `make lint && make test` pass, commit. Full verbatim output for
each mutation check is in the corresponding task report under
`.superpowers/sdd/2026-08-03-phase-6-deletions/`.

## The prerequisite: closed by real use, not by this plan

CLAUDE.md had gated `gh-review-poll`'s deletion on living on a real queue dispatch, because its
`--detached --non-interactive` invocation was the only production evidence that the workflow
scripts honour `--detached` at all. Phase 5 shipped without ever running one - see that
handoff's "What was NOT verified".

That evidence now exists, measured on the live machine before this plan was written:

| Observation | Value |
|---|---|
| Queue rendering | 10 items, `queue_hidden: 5` |
| Live sessions covering hidden items | `SC-223374`, `SC-223479`, `PR-35033`, `PR-35035`, `PR-35037` - exactly 5 |
| `PR-35035` session created | 28s before the reading |
| `PR-35037` session created | 9s before the reading |
| Attached clients throughout | one, `session=main` |

Both dispatches were user-confirmed to have gone through `enter` on a queue row - the
`Detached = true` path. Five sessions were created and the one attached client never left
`main`, so `--detached` reached `run_worktree_popup` and suppressed `teleport_client_to`. The
phase 5 `%self%` fix is live in the same reading: `short api /member` resolves to `joshuazd`,
both assigned stories are found, and both are hidden by their own sessions - dedup exercised on
both keys at once, name-prefix for the two `SC-` sessions and both name-prefix and `PR.Number`
for the three `PR-` sessions.

`gh-review-poll` was also confirmed cold before deletion: not running, pid file stale,
`~/.local/state/gh-review-poll/log` last written 2026-05-09, `seen` 2026-05-07.

## Two items on the original list needed no work

Checked 2026-08-03, before writing the plan, so a future session does not re-derive them:

- **The popup tunnel inside `dispatch-from-chrome` was already gone**, removed in phase 4.
  `dispatch-from-chrome:9` reads "No popup is opened here" and the script calls `vigil dispatch`
  directly.
- **"One of the two menu bar implementations" resolves to `dispatch.1d.sh`.** SwiftBar is not
  installed - `~/Library/Application Support/SwiftBar/` does not exist, not even a dangling
  plugin symlink - and the native `dispatch-bar` was confirmed running (PID 862) under
  `~/Library/LaunchAgents/com.user.dispatch-bar.plist`. `dispatch-bar.swift` and the compiled
  binary stay; they are the surviving implementation, not a second target.

## What was deliberately NOT deleted

- **`lib/tmux.sh:611`, the `display-popup` branch of `run_worktree_popup`, taken whenever
  `DISPATCH_INLINE` is unset.** This is every manual `gh-review <url>` and
  `shortcut-implement <id>` run from a terminal - both call `run_worktree_popup`
  (`gh-review:188`, `shortcut-implement:188`) directly, not through vigild's dispatch queue.
  The design preserved this path on purpose: "The standalone `dispatch` CLI keeps working
  unchanged for direct terminal use"
  (`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md:149`). Deleting it would silently
  convert every manual run's "press Enter to close" popup into inline output dumped in the
  caller's own pane. Phase 6's popup-deletion item was the `dispatch-from-chrome` tunnel above,
  already gone via phase 4 - not this branch. **If a future session looks at this code and
  thinks "phase 6 missed one," it did not. Leave it.**
- **`~/dotfiles/.nit.json:58-59`** still has two `worktree-status` strings
  (`bind-key w display-popup ... worktree-status`, twice). It is `hunk_context` /
  `line_content` inside a stored code-review comment from March - a record of what the file
  said at the time, not configuration. Rewriting it would falsify that record. Note also that
  `.nit.json` is gitignored (`.cvsignore`) and untracked, so it exists only in the main
  `~/dotfiles` checkout, not in the `~/dotfiles-phase6` worktree this plan worked in - it was
  read directly from the live checkout, not from a diff.
- **`~/vigil/python/src/vigil/widgets.py:429`**: `"""Apply colors to PR display string. Matches
  tmux-monitor palette."""`. Historical attribution inside the legacy Python implementation,
  which is not the product anymore. Left as-is for the same reason as the `.nit.json` lines:
  it documents a past fact, not a current dependency.

## The `worktree-status` story-column loss, which is real

The design called `worktree-status` "superseded by the panel." True for three of its four
columns, false for the fourth:

| `worktree-status` column | vigil equivalent |
|---|---|
| Session | session name column |
| Branch | git column |
| Git (dirty / N unpushed) | git column |
| **Story (Shortcut story state from `sc-NNNN` in the branch)** | **none** |

vigil's session table renders Indicator, Index, Name, Git, PR, State
(`internal/view/table.go` `renderRow`, `internal/view/layout.go` `TableLayout`), and
`session.Session` carries no story field. The work queue shows stories, but only
*assigned-and-not-done* ones, and hides any story a live session already covers - which is
exactly the set a session-status view would be asked about. Deleting `worktree-status` loses
the ability to see, for an existing session, what state its Shortcut story is currently in.

**Remaining ways to get that information: `short story <id>`, or the Shortcut web UI.** This
phase accepted the loss rather than adding a story column to vigil, because that is feature
work and this phase is deletions. Recorded here and in CLAUDE.md's "Key Conventions" as an open
follow-up candidate, not as a silent regression.

## The honest limit of this phase's verification

**No bats test covers any of the four deleted scripts.** Confirmed by
`grep -rnE 'tmux-monitor|worktree-status|gh-review-poll' scripts/scripts/tests/`, which returns
nothing, and `dispatch.1d.sh` was never a bats subject either. This means the suite could not
have caught a wrong deletion, and - the point worth stating plainly - **it also means the suite
proves nothing about these deletions being safe.** `make lint`'s pass/fail only tells you the
`SHELL_SCRIPTS` list matches the files on disk; it has no opinion on whether a deleted script
was still wanted.

Every other safety claim in this phase rests on a manual reference sweep: `grep -rn <name>` over
both repositories, `~/Library`, `~/.local/state` and the live tmux server, repeated per task and
then again across all four names at once in the post-merge final sweep
(`docs/superpowers/plans/2026-08-03-phase-6-deletions.md`, Task 5). That is a real check, and it
is not a test - it does not run again on the next commit, and it depends on a human or an agent
remembering to re-run it. **Given this repository's documented history of tests that would pass
with their subject deleted (nineteen briefs across three plans as of the phase 5 handoff), the
correct reading of this phase's green `make lint`/`make test` is "nothing regressed that either
suite happens to cover," not "these deletions were validated."** They were validated by the
reference sweeps, which are recorded above and in the per-task reports, not by CI.

Task 2's live-server unbind (`tmux unbind-key w`, `tmux unbind-key C-w`) is post-merge by
design - `~/.tmux.conf` and `~/scripts` are symlinks into the main `~/dotfiles` checkout, so the
unbind cannot happen until `master` carries the change. As of this writing it has not run; the
baseline bindings it will remove are recorded verbatim in
`.superpowers/sdd/2026-08-03-phase-6-deletions/task-2-report.md`.

## Landmines

- **`lib/tmux.sh:611`'s `display-popup` branch looks like leftover phase 6 work and is not.**
  See "What was deliberately NOT deleted" above. This is the single most likely thing for a
  future cleanup pass to break.
- **A skipped Task 2 Step 8 (the live unbind) leaves `prefix w` bound to a deleted file.** The
  failure mode is a popup that flashes a shell error, not silence, which is the only reason this
  is not treated as blocking. `tmux source-file` does not remove a binding absent from the file
  it reads - only an explicit `unbind-key` does.
- **`gh-review-poll`'s deletion removed the only production evidence for `--detached`
  honouring**, and it was deliberately replaced before deletion rather than after. If a future
  regression breaks `--detached` silently, there is no longer a second, independent caller that
  would have surfaced it by drifting out of sync - the queue path is now the only user.
- **`.nit.json`'s `worktree-status` references are a historical record, not configuration, and
  are gitignored.** A sweep with `--include-dir=.git` won't find it as untracked; the two lines
  were found by reading the live file directly.

## Deferred

- **Adding a Shortcut story-state column to vigil's session table**, closing the
  `worktree-status` gap. Feature work, out of scope for a deletions phase. The queue's story
  section is the closest existing surface but filters to assigned-and-not-done and dedups
  against live sessions, so it cannot substitute directly.
- **The `fillGit` publication-cadence issue** (collector async remote handoff) is untouched by
  this phase and remains the standing candidate for the next structural work, ahead of whatever
  needs a more responsive session list.
- **The session table has no viewport** and **`queueRowBudget` starves the session table below
  a full queue** (both from the phase 5 handoff) are untouched by this phase.

## Process notes

This phase had the smallest blast radius of any phase in the design - four file deletions, no
Go code - and correspondingly the least drama. Two things worth recording anyway:

1. **A "should NOT be done" section in the plan earned its keep.** The plan explicitly flagged
   `lib/tmux.sh:611` as a target that looks deletable and isn't, before any task touched it. No
   task then tried to delete it. Naming the near-miss in advance, rather than trusting a
   reviewer to catch it after the fact, is cheaper than the alternative - this phase's plan did
   that in one place and it held.
2. **The two "already resolved" list items were worth checking explicitly rather than assuming
   done.** Both `dispatch-from-chrome`'s tunnel and the SwiftBar-vs-native menu bar question
   were verified fresh on 2026-08-03 rather than trusted from memory of what phase 4 did. The
   SwiftBar check in particular (`ls` the plugins dir, `pgrep` both processes) is the kind of
   three-line check that costs nothing and forecloses "wait, is SwiftBar actually gone?" as a
   review question later.

No mutation-check failures, no vacuous tests, no whole-branch surprises to report here - the
absence of findings in a phase this small is itself unremarkable, not evidence of unusual rigor.

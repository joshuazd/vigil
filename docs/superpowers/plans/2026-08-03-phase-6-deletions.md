# Phase 6: deletions - implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the four superseded shell scripts phase 6 names, plus the two `.tmux.conf`
bindings and the Makefile entries that reference them, without leaving a dangling reference or
a live tmux binding pointing at a deleted file.

**Architecture:** There is no vigil code in this phase. Every deletion is in `~/dotfiles`; the
only change in `~/vigil` is documentation. That makes phase 6 the mirror image of phase 5,
which was vigil-only - so unlike phases 0-4, **neither half depends on the other**, and the
dotfiles half can merge alone.

**Tech Stack:** bash, shellcheck, bats, tmux, git worktrees.

## Global Constraints

- **`~/scripts` and `~/.tmux.conf` are symlinks into the main `~/dotfiles` checkout**
  (`~/scripts -> dotfiles/scripts/scripts`, `~/.tmux.conf -> dotfiles/tmux/.tmux.conf`).
  Editing the main checkout removes live tooling the instant it is saved. Per the phase 4
  landmine, do all shell work in a separate worktree: `~/dotfiles-phase6`. An interrupted
  agent editing the live path broke dispatch once already.
- **Never delete a script in the same commit-order position as its Makefile entry without
  observing the failure in between.** `SHELL_SCRIPTS` in `scripts/scripts/Makefile` names each
  script literally, so a deleted file with a live entry makes `make lint` fail. That failure is
  this plan's mutation check - it is the one place a deletion is *observably* wrong, and every
  task below requires seeing it before fixing it. Do not reorder those steps.
- **`tmux source-file` does not remove a deleted binding.** Sourcing applies what is in the
  file; it does not unbind what is absent from it. A running server keeps `prefix w` bound to a
  deleted script until an explicit `unbind-key`. Task 2 depends on this.
- Commit messages follow the repo's existing convention: `chore(scripts): ...` for deletions.
- `make lint` and `make test` in `scripts/scripts` must both pass at the end of every task, not
  only at the end of the plan.

## Prerequisite: satisfied 2026-08-03, by real use

CLAUDE.md gated `gh-review-poll`'s deletion on living on a real queue dispatch, because its
`--detached --non-interactive` invocation was the only production evidence that the workflow
scripts honour `--detached`. Phase 5 never ran one (see that handoff's "What was NOT
verified").

**That evidence now exists.** Measured on the live machine before this plan was written:

| Observation | Value |
|---|---|
| Queue rendering | 10 items, `queue_hidden: 5` |
| Live sessions covering hidden items | `SC-223374`, `SC-223479`, `PR-35033`, `PR-35035`, `PR-35037` - exactly 5 |
| `PR-35035` session created | 28s before the reading |
| `PR-35037` session created | 9s before the reading |
| Attached clients throughout | one, `session=main` |

Both dispatches were confirmed by the user to have gone through `enter` on a queue row, which
is the `Detached = true` path. Five sessions were created and **the client never left `main`**,
so `--detached` reached `run_worktree_popup` and suppressed `teleport_client_to`. The
`%self%` fix is live in the same reading: `short api /member` resolves to `joshuazd`, both
assigned stories are found, and both are hidden by their own sessions.

Dedup was exercised on both keys at once - the two `SC-` sessions by name prefix, the three
`PR-` sessions by name prefix and `PR.Number`.

`gh-review-poll` is also cold: not running, `~/.local/state/gh-review-poll/log` last written
2026-05-09, `seen` 2026-05-07.

## Two items on phase 6's list are already resolved

Do not look for work here. Both were checked on 2026-08-03.

- **The popup tunnel inside `dispatch-from-chrome` is already gone**, removed in phase 4.
  `dispatch-from-chrome:9` now reads "No popup is opened here" and the script calls
  `vigil dispatch` directly. Nothing to delete.
- **"One of the two menu bar implementations" resolves to `dispatch.1d.sh`.** SwiftBar is not
  installed - `~/Library/Application Support/SwiftBar/` does not exist, so there is not even a
  dangling plugin symlink - and the native `dispatch-bar` is live as PID 862 under
  `~/Library/LaunchAgents/com.user.dispatch-bar.plist`. `dispatch-bar.swift` and the compiled
  `dispatch-bar` stay.

## One item on phase 6's list should NOT be done

The residual popup code is `run_worktree_popup`'s `tmux display-popup` branch,
`scripts/scripts/lib/tmux.sh:611`, taken whenever `DISPATCH_INLINE` is unset. That is every
manual `gh-review <url>` and `shortcut-implement <id>` run from a terminal - both call
`run_worktree_popup` (`gh-review:188`, `shortcut-implement:188`).

The design preserved that path deliberately: "The standalone `dispatch` CLI keeps working
unchanged for direct terminal use"
(`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md:149`). Deleting the branch would
silently convert every manual run from a popup with a "Press Enter to close" prompt into inline
output in the current pane. **Leave it.** Phase 6's popup item was about the
`dispatch-from-chrome` tunnel, which phase 4 already removed.

## `worktree-status` is not fully superseded, and this is the one real loss

The design says `worktree-status` is "superseded by the panel". That is true for three of its
four columns and false for the fourth.

| `worktree-status` column | vigil equivalent |
|---|---|
| Session | session name column |
| Branch | git column |
| Git (dirty / N unpushed) | git column |
| **Story (Shortcut story state from `sc-NNNN` in the branch)** | **none** |

vigil's session table renders Indicator, Index, Name, Git, PR, State (`internal/view/table.go`
`renderRow`, `internal/view/layout.go` `TableLayout`). There is no Shortcut story state column
and no field carrying one on `session.Session`. The queue shows stories, but only *assigned and
not-done* ones, and it hides any story a live session already covers - which is the exact set a
session-oriented status table would be asked about.

So deleting `worktree-status` loses the ability to see, for an existing work session, what
state its Shortcut story is in. The remaining ways to get it are `short story <id>` and the
Shortcut web UI.

**This plan accepts the loss** rather than adding a story column to vigil, because that is
feature work and this phase is deletions. It is recorded in Task 4's handoff as an explicit
follow-up candidate, not as a silent regression. If the user would rather keep
`worktree-status` until vigil grows a story column, skip Task 2 - nothing else in the plan
depends on it.

## Edit sites

Every reference to a deletion target, found by sweeping both repositories, `~/Library`,
`~/.local/state` and the live tmux server on 2026-08-03.

**Delete outright** (`~/dotfiles/scripts/scripts/`):

| File | Referenced by |
|---|---|
| `tmux-monitor` | `Makefile:10` only |
| `dispatch.1d.sh` | `Makefile:6` only |
| `worktree-status` | `Makefile:10`, `tmux/.tmux.conf:29-30`, live tmux bindings |
| `gh-review-poll` | `Makefile:7` only, plus `~/.local/state/gh-review-poll/` |

**Modify:**

- `~/dotfiles/scripts/scripts/Makefile:5-10` - the `SHELL_SCRIPTS` list, four entries.
- `~/dotfiles/tmux/.tmux.conf:29-30` - the two `worktree-status` bindings.
- `~/vigil/CLAUDE.md` - the in-flight design section.

**Deliberately not modified:**

- `~/dotfiles/.nit.json:58-59` - the two `worktree-status` strings there are `hunk_context` and
  `line_content` inside a stored code-review comment from March. It is a review artifact, not
  configuration; rewriting it would falsify a record of what the file said at the time.
- `~/vigil/python/src/vigil/widgets.py:429` - the comment "Matches tmux-monitor palette" in the
  legacy Python implementation. Historical attribution in code that is no longer the product.
- `~/dotfiles/scripts/scripts/lib/tmux.sh:611` - see the section above.

**No bats test covers any of the four scripts.** Verified: `grep -rnE
'tmux-monitor|worktree-status|gh-review-poll' scripts/scripts/tests/` returns nothing. This
means the suite cannot break, and equally that **the suite proves nothing about these
deletions.** `make lint` is the only automated guard, and it only guards the Makefile list.
Every other check in this plan is a manual reference sweep. Do not report "tests pass" as
evidence that a deletion was safe.

---

### Task 0: Set up the worktree

**Files:** none modified.

- [ ] **Step 1: Create the worktree**

```bash
cd ~/dotfiles && git worktree add ~/dotfiles-phase6 -b phase-6-deletions master
```

- [ ] **Step 2: Confirm the live symlinks still point at the main checkout, not the worktree**

```bash
ls -la ~/scripts ~/.tmux.conf
```

Expected: `~/scripts -> dotfiles/scripts/scripts` and `~/.tmux.conf -> dotfiles/tmux/.tmux.conf`.
Neither may mention `dotfiles-phase6`. If either does, stop - the isolation this task exists
for is not there.

- [ ] **Step 3: Confirm the baseline is green before deleting anything**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint && make test
```

Expected: both pass. A pre-existing failure here must be understood before proceeding, or the
mutation checks in later tasks cannot be read.

There is nothing to commit in this task.

---

### Task 1: Delete the two pure orphans - `tmux-monitor` and `dispatch.1d.sh`

Grouped because both are referenced only by the Makefile, neither has a live consumer, and a
reviewer has no basis to accept one and reject the other.

**Files:**
- Delete: `~/dotfiles-phase6/scripts/scripts/tmux-monitor`
- Delete: `~/dotfiles-phase6/scripts/scripts/dispatch.1d.sh`
- Modify: `~/dotfiles-phase6/scripts/scripts/Makefile:6,10`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing. No later task reads anything from this one.

- [ ] **Step 1: Re-confirm nothing outside the Makefile references either script**

```bash
cd ~/dotfiles-phase6 && grep -rn -E "tmux-monitor|dispatch\.1d" --exclude-dir=.git . \
  | grep -vE "^\./scripts/scripts/(tmux-monitor|dispatch\.1d\.sh):"
```

Expected, exactly these two lines and nothing else:

```
./scripts/scripts/Makefile:6:	claude-trust dispatch dispatch-from-chrome dispatch.1d.sh \
./scripts/scripts/Makefile:10:	tmux-monitor ts vigil-panel worktree-status
```

Plus `./scripts/scripts/CLAUDE.md` may mention `dispatch-bar`; that is a different string and
is fine. Any other hit means this task's premise is wrong - stop and report.

- [ ] **Step 2: Confirm SwiftBar is genuinely absent, so `dispatch.1d.sh` has no consumer**

```bash
ls ~/Library/Application\ Support/SwiftBar/Plugins/ 2>&1
pgrep -fl SwiftBar || echo "SwiftBar not running"
pgrep -fl dispatch-bar || echo "dispatch-bar NOT running"
```

Expected: the plugins directory does not exist, SwiftBar is not running, and **`dispatch-bar`
IS running**. If `dispatch-bar` is not running, stop: deleting the SwiftBar plugin while the
native replacement is also down leaves no menu bar dispatch at all.

- [ ] **Step 3: Delete both files**

```bash
cd ~/dotfiles-phase6/scripts/scripts && git rm tmux-monitor dispatch.1d.sh
```

- [ ] **Step 4: Run lint and WATCH IT FAIL - this is the mutation check**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint
```

Expected: **FAIL**, exit 2, with a line per deleted file of the form:

```
dispatch.1d.sh: dispatch.1d.sh: openBinaryFile: does not exist (No such file or directory)
```

Confirmed on 2026-08-03 against the real shellcheck with this Makefile's exact flags: a missing
file in the argument list exits 2 and names the file. Paste the real output into the task
report.

If this passes, the Makefile is not actually driving shellcheck over these files and this
plan's only automated guard is vacuous - stop and report that finding, do not proceed.

- [ ] **Step 5: Remove both entries from `SHELL_SCRIPTS`**

`Makefile:6` becomes:

```make
	claude-trust dispatch dispatch-from-chrome \
```

`Makefile:10` becomes:

```make
	ts vigil-panel worktree-status
```

- [ ] **Step 6: Run lint and test to verify both pass**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint && make test
```

Expected: both PASS.

- [ ] **Step 7: Commit**

```bash
cd ~/dotfiles-phase6 && git add -A scripts/scripts && git commit -m "chore(scripts): delete tmux-monitor and the SwiftBar dispatch plugin

tmux-monitor is superseded by vigil. dispatch.1d.sh is the SwiftBar half of
the menu bar; SwiftBar is not installed and the native dispatch-bar is live
under com.user.dispatch-bar.plist, so it has had no consumer for months.

Neither was referenced outside the Makefile's shellcheck list."
```

---

### Task 2: Delete `worktree-status` and unbind `prefix w` / `prefix C-w`

Separate from Task 1 because it is the only deletion with a live consumer, the only one that
needs a running-server change, and the only one with a capability loss a reviewer might reject
on (see "`worktree-status` is not fully superseded" above).

**Files:**
- Delete: `~/dotfiles-phase6/scripts/scripts/worktree-status`
- Modify: `~/dotfiles-phase6/scripts/scripts/Makefile:10`
- Modify: `~/dotfiles-phase6/tmux/.tmux.conf:29-30`

**Interfaces:**
- Consumes: the `Makefile:10` line as Task 1 left it - `ts vigil-panel worktree-status`.
- Produces: nothing.

- [ ] **Step 1: Record the live bindings before touching anything**

```bash
tmux list-keys -T prefix | grep -E "^bind-key +-T prefix +(w|C-w) "
```

Expected, two lines pointing at `/Users/joshua.zink-duda/scripts/worktree-status`. Paste them
into the report - Step 8 asserts they are gone, and that assertion is meaningless without this
baseline.

- [ ] **Step 2: Confirm `prefix v` covers the replacement path**

```bash
tmux list-keys -T prefix | grep -E "^bind-key +-T prefix +v "
```

Expected: `display-popup -E -h "80%" -w "90%" vigil`. This is the surface the design says
supersedes `worktree-status`. If it is absent, stop - do not delete the old tool while its
replacement is unbound.

- [ ] **Step 3: Delete the script and remove its Makefile entry**

```bash
cd ~/dotfiles-phase6/scripts/scripts && git rm worktree-status
```

`Makefile:10` becomes:

```make
	ts vigil-panel
```

- [ ] **Step 4: Remove the two bindings from the config**

Delete these two lines from `~/dotfiles-phase6/tmux/.tmux.conf` (lines 29-30):

```tmux
bind-key w display-popup -E -w 120 -h 40 "$HOME/scripts/worktree-status"
bind-key C-w display-popup -E -w 120 -h 40 "$HOME/scripts/worktree-status"
```

Leave the `bind-key v` / `bind-key C-v` vigil lines immediately below them untouched.

- [ ] **Step 5: Verify no reference survives in the worktree**

```bash
cd ~/dotfiles-phase6 && grep -rn "worktree-status" --exclude-dir=.git .
```

Expected: only `./.nit.json`, on the two lines documented as a stored review artifact. Any hit
under `scripts/` or `tmux/` means the edit is incomplete.

- [ ] **Step 6: Run lint and test**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint && make test
```

Expected: both PASS. Note that `make lint` passing here is weak evidence - it only proves the
Makefile list matches the files on disk. The tmux binding is not covered by any test.

- [ ] **Step 7: Commit**

```bash
cd ~/dotfiles-phase6 && git add -A scripts/scripts tmux && git commit -m "chore(scripts): delete worktree-status and its prefix+w bindings

Superseded by the vigil panel and prefix+v for its session, branch and git
columns. Its Shortcut story column has no vigil equivalent and is a real
loss, recorded in the phase 6 handoff as follow-up rather than replaced
here.

Sourcing this file does not unbind a removed binding, so the running server
needs an explicit unbind-key; that is a post-merge step, not part of this
commit."
```

- [ ] **Step 8: After the branch is merged and only then, unbind on the live server**

This step cannot run before the merge: `~/.tmux.conf` and `~/scripts` point at the main
checkout, so until `master` has the change the file still contains the bindings and the script
still exists.

```bash
tmux unbind-key w
tmux unbind-key C-w
tmux source-file ~/.tmux.conf
tmux list-keys -T prefix | grep "worktree-status" \
  && echo "STILL BOUND - investigate" || echo "unbound as intended"
```

Expected: `unbound as intended`. The `source-file` is there to prove sourcing does not
resurrect them, which is the failure mode if `unbind-key` were relied on alone in a config
that still had the lines.

**Grep for the target (`worktree-status`), not the key.** tmux binds `w` to a built-in
("Choose the current window interactively") by default, so once this config no longer
overrides it, `w` shows up bound to something on any fresh server - a key-name grep would print
"STILL BOUND - investigate" forever, even after a correct unbind. Worse, `tmux unbind-key w` on
*this* running server leaves `w` truly dead, while a fresh server picks up tmux's default - the
two states diverge permanently, so a key-name check can never agree with itself across a
restart. Checking for the deleted script's name is the only assertion that means what it
should: nothing points at `worktree-status` any more.

---

### Task 3: Delete `gh-review-poll` and its state directory

**Files:**
- Delete: `~/dotfiles-phase6/scripts/scripts/gh-review-poll`
- Modify: `~/dotfiles-phase6/scripts/scripts/Makefile:7`
- Delete (outside git): `~/.local/state/gh-review-poll/`

**Interfaces:**
- Consumes: nothing from earlier tasks except the Makefile's current shape.
- Produces: nothing.

- [ ] **Step 1: Confirm it is not running and holds no live pid**

```bash
pgrep -fl gh-review-poll || echo "not running"
cat ~/.local/state/gh-review-poll/pid 2>/dev/null
ps -p "$(cat ~/.local/state/gh-review-poll/pid 2>/dev/null)" 2>/dev/null \
  || echo "pid file is stale"
ls -la ~/.local/state/gh-review-poll/
```

Expected: not running, and the pid in the file belongs to no live process. If a live process
owns that pid, stop it with `gh-review-poll stop` **before** deleting the script - the stop
subcommand is in the file you are about to remove.

- [ ] **Step 2: Re-confirm the `--detached` evidence still holds**

The prerequisite section records this from 2026-08-03. Re-check it rather than trusting the
page, because it is the entire justification for this task:

```bash
grep -n "detached" ~/dotfiles-phase6/scripts/scripts/gh-review-poll
grep -n "detached" ~/dotfiles-phase6/scripts/scripts/gh-review \
  ~/dotfiles-phase6/scripts/scripts/shortcut-implement
```

Expected: `gh-review-poll` passes `--detached --non-interactive`, and both workflow scripts
still parse `--detached` and use it only to gate `teleport_client_to`. The queue path exercises
the same flag through the same function, which is why deleting the old caller costs no
coverage.

- [ ] **Step 3: Confirm no reference outside the Makefile**

```bash
cd ~/dotfiles-phase6 && grep -rn "gh-review-poll" --exclude-dir=.git . \
  | grep -v "^\./scripts/scripts/gh-review-poll:"
```

Expected: exactly `./scripts/scripts/Makefile:7`. In particular there must be no hit in
`gh-review` itself - the poller calls the workflow, not the reverse.

- [ ] **Step 4: Delete the script**

```bash
cd ~/dotfiles-phase6/scripts/scripts && git rm gh-review-poll
```

- [ ] **Step 5: Run lint and WATCH IT FAIL**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint
```

Expected: **FAIL**, exit 2, with:

```
gh-review-poll: gh-review-poll: openBinaryFile: does not exist (No such file or directory)
```

Paste the output.

- [ ] **Step 6: Remove the entry from `SHELL_SCRIPTS`**

`Makefile:7` becomes:

```make
	gh-review gh-worktree \
```

- [ ] **Step 7: Run lint and test to verify both pass**

```bash
cd ~/dotfiles-phase6/scripts/scripts && make lint && make test
```

Expected: both PASS.

- [ ] **Step 8: Commit the script deletion**

```bash
cd ~/dotfiles-phase6 && git add -A scripts/scripts && git commit -m "chore(scripts): delete gh-review-poll, superseded by vigil's work queue

Phase 5's queue polls review-requested PRs and dispatches a selection
detached, which is what this script did on a timer. Cold since 2026-05-09.

Its --detached --non-interactive invocation was the only production evidence
that the workflow scripts honour --detached. That evidence now comes from the
queue path instead: verified 2026-08-03 with five sessions created from queue
rows and the only attached client never leaving its own session."
```

- [ ] **Step 9: Remove the state directory**

Not in git, so it needs its own step. Read it before removing it, per the standing
instruction to look at a delete target.

```bash
tail -5 ~/.local/state/gh-review-poll/log
wc -l ~/.local/state/gh-review-poll/seen
\rm -rf ~/.local/state/gh-review-poll
ls ~/.local/state/ | grep gh-review-poll && echo "STILL THERE" || echo "removed"
```

Expected: `removed`. Use `\rm` to bypass the interactive `rm` alias, per the user's global
instructions.

---

### Task 4: Update vigil's documentation

The only change in the vigil repository. It runs last so it can describe what actually
happened rather than what was planned.

**Files:**
- Modify: `~/vigil/CLAUDE.md` - the "In-flight design work" section
- Create: `~/vigil/docs/superpowers/2026-08-03-phase-6-deletions-handoff.md`

**Interfaces:**
- Consumes: the outcome of Tasks 1-3, including any deviation.
- Produces: the handoff a phase 7 session reads first.

- [ ] **Step 1: Rewrite CLAUDE.md's in-flight section**

Phase 6's bullet list of deletion targets is now history and must not read as pending work.
Required content:

- All six phases merged; name phase 6's merge commit in each repo.
- Phase 6 was **dotfiles-only plus vigil docs**, so unlike phases 0-4 the two halves were
  independent. Say so - the "a change to one usually needs the other" warning no longer applies
  to this phase and a reader will otherwise assume it does.
- The `dispatch-from-chrome` popup tunnel was already removed in phase 4; phase 6 found nothing
  to do there.
- **`lib/tmux.sh`'s `display-popup` branch was kept on purpose** and is not leftover phase 6
  work. This is the single most likely thing for a future session to "finish" incorrectly.
- `worktree-status`'s Shortcut story column has no vigil equivalent. Add it to the open list
  next to the `fillGit` and session-viewport entries.
- Keep every existing "Key Conventions" bullet. Phase 6 changed no vigil behaviour, so nothing
  in that section is superseded.

- [ ] **Step 2: Write the handoff**

Follow the shape of `docs/superpowers/2026-08-01-phase-5-work-queue-handoff.md`: what shipped,
what was verified with numbers, what was NOT verified, landmines, deferred, process notes.
Required content:

- The prerequisite evidence table from this plan, as the record that the phase 5 gap was closed
  by real use rather than a test.
- **The honest limit of this phase's verification.** No bats test covered any of the four
  scripts, so `make lint` guarded only the Makefile list and every other check was a manual
  reference sweep. A future reader must not infer that a green suite validated these deletions.
- The `worktree-status` story-column loss, with the two remaining ways to get that information
  (`short story <id>`, the Shortcut web UI).
- The two "already resolved" items, so phase 7 does not re-derive them.
- That `.nit.json` and `python/src/vigil/widgets.py` still mention deleted scripts, and why
  both were left.

- [ ] **Step 3: Verify the vigil suite is untouched by this phase**

```bash
cd ~/vigil && make test && make lint
```

Expected: both PASS, 14 packages. This phase changes no Go code, so a failure here is a
pre-existing problem or a bad doc edit, not a regression from a deletion.

- [ ] **Step 4: Commit**

```bash
cd ~/vigil && git add CLAUDE.md docs/superpowers && git commit -m "docs: record phase 6 and close out the six-phase design

Phase 6 was deletions only, dotfiles-side, with no vigil code change. Notes
the two list items that had already resolved themselves, the popup branch
that was kept deliberately, and the worktree-status story column that has no
vigil equivalent."
```

---

### Task 5: Final sweep

Runs after Tasks 1-3 are merged to `master` in dotfiles and Task 2 Step 8 has unbound the live
keys. Exists because the per-task sweeps were each scoped to one target, and the phase 5
handoff's clearest process finding is that a task-scoped check cannot see a seam.

**Files:** none modified unless the sweep finds something.

- [ ] **Step 1: Sweep both repositories for every deleted name at once**

```bash
for n in tmux-monitor gh-review-poll worktree-status dispatch.1d; do
  echo "=== $n ==="
  grep -rn "$n" --exclude-dir=.git ~/dotfiles ~/vigil 2>/dev/null \
    | grep -vE "docs/superpowers|CLAUDE\.md|\.nit\.json|widgets\.py"
done
```

Expected: no output under any heading. Hits in docs, CLAUDE.md, `.nit.json` and `widgets.py`
are filtered because they are the deliberate historical references catalogued above.

- [ ] **Step 2: Confirm the live system still works**

```bash
ls -la ~/scripts/ | grep -E "tmux-monitor|gh-review-poll|worktree-status|dispatch.1d" \
  && echo "STILL PRESENT - stow may not have re-run" || echo "gone from live path"
tmux list-keys -T prefix | grep "worktree-status" \
  && echo "STILL BOUND" || echo "unbound"
pgrep -fl dispatch-bar || echo "dispatch-bar DOWN - menu bar dispatch is gone"
pgrep -f "vigil daemon" || echo "no daemon"
```

Expected: gone from the live path, unbound, `dispatch-bar` up, a daemon running.

Grep for `worktree-status`, not for the `w`/`C-w` key names - same reasoning as Task 2 Step 8:
tmux binds `w` to a built-in by default, so a key-name check false-positives as "STILL BOUND" on
any server where this config no longer overrides it.

- [ ] **Step 3: Exercise the two surfaces that replaced the deleted tools**

```bash
python3 - <<'PY'
import socket, json, os
s = socket.socket(socket.AF_UNIX); s.settimeout(5)
s.connect(os.path.expanduser("~/.local/state/vigil/vigild.sock"))
buf = b""
while b"\n" not in buf:
    buf += s.recv(65536)
snap = json.loads(buf.split(b"\n")[0]); s.close()
print("sessions:", len(snap.get("sessions") or []))
print("queue:", len(snap.get("queue") or []), "hidden:", snap.get("queue_hidden"))
PY
```

Expected: a nonzero session count and a queue that is either populated or legitimately empty.
This is the surface that replaced `worktree-status` (sessions) and `gh-review-poll` (queue); if
the daemon cannot answer, the deletions removed the old path while the new one was down.

Note: this reads the socket, it does not dispatch. Do not add a dispatch to this step - a
mis-aimed one creates a real worktree.

- [ ] **Step 4: Remove the worktree**

```bash
cd ~/dotfiles && git worktree remove ~/dotfiles-phase6 && git worktree prune
git worktree list
```

Expected: `~/dotfiles-phase6` absent from the list. If `git worktree remove` refuses because
the tree is dirty, inspect what is uncommitted before forcing - it may be work Tasks 1-3
missed.

---

## Self-review

**Spec coverage.** Phase 6's five list items, from
`docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md:157-165`:

| Spec item | Task |
|---|---|
| `tmux-monitor` | Task 1 |
| `gh-review-poll` | Task 3, gated on the prerequisite, now satisfied |
| `worktree-status` and its two bindings | Task 2 |
| One of the two menu bar implementations | Task 1 (`dispatch.1d.sh`) |
| The popup tunnel inside `dispatch-from-chrome` | Already done in phase 4; no task, documented |

The design's "Only after living on the above" precondition is addressed by the prerequisite
section.

**Placeholder scan.** No TBDs. Every step carries the exact command or the exact replacement
text. Task 4's two steps specify required *content* rather than final prose, which is
deliberate - a handoff describing what happened cannot be written before it happens - and each
lists the specific claims it must contain so a reviewer can check it rather than approve a
gesture.

**Type consistency.** No new code, so no signatures. The one cross-task dependency is the
`Makefile` `SHELL_SCRIPTS` list, edited by Tasks 1, 2 and 3 in that order. Traced:

- Task 1 leaves line 6 as `claude-trust dispatch dispatch-from-chrome \` and line 10 as
  `ts vigil-panel worktree-status`.
- Task 2 consumes that line 10 and leaves `ts vigil-panel`.
- Task 3 edits line 7 only, which neither of the others touches.

Final state, four names removed from a list that named twenty-six:

```make
SHELL_SCRIPTS := common.sh $(wildcard lib/*.sh) \
	claude-trust dispatch dispatch-from-chrome \
	gh-review gh-worktree \
	git-worktree-cleanup git-worktree-done git-worktree-new git-worktree-session \
	portal-open short-story-md shortcut-claim shortcut-implement shortcut-worktree \
	ts vigil-panel
```

**Two known weaknesses in this plan, stated rather than hidden.**

1. The only automated guard is `make lint`, and it verifies one property: that the Makefile's
   list matches the files on disk. It cannot tell whether a deleted script was still wanted.
   Every real safety claim here rests on manual reference sweeps, which is why Task 5 repeats
   them across both repositories after the merge.
2. Task 2 Step 8 runs against the live tmux server after a merge, so it is the one step that
   cannot be rehearsed in the worktree and the one most likely to be skipped. A skipped Step 8
   leaves `prefix w` bound to a deleted file: the failure is a popup that flashes an error, not
   silence, which is the only reason it is not blocking.

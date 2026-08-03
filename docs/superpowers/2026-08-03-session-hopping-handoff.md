# Session hopping handoff: an agreeing order, tmux-native bindings, and one measurement that was wrong

Date: 2026-08-03

**This is not a phase 7.** `CLAUDE.md` says plainly that the six-phase design is complete and
there is no phase 7; this is a defect-and-feature batch that came out of three user complaints,
not a continuation of that design. Nothing here changes the phase model.

Spans two repositories. Design:
`docs/superpowers/specs/2026-08-03-session-hopping-design.md`. Plan:
`docs/superpowers/plans/2026-08-03-session-hopping.md`. Both are **corrected by this handoff on
three points** - see "Corrections to the spec and the plan" below, and prefer this document
where they disagree.

| Repo | Base | Code tip | Commits |
|---|---|---|---|
| `vigil` | `56f071e` | `2cfae6c` | `6a42404` T1, `932beb5` T2, `737f0a2` T3, `2cfae6c` T4 |
| `~/dotfiles` | `46d844b` | `515b3df` | `5dcfd74` T5, `7dc0cb6` T7, `96feee6` T5 fix, `515b3df` T6 |

Two documentation commits sit on top of those tips: this handoff's own commit in `vigil`
(which also carries the spec, plan and two comment corrections below), and `1a224b0` in
`~/dotfiles`, which corrects three comments - the two `TEMPORARY` stamp comments' doc path and
`M-o`'s claim about its own fallback. Both are comment-only; `make test`/`make lint` are green
and `.tmux.conf` still sources with all thirteen bindings intact.

Both branches are named `session-hopping`. `~/dotfiles` carries **unrelated uncommitted work**
(`claude/.claude/settings.json`, `claude/.claude/skills/create-pr/SKILL.md`,
`scripts/scripts/git-worktree-new`, and two untracked skill directories). Every task staged
only the files it named. **Never `git add -A` in that repo.**

## What landed

**T1 - `fetch.ListSessions` reads `#{session_id}` (`6a42404`).** The format string gains the
field between `session_created` and `session_name`, so the parse rule the existing comment
defends - "flags are the last three fields, the path is everything between the name and them" -
is untouched and only the indices shift.

```go
// internal/fetch/tmux.go:59
"-F", "#{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}")
```

Re-derived from scratch rather than read off the diff, against a line with a piped path *and*
all three flags. `1700000000|$1|name|/a|b|1|0|0` splits to 8 parts; `flagStart = 8-3 = 5`;
`path = join(parts[3:5], "|")` is `/a|b`; `parts[5]`, `parts[6]`, `parts[7]` are the three
flags. Correct. The two guards moved by exactly one each (`len(parts) < 3` → `< 4`,
`flagStart > 2` → `> 3`), which keeps the same boundary: the flag branch is taken at seven
fields or more, which is every well-formed line.

`sort.Strings(lines)` stayed, and its effect changed in one inert way worth writing down: lines
now begin `created|$id|...` rather than `created|name|...`, so among sessions sharing a
`created` second the *raw* emission order is lexical by the `$id` string (`$1` < `$10` < `$2`)
rather than alphabetical by name. Nothing consumes that order: `collect.Snapshot`
(`internal/collect/collect.go:198-236`) builds sessions in raw order and never sorts, and every
path that populates `Model.sessions` calls `session.SortSessions` first -
`internal/model/model.go:279` (cache load, whose own comment says "The cache is written in tmux
order, and nothing else sorts before the first render"), `:1097` (`applySnapshot`) and `:1800`
(the `s` key). Within one session name the `created` and `$id` fields are constant, so pane
dedup order - the actual job `sort.Strings` has - cannot have changed.

**T2 - `Session.ID`, and `SortCreated` becomes `(Created, ID)` (`932beb5`).**

```go
// internal/session/sort.go:43-47
sortBy(sessions, func(a, b *Session) bool {
    if a.Created != b.Created {
        return a.Created < b.Created
    }
    return a.ID < b.ID
})
```

Two keys and not one, because `ID` is 0 for a session hydrated from a cache file written before
`json:"id"` existed, and a pure-`ID` comparator would hoist every such session to the front
until the first poll landed. `(Created, ID)` degrades to exactly today's behaviour instead.
`ID` 0 is also legitimately tmux's first session on a fresh server (`$0`, verified), and the
comparator is right either way: 0 sorts first and a real `$0` genuinely is the oldest.

The index column needed no change, and the deciding lines are in `RenderTable`'s loop rather
than in `indexCol`:

```go
// internal/view/table.go:56-64
for i, s := range sessions {
    ...
    line := renderRow(s, i, selected[s.Name], staleThreshold, width, isCursor, layout)

// internal/view/table.go:83
func renderRow(s *session.Session, index int, selected bool, ...) string {

// internal/view/table.go:96
cells = append(cells, indexCol(index, bg))
```

So the label is the loop index over the already-sorted slice. Two consequences that follow from
that and are worth having written down: the index is **absolute, not viewport-relative** -
`TableWindow` skips rows with `if i < offset { continue }` and `i` is still what `renderRow`
gets - so scrolling the table does not shift the numbers `M-<n>` has to match; and `indexCol`
blanks past 9 (`if index <= 9`, `internal/view/table.go:126-129`), which is why there are ten
literal bindings and no eleventh.

**T3 - the daemon logs `session dropped: <name>` (`737f0a2`).** A `Server.prevSessions
map[string]bool` compared once per poll, in `logDroppedSessions`
(`internal/daemon/daemon.go:437-450`), called from `poll` after the `err != nil` early return.
Daemon-only and not user-visible, on the `slow poll` precedent. No mutex, and the claim was
re-derived: `grep -rn "\.poll("` gives two production callers, `daemon.go:182` (the pre-ticker
poll) and `:200` (the ticker arm), both inside `Run`'s own function body. `Run` starts
`accept`, `jobs.work` and the collector's pollers, and none of them call `poll` - they reach
`Run`'s select loop through channels. No test both runs `Run` in a goroutine and calls `poll`
on the same `Server`.

The placement after the error return is load-bearing and was proved so, not assumed. With the
call moved above it and the collector made to fail, a single failing poll logged every known
session as dropped:

```
log after failing poll: "session dropped: alpha\nsession dropped: beta\npoll failed: boom\n"
```

`Collector.Snapshot` returns `nil, err`, so `current` is empty and the whole previous set reads
as departed. A recovering poll is clean for the same reason in reverse: a failed poll never
reaches `logDroppedSessions`, so `prevSessions` still holds the last *successful* set.

**T4 - the `notify` default's quoting (`2cfae6c`).**

```go
// internal/config/config.go:66
"notify": `tmux display-message -d 5000 "vigil: "{session}" → "{new_state}`,
```

The default had **never fired successfully**. `ExpandHook` substitutes each placeholder as one
shell-quoted word, so a placeholder inside a larger double-quoted string lands as `'...'`
within `"..."`, and dotfiles' `session_name_from_title` produces session names containing
literal double quotes, which close the outer string early and split the message into two
arguments. `tmux display-message` takes at most one. The adjacent form makes the shell
concatenate the pieces. `rawPlaceholders` was **not** widened, verified after the fact:

```go
// internal/config/config.go:156
var rawPlaceholders = map[string]bool{"flags": true}
```

The ugliness is inherent, not incidental: `ExpandHook`'s one-word-per-placeholder guarantee is
what makes the readable form impossible, which is why the default carries a comment saying so.

**T5 - `~/dotfiles/scripts/scripts/tmux-hop` (`5dcfd74`, fixed in `96feee6`).** One script,
one ordered-list function, three subcommands. The order is `#{session_id}` numerically, which
needs no tie-break rule and therefore cannot drift. Two defects were found and fixed in review:
`nth()`'s `$((n + 1))` read a leading-zero index as octal and aborted with a raw
`value too great for base` on `08`/`09` (now `$((10#${n} + 1))`), and `neighbour()` leaked two
raw tmux stderr lines when invoked with no tmux server (now one `error()` line).

**T6 - the bindings (`515b3df`).** `M-j`, `M-k`, `M-0`..`M-9`, `M-o` in `tmux/.tmux.conf`, all
`bind -n` (root table), all `run-shell -b`. All thirteen confirmed registered with byte-exact
command strings, on an isolated scratch server first and then on the live one. The old inline
`run-shell` bodies are gone rather than shadowed, and `M-C-h/l`, `M-h/l`, `M-Space` and the
whole prefix table are untouched (`bind j` is a different binding from `bind -n M-j` and
survived).

**T7 - temporary timing stamps (`7dc0cb6`).** Four points in `git-worktree-done`, and
**seven** in `git-worktree-cleanup` rather than the three the design named: `main` entry, after
`kill_tmux_session`, and every one of the six exit paths (`main` has two `return 0` and four
`return 1`, not the two the plan counted). The brief's `stamp` helper was **not errexit-safe**
and that was a real bug averted: a redirection failure aborts under `set -o errexit`, and
`git-worktree-cleanup` runs unattended from the daemon's `cleanup` hook, so an unwritable or
full `/tmp` would have taken down a real cleanup mid-run. Fixed with `|| true` and verified
end-to-end by making `/tmp/vigil-hop-timing.log` a directory: cleanup still ran to completion
and exited 0, with only bash's own stderr line as a symptom. A missing `perl` alone does not
trip errexit - only the redirection does.

## Measurements

**The `session_id` ordering comparison, against a real dashboard.** Real tmux had only two
sessions with distinct `session_created` values, so two throwaway sessions were created
back-to-back to produce a **genuine tie** - the case the change exists for - and torn down
afterwards.

```
$ tmux list-sessions -F '#{session_id}|#{session_created}|#{session_name}'
$1|1785767676|SC-223374 Add bulk "Report Investigation" action
$0|1785767643|main
$11|1785787845|vigil-test-a
$12|1785787845|vigil-test-b
```

`$11` and `$12` share `session_created` = 1785787845. tmux's own `session_id` order:

```
$ tmux list-sessions -F '#{session_id}|#{session_created}|#{session_name}' \
    | sed 's/^\$//' | sort -t'|' -k1,1n | cut -d'|' -f3-
main
SC-223374 Add bulk "Report Investigation" action
vigil-test-a
vigil-test-b
```

The real dashboard, built from branch tip and run in a real tmux window, then `capture-pane`d -
not a synthetic harness:

```
    0 · main                                                 ~1 +2              —
 ·  1 ● SC-223374 Add bulk "Report Investigation" action     ↻ 5h               #35068 ✓
 ▸  2 · vigil-test-a                                         ~6 +1 ⚠↻ 91d       —
    3 · vigil-test-b                                         ~6 +1 ⚠↻ 91d       —
```

And a direct library-call harness over `fetch.ListSessions` + `session.SortSessions(...,
SortCreated)` against the real `ExecCommander`, bypassing the TUI:

```
1: id=0 created=1785767643 name=main
2: id=1 created=1785767676 name=SC-223374 Add bulk "Report Investigation" action
3: id=11 created=1785787845 name=vigil-test-a
4: id=12 created=1785787845 name=vigil-test-b
```

Three independent routes, same order, tie resolving `$11` before `$12`. T5 repeated the
comparison against a six-session set including `hop-prefix`, `hop-prefix-longer` and
`hop-regex[a+b]` and got the same agreement between `ordered_sessions()` and the rendered
panel.

**The wrap and awkward-name checks.** Six sessions, `next` seven times, each hop observed
through `tmux list-clients` rather than `display-message`:

```
next#1 : main -> SC-223374 Add bulk "Report Investigation" action
next#2 : SC-223374 Add bulk "Report Investigation" action -> hop-prefix
next#3 : hop-prefix -> hop-prefix-longer
next#4 : hop-prefix-longer -> hop-regex[a+b]
next#5 : hop-regex[a+b] -> hop_test_one "quoted"
next#6 : hop_test_one "quoted" -> main
next#7 : main -> SC-223374 Add bulk "Report Investigation" action
```

`prev` produced the exact mirror, confirming it is `next`'s true inverse. `hop-regex[a+b]`
(`[`, `+`, `]`) and `hop_test_one "quoted"` (space, `"`) both matched exactly through `awk`'s
`==` at every hop - defect 1, exercised live rather than reasoned about.

The prefix hazard, defect 2, by index jump from a known state:

```
before: main
tmux-hop 0                                    -> main
tmux-hop 2  (expect hop-prefix)               -> hop-prefix
tmux-hop 3  (expect hop-prefix-longer)        -> hop-prefix-longer
tmux-hop 99 (out of range, expect unchanged)  -> hop-prefix-longer, exit 0
```

The `=` exact match never crossed between `hop-prefix` and `hop-prefix-longer`, and index 99
was a silent no-op at exit 0.

The leading-zero fix, traced through `bash -x` against a private ten-session socket:

```
++ nth 08     ++ sed -n 9p      + target=z9
++ nth 09     ++ sed -n 10p     + target=z0new
++ nth 00     ++ sed -n 1p      + target=z1
++ nth 007    ++ sed -n 8p      + target=z8
```

`8`/`9`/`0` resolve to the same targets, so the fix changes nothing about normal input.

**A testing-harness trap that will catch the next person too.** An automation shell living in a
fixed tmux pane cannot use `tmux display-message -p '#{session_name}'` to observe where the
real client went: `display-message` without `-t` resolves against the *calling pane's* context,
so it always answers with the agent's own session and a loop of `tmux-hop next` appears to
no-op forever. That is a harness artifact, not a script defect. Drive it as
`tmux run-shell -t <pane-in-the-client's-session> 'tmux-hop next'`, which is what `M-j` actually
does, and read the result from `tmux list-clients -F '#{client_session}'`.

**The `notify` hook's real-daemon fire: UNVERIFIED.** See "Verification limits". The historical
failures are what is actually measured:

```
vigil: 2026/08/03 13:38:43 notify hook for SC-223374 Add bulk "Report Investigation" action: hook notify failed: exit status 1 (output: command display-message: too many arguments (need at most 1))
vigil: 2026/08/03 14:02:42 notify hook for SC-223374 Add bulk "Report Investigation" action: hook notify failed: exit status 1 (output: command display-message: too many arguments (need at most 1))
```

**Twenty-two** such lines on 2026-08-03 alone (09:42:08 through 14:02:42), 26 in the whole log
going back to 2026-07-29, and **zero** `notify hook` lines that are not failures
(`grep 'notify hook' … | grep -vc failed` → 0). The last is at 14:02:42; the fixed binary was
installed at 15:28 and the daemon restarted at 15:29
(md5 of `~/.local/bin/vigil` matched the fresh build; exactly one `vigil daemon` running,
started 15:29). No `notify hook` line of any kind has appeared since - not a success and not a
failure - because no state transition has occurred. The absence of new `too many arguments`
lines is therefore **vacuous**, and is not evidence for the fix.

**`run-shell -b` visibility, tested on a fresh `-L` 3.7b server** (see the machine landmine
below for why naming the server matters). This decides whether `tmux-hop`'s "fail loudly"
posture is real:

```
run-shell -b 'true'                                   -> name=zsh     mode=0   (pane untouched)
run-shell -b 'exit 3'                                 -> name=[tmux]  mode=1   (view mode)
run-shell -b 'echo OUT; echo ERR >&2; exit 1'         -> name=[tmux]  mode=1   (view mode)
```

So tmux drops the pane into view mode for a backgrounded command that either produces output or
exits non-zero, and leaves it alone only for a silent success. `tmux-hop`'s `error()` lines and
`switch-client`'s own `can't find session` are therefore visible to the user, and the
out-of-range index no-op is silent because it returns 0 with no output - both by mechanism, not
by luck.

## Corrections to the spec and the plan

Three prose claims in the design and plan were wrong. All three were caught by a reviewer
re-deriving the claim rather than reading it. The spec and plan have been edited in place; this
section is the record of what changed and why.

**1. `(Created, ID)` is not "provably the same total order" as pure `session_id`.** It is the
same order under the assumption that `session_created` is monotonic in `session_id`, and that
assumption fails if the wall clock moves backwards between two session creations while tmux's
id counter keeps climbing. Two sessions created at t=100/`$5` and t=99/`$6` sort `$5, $6` by
pure id and `$6, $5` by `(Created, ID)`, so vigil's rows and `M-j`'s walk disagree for that
pair. The exposure is pre-existing - `Created` was already the primary key - and it is narrow,
but "provably" was the wrong word and has been removed from the spec. `internal/session/sort.go`
and `internal/session/session.go` carry the same overstatement in their comments; both are
softened to "the same total order in practice", with the clock-skew case named.

**2. "Defect 3" - the colon fix - was overstated in both the spec and the plan.**
`cut -d'|' -f2-` is unconditionally correct, but it fixes **extraction only**. A colon-named
session is untargetable by name on *any* tmux build, because `:` is tmux's own session:window
target separator: on a fresh, non-sanitizing server, `has-session -t '=a.b:c[d+e]'` fails with
`can't find session: a.b` - it parsed everything after the first colon as a window spec, and
the `=` prefix does not change that. `switch-client -t` fails identically. So the value of the
fix is not reachability: the old `cut -d: -f2` would have **silently switched to a different
real session named `a.b`**, and the new code attempts the true full name and fails loudly.
Same trade as defect 2. Scope is also narrower than the plan implies -
`lib/tmux.sh`'s `session_name_from_title` already strips `:` and `.` at creation
(`title="${title//:/}"`, `title="${title//./}"`, lines 110-111), so this can only reach a
session a human named by hand.

**3. The `M-o` quoting hazard is a double quote, not a single quote.** A single quote in
`#{pane_current_path}` is harmless: at runtime the substituted path sits inside a double-quoted
string for `sh`, where a literal `'` is inert. Tested with a directory named `dir_with_'_quote`
and `cd` succeeded. A literal **double** quote does break it, and the interesting half is what
happens next: `#{...}` expansion happens before `sh` parses, so the injected `"` makes the
whole command a parse error and the `||` fallback **also never runs** -

```
$ sh -c '{ cd "/private/tmp/.../dir_with_"_dquote" && false; } || echo FALLBACK-FIRED'
sh: -c: line 0: unexpected EOF while looking for matching `"'
sh: -c: line 1: syntax error: unexpected end of file
exit=2
```

`FALLBACK-FIRED` never printed. **So `|| tmux display-message "no PR for this branch"` must
not be described as unconditional anywhere**, and the `.tmux.conf` comment claiming "the braces
are so a failed cd also reports" has been corrected. It is *not* total silence, though - a
non-zero exit with output puts the pane into view mode (measured above), so the user sees the
shell's parse error instead of the intended message. Accepted risk: no worktree path in this
toolchain contains a `"`.

## Verification limits

The honest account. Read this before trusting anything above.

- **The two orders agreeing is checked by hand and by nothing automated.** There is no test,
  in either repository, that compares vigil's rendered order against `tmux-hop`'s. The
  agreement rests on `session_created` being monotonic in `session_id`, which is true of tmux
  and **asserted nowhere** - not in a test, not in a runtime check. If tmux ever changed that,
  or the clock skews (correction 1), nothing detects it.
- **The agreement breaks silently the moment the user presses `s` or `f`.** `CycleSort` (`s`,
  `internal/model/keys.go:45`) and `CycleFilter` (`f`, `:42`) are live keys. A filter is worse
  than a sort: `Model.visibleSessions` (`internal/model/model.go:1635-1646`) returns a
  *shorter* slice, `RenderTable` labels rows with the loop index over it, so every index below
  the filtered-out session shifts and `M-<n>` lands somewhere else. Accepted on the user's
  statement that they neither sort nor filter. Nothing warns.
- **`M-<n>` past the session count is silent by design and indistinguishable from a broken
  binding.** `nth()` returns empty, `main()` takes `[[ -z "${target}" ]]; return 0`, and a
  silent exit-0 `run-shell -b` leaves the pane untouched (measured). Correct behaviour;
  identical symptom to a typo in `.tmux.conf`.
- **At the landscape panel's default width there is no index column to match.** `M-<n>` is
  documented as 0-based "matching vigil's index column", but `LayoutForWidth`
  (`internal/view/layout.go:108-113`) drops `Index` below `tierNoGit = 41`, and the landscape
  panel's default is 40 columns. So in that panel the numbers `M-<n>` refer to are not drawn at
  all; the user has to count rows or use the dashboard. Nobody flagged this during the plan.
- **T4's real-daemon fire is UNVERIFIED, not passed.** The binary was rebuilt, installed and
  the daemon restarted, all confirmed by md5 and process start time, but **no state transition
  occurred in the window watched (~2-4 minutes)** and nothing was appended to the log. Unit
  tests are the whole of the evidence for that fix. The next real transition on this machine is
  the first live proof, and it is worth looking for: `grep 'notify hook' ~/.local/state/vigil/vigild.log`.
- **`TestTheFirstPollLogsNoDrops` is non-discriminating.** It survives full removal of the code
  it was written to pin - both `if s.prevSessions != nil` → `if true` and deleting the branch
  entirely leave it green - because ranging over a nil map in Go is a zero-iteration loop. It
  tests Go's semantics, not this implementation. **The plan's Step 8.2 mutation check must not
  be recorded as passed.** The two positive tests (`TestPollLogsADroppedSession`,
  `TestPollLogsEverySessionDroppedInOnePoll`) do discriminate and did fail under mutation 1.
- **T6's interactive smoke test was never run.** No binding was pressed or simulated against
  the live server. `M-j`/`M-k` moving and wrapping *as a keypress*, `M-<n>` landing on the right
  row with the panel visible, and `M-o` actually opening a browser are all unverified. What was
  verified is that the bindings are registered with the right command strings, and that
  `tmux-hop` itself does the right thing when invoked through `run-shell`.
- **`M-o` has never been run against a real PR.** The shape was tested with `true`/`false`
  substituted for `gh pr view --web`, deliberately, to avoid opening a browser.
- **Every tmux behavioural claim above names its server on purpose** - see the landmine below.
  The awkward-name cycle ran on the live server; the colon-separator finding, the sanitization
  contrast and all the `run-shell -b` visibility results ran on fresh `-L` servers.
- **The `session dropped` line has no counterpart.** There is no `session returned`, so a
  transient drop is indistinguishable from a permanent kill, and a rename reads as an unpaired
  drop. Deliberately out of scope: the reported symptom is a *late* drop, and a single
  timestamped drop line answers that.

## Complaint 1: instrumented, uncaused, and the measurement behind its dismissal was wrong

The complaint is that a session takes "a few seconds" to leave vigil's list after `prefix d`.
**It was never reproduced, it is not fixed, and it now has less of an explanation than the
design claimed.**

The design's measurement table, reproduced as written:

| gap | measured |
|---|---|
| `slow poll` lines in the entire daemon log | **0** |
| `tmux display-popup -E` startup, 3 runs | 0.01-0.02s |
| `git-worktree-cleanup` invoke → session gone from `tmux list-sessions` | 0.272s |
| tmux drops the session → daemon snapshot drops the row | 0.968s |
| session created → daemon snapshot has the row | 0.676s |

The four timing rows stand, and the conclusion drawn from the fourth stands with them:
**0.968s is the floor for a 1s cadence, so vigil is at its limit on that path and is not the
lag.** `kill_tmux_session` also runs early in cleanup's `main` (`git-worktree-cleanup:218`
after T7's stamps; the spec's `:206` was the pre-T7 line), before any worktree removal, so the
kill is not waiting on git. The components sum to roughly
1.3s worst case, which does not account for "a few seconds".

**The first row is wrong.** The daemon log is opened `O_CREATE|O_WRONLY|O_APPEND`
(`internal/daemon/spawn.go:26`), never truncated, and it runs continuously from 09:33:58 to
16:19:13 on 2026-08-03. It contains **eight** `slow poll` lines, not zero:

```
2026/08/03 14:30:27 slow poll: 11.1s total, 11.085s in git, slowest 11.085s at /Users/joshua.zink-duda/sc-223374
2026/08/03 15:14:59 slow poll: 1.613s total, 1.603s in git, slowest 1.603s at /Users/joshua.zink-duda/pr-35108
2026/08/03 15:17:24 slow poll: 1.014s total, 1.005s in git, slowest 1.004s at /Users/joshua.zink-duda/pr-35108
2026/08/03 15:20:11 slow poll: 1.379s total, 1.368s in git, slowest 1.368s at /Users/joshua.zink-duda/pr-35108
2026/08/03 15:25:14 slow poll: 1.017s total, 1.007s in git, slowest 1.007s at /Users/joshua.zink-duda/pr-35108
2026/08/03 15:27:47 slow poll: 1.008s total, 994ms in git, slowest 994ms at /Users/joshua.zink-duda/pr-35108
2026/08/03 15:29:07 slow poll: 1.289s total, 1.278s in git, slowest 1.278s at /Users/joshua.zink-duda/pr-35108
2026/08/03 16:19:13 slow poll: 1.131s total, 1.081s in git, slowest 1.08s at /Users/joshua.zink-duda/sc-223374
```

The 14:30:27 line - **11.1s total, 11.085s of it in one `git status`** - predates the spec file
by seven minutes (spec written 14:37:19, committed 14:37:26). So the claim was already false
when it was written, and it was the sole support for two things the spec asserted: that
"`fillGit` gating publication is dead as a hypothesis", and that this was "a third data point"
for `CLAUDE.md`'s 2026-08-03 demotion. **Neither survives.** Both have been struck from the
spec.

Read the eight lines with `logSlowPoll`'s rate limit in mind
(`internal/daemon/daemon.go:413-427`): it is a one-minute **window**, so eight lines means at
least eight distinct minutes containing a poll over `tmux_interval`, not eight slow polls. The
shape also matches what `CLAUDE.md` predicted as the remaining unexplained suspect: six of the
eight are 1.0-1.6s at `pr-35108`, a freshly dispatched worktree, which is the cold-worktree
first-`status` exposure; the 11.1s outlier at `sc-223374` is not explained by anything.

An 11-second poll in a 1-second cadence blocks publication for eleven seconds, and a session
that has already been killed stays on screen for all of it. That is a mechanism for the
reported symptom, and it is present in the log. **It does not make `fillGit` the cause** - the
11.1s line is not timestamped against a `prefix d` - but it does mean the search should start
from the log rather than from the popup.

**The leading untested hypothesis is still popup occlusion**: the popup covers the screen for
the remainder of cleanup - `git worktree remove`, mise cleanup, branch deletion, all after the
kill - so the row may already be gone by the first moment the user can look at it. Both
hypotheses are testable by the same timeline.

**Step 6 of the plan - collecting one real `prefix d` timeline - is DEFERRED TO THE HUMAN and
has NOT been done.** It needs a real `prefix d` on a real worktree session, with the T3 daemon
build installed. Nothing here substitutes for it. When it is run, the shape to fill in:

```
prefix d pressed                t+0.000
switch-client returned          t+?     /tmp/vigil-hop-timing.log
popup opened                    t+?     /tmp/vigil-hop-timing.log
cleanup started                 t+?     /tmp/vigil-hop-timing.log
kill-session returned           t+?     /tmp/vigil-hop-timing.log
daemon: session dropped         t+?     ~/.local/state/vigil/vigild.log
cleanup finished, popup closes  t+?     /tmp/vigil-hop-timing.log
```

The gap between `kill-session returned` and `session dropped` is vigil's; every other gap is
dotfiles'. If the largest is between `session dropped` and `cleanup finished`, occlusion is
confirmed and the fix is on the dotfiles side. If a `slow poll` line lands inside that window,
it is `fillGit` and the design in
`docs/superpowers/specs/2026-08-03-dirty-counts-off-publication-path-design.md` is the answer
already written for it.

The `session dropped` line is already working on live data - sixteen lines between 15:35 and
16:00, including the throwaway sessions the tasks created and killed:

```
2026/08/03 15:35:33 session dropped: PR-35108 Command Center widget add Investigations
2026/08/03 15:46:51 session dropped: hop-regex[a+b]
2026/08/03 16:00:00 session dropped: review-timing-check-xyz
```

## The dotfiles timing stamps are TEMPORARY

`git-worktree-done` (4 stamps) and `git-worktree-cleanup` (7 stamps) append to
`/tmp/vigil-hop-timing.log`. **Remove both once the timeline above has been read.** The
instruction to do so lives in the `TEMPORARY` comment above each `stamp` helper -
`scripts/scripts/git-worktree-done:22-24` and `scripts/scripts/git-worktree-cleanup:26-28` -
and it points here, at part C of the design spec. Both comments originally cited
`docs/superpowers/specs/...` as if that path existed in `~/dotfiles`; corrected to
`vigil/docs/...`, matching how `tmux-hop`'s header and `.tmux.conf` already write it.

Two known, accepted exposures while they are in place: `/tmp/vigil-hop-timing.log` is a
predictable name in a world-writable directory, so it is symlink-attackable in principle
(negligible on a single-user machine, and temporary); and the `stamp` helpers are `|| true`, so
a failure to write is silent apart from bash's own stderr line.

## Deferred and open

Nothing below is scheduled.

**In vigil:**

- **The `if s.prevSessions != nil` guard in `logDroppedSessions` is dead.** A reviewer deleted
  the branch and the package still passed, because ranging a nil map is a no-op. **Ruling:
  remove the branch, keep the loop.** Same precedent as the `min(cursor, count-1)` removal
  recorded in `CLAUDE.md`: with both a guard and the thing it guards against being harmless, no
  test can distinguish them, so the guard is not documentation - it is a second thing that has
  to stay true.
- `internal/daemon/daemon.go:66`'s comment ("nil means no poll has succeeded yet") asserts a
  distinction nothing can observe, for the same reason.
- **`README.md:108` still documents the old, broken `notify` default** as
  `tmux display-message "vigil: {session} → {new_state}"`. No task in the plan touched README.
  Anyone copying it gets a hook that has never worked.
- The plan's item 4 for the whole-branch review - the `notify` default under a *single*-quoted
  session name - was done and passed. It is worth knowing why it was interesting: before the
  fix that case reduced to one shell argument but the wrong one
  (`vigil: 'it'\''s broken' → 'approved'`), because the old default nested the placeholder's
  quoting inside its own double quotes and rendered it inert. `shellQuote`'s `'\''` idiom was
  never buggy; the default was hiding a working escape.

**In dotfiles:**

- A 20+ digit index still overflows bash's 64-bit arithmetic in `nth()`. Pre-existing, not
  worsened by `10#`, and unreachable through a single-digit keybinding.
- `git-worktree-cleanup`'s stamps do not cover an uninstrumented `errexit` abort - a command
  failing mid-`main` without reaching a `return`. A `trap ... EXIT` would, and was judged a
  larger and riskier change than the diagnostic warrants.
- `git-worktree-done` has three `return 1` paths (lines 64, 70, 91), all deliberately
  unstamped; the T7 report says two.

## Landmine: this machine's tmux server and its binary disagree

**The live tmux server started at 09:34:03 on 2026-08-03. `/opt/homebrew/bin/tmux` was
relinked to 3.7b at 14:32 the same day.** `brew upgrade` replaced the binary underneath a
running server, so the live server executes pre-upgrade code while every freshly invoked
client - and every new `-L` server - executes 3.7b. `tmux -V` reports the **client** binary and
cannot detect this.

It is not academic. It produced a direct contradiction that cost two agents real time: on the
live server, `new-session -s 'a.b:c[d+e]'` silently rewrites `.` and `:` to `_`; on a fresh
`-L` server with the identical 3.7b client, the same command stores the name intact. That was
first written up as "tmux 3.7b sanitizes session names", which is false as a statement about
the build.

**Any tmux behavioural claim on this machine must name which server it tested**, and a
disagreement between two such claims should be checked against
`ps -eo pid,lstart,command | grep tmux` before it is treated as a real inconsistency. This
resolves itself the next time the server restarts, and will recur on the next `brew upgrade`.

## Process notes

`CLAUDE.md`'s standing warning tallies nineteen briefs containing tests that would have passed
with their subject deleted. **This plan adds to that tally, and the author was again the plan's
author.**

- **T1's three new tests were vacuous as written.** The brief specified
  `mock.On("tmux", output, nil)`. `MockCommander.On` keys the handler map by **command name
  only** (`internal/fetch/cmd.go:184-186`), and `Run` falls back to that name-only key
  (`:250-253`) after trying the full command line. So any `tmux` invocation matched regardless
  of arguments, and a mutation to the format string - the entire subject of the task - could
  not fail them. Fixed by switching to `mock.OnArgs` on the exact command line, at which point
  both mutations killed the tests.
- **T3's `TestTheFirstPollLogsNoDrops` is non-discriminating** and shipped that way. It tests
  Go's nil-map semantics.

Separately, **three prose claims were wrong**, and they are the three corrections above: the
"provably the same total order" claim, defect 3's colon rationale, and the
single-versus-double-quote claim.

**Which mechanism caught which class, because they did not overlap:**

- **The mandated per-test mutation check caught every test defect.** T1's implementer found its
  own vacuous tests that way and fixed them before reporting; T3's found that the brief's
  predicted failure did not happen and reported it rather than ticking the box. Neither was
  found by reading.
- **Reviewer re-derivation caught every prose defect, and nothing else could have.** All three
  wrong claims are about mechanisms outside the diff: tmux's target parser, `sh`'s quoting, and
  the relationship between two sort keys. A reviewer reading the diff sees nothing wrong,
  because nothing in the diff *is* wrong. The reviewer who ran `has-session -t '=a.b:c[d+e]'`
  found the colon claim; the one who created `dir_with_'_quote` found the quoting claim
  backwards; the one who tried to construct a clock-skew counterexample found one.

**A fourth mechanism was needed and was not in the plan: re-checking a measurement the design
already treated as settled.** The "0 `slow poll` lines" row was false when written, survived
the design review, the plan, seven task briefs and seven per-task reviews, and was found only
by re-running the grep during documentation. Every per-task review was correctly scoped to its
own diff, and none of them owned that number. The generalisation of `CLAUDE.md`'s phase 6
lesson - a claim is verified only when traced to the line that decides it - extends to
measurements: **a number in a spec is verified only by re-running the command that produced
it**, and a whole-branch review should re-run the design's measurements, not just re-read them.

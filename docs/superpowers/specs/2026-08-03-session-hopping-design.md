# Session hopping: tmux-native navigation, agreeing order, and a measured removal path

Date: 2026-08-03

Spans two repositories: `vigil` and `~/dotfiles`. Parts A, C (vigil half) and D are
vigil; part B and C (dotfiles half) are `~/dotfiles`. The parts are independent of each
other and can land in any order, with one exception noted in part B.

## Problem

Three complaints, which resolve into two defects, one feature, and one bug found while
measuring.

1. **A session takes "a few seconds" to leave vigil's list after `prefix d`.** Not
   reproduced. Every component of that path measures fast (see "What was measured").
   This spec adds instrumentation and does **not** fix it.
2. **`M-j`/`M-k` do not always land on the next session in vigil's list.** Reproduced by
   inspection: the two sides sort by different keys.
3. **Hopping between sessions requires entering vigil.** `prefix v`, then a digit, then
   `q` - four keystrokes and a full-screen popup to do what tmux does natively. Opening a
   PR is the same round trip.

### Why the order disagrees

`~/dotfiles/tmux/.tmux.conf:44-45` orders sessions by `session_id`, numerically.

`vigil`'s default sort is `SortCreated`, which compares `Session.Created` -
`#{session_created}`, in **whole seconds** (`internal/fetch/tmux.go:94`). Two sessions
created in the same second therefore tie, and `SortSessions` is a stable insertion sort
(`internal/session/sort.go:52`), so the tie falls through to the order
`fetch.ListSessions` emitted - which is `sort.Strings` over lines beginning with
`#{session_created}|#{session_name}`, i.e. alphabetical by name.

So the two orders share a primary key and disagree on the tie-break: `session_id` on one
side, session name on the other. They agree whenever no two sessions share a creation
second, which is why the symptom is "doesn't always" rather than "never".

### The notify hook has never worked

Found while reading the daemon log for part C. The default hook
(`internal/config/config.go:57`) is:

```
tmux display-message -d 5000 "vigil: {session} → {new_state}"
```

`ExpandHook` shell-quotes every placeholder except `{flags}`, so `{session}` expands to a
single-quoted word *inside* the hook's own double quotes. Session names produced by
dotfiles' `session_name_from_title` contain literal double quotes - e.g.
`SC-223374 Add bulk "Report Investigation" action` - which terminate the outer
double-quoted string. The result splits into two words and leaks the single quotes:

```
$ sh -c 'printf "[%s]\n" "vigil: '"'"'SC-223374 Add bulk "Report Investigation" action'"'"' → '"'"'approved'"'"'"'
[vigil: 'SC-223374 Add bulk Report]
[Investigation action' → 'approved']
```

`tmux display-message` accepts at most one argument, so every fire failed with
`command display-message: too many arguments (need at most 1)`. The daemon log records
dozens of these on 2026-08-03 alone and not one success.

## What was measured

Before designing anything for complaint 1. All figures from this machine, 2026-08-03.

| gap | measured |
|---|---|
| `slow poll` lines in the entire daemon log | ~~**0**~~ **WRONG. Eight - see below** |
| `tmux display-popup -E` startup, 3 runs | 0.01-0.02s |
| `git-worktree-cleanup` invoke → session gone from `tmux list-sessions` | 0.272s |
| tmux drops the session → daemon snapshot drops the row | 0.968s |
| session created → daemon snapshot has the row | 0.676s |

The removal measurement connected to the daemon socket directly, streamed snapshots,
killed a throwaway session, and timestamped both the disappearance from
`tmux list-sessions` and the first snapshot lacking the row.

**Corrected 2026-08-03: the first row was false when this file was written, and one of the
conclusions below is withdrawn because of it.** The daemon log is opened `O_APPEND` and
never truncated (`internal/daemon/spawn.go:26`); it runs continuously from 09:33:58 to
16:19:13 on 2026-08-03 and contains **eight** `slow poll` lines - six of them 1.0-1.6s at
the freshly dispatched `pr-35108` worktree, and one at **11.1s total, 11.085s in git** at
14:30:27, seven minutes before this file was written (14:37:19).

Three conclusions:

- **0.968s is the floor for a 1s cadence, so vigil is at its limit on that path and is not
  the lag.** This one stands; it rests on the fourth row, not the first.
- ~~**`fillGit` gating publication is dead as a hypothesis.**~~ **Withdrawn.** There is no
  third data point for `CLAUDE.md`'s 2026-08-03 demotion, and an 11-second poll inside a
  1-second cadence is a live mechanism for the reported symptom: publication is blocked for
  the whole of it, so a killed session stays on screen. Six of the eight lines match the
  cold-worktree first-`status` exposure that note names as its remaining unexplained
  suspect. Note also that `logSlowPoll`'s rate limit is a one-minute *window*, so eight
  lines means at least eight distinct minutes containing a slow poll, not eight slow polls.
- `kill_tmux_session` runs early in cleanup's `main` (`git-worktree-cleanup:218` after part
  C's stamps landed; `:206` before), before any worktree removal, so the kill is not
  waiting on git.

The lesson, recorded in the handoff: **a number in a spec is verified only by re-running
the command that produced it.** This one survived a design review, a plan, seven task
briefs and seven per-task reviews, because no per-task diff owned it.

The components sum to roughly 1.3s worst case. That does not account for "a few seconds",
so a cause remains unconfirmed and part C exists to find it. The leading untested
hypothesis is that the popup covers the screen for the *remainder* of cleanup - several
seconds of `git worktree remove`, mise cleanup and branch deletion after the kill - so
the row is already gone by the first moment it can be looked at. Part C's timeline will
confirm or kill that without further guessing.

## A. Ordering (vigil)

`session_id` is a total order, never reused, and equal to creation order. If both sides
sort by it there is no tie-break convention left to keep in sync - which is the actual
goal, given that a cross-repository convention with a test on one side only is this
repository's documented silent-drift failure mode.

vigil is the side that moves, because the tmux bindings must keep working on a machine
with no vigil installed.

- `fetch.RawSession` and `session.Session` gain `ID int` (JSON tag `id`).
- `fetch.ListSessions`' format string gains `#{session_id}` **between `session_created`
  and `session_name`**, i.e. at `parts[1]` after the split:

  ```
  #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}
  ```

  There, not first and not appended, so that the parse rule the existing comment
  defends - "flags are the last three fields, the path is everything between the name and
  them" - is untouched. The consequent index shifts: `name` moves to `parts[2]`, the path
  join to `parts[3:flagStart]`, the short-line guard from `len(parts) < 3` to `< 4`, and
  the path guard from `flagStart > 2` to `> 3`.

  The ID is parsed by stripping the leading `$` and `ParseInt`, mirroring how `created`
  is parsed and ignoring the error the same way (a `RawSession` with `ID` 0 is the
  degraded case part A already handles below).

- `sort.Strings(lines)` **stays.** Its job is dedup determinism - making equal-preference
  panes for one session resolve identically across polls - not display order. Within a
  single session name the ID is constant, so adding the field cannot change dedup
  behavior.

- `SortSessions`' `SortCreated` comparator becomes `(Created, ID)` lexicographic:

  ```go
  if a.Created != b.Created {
      return a.Created < b.Created
  }
  return a.ID < b.ID
  ```

  **Not pure `ID`.** Because `session_created` is monotonic in `session_id` in practice,
  comparing `Created` first and `ID` second yields the same total order as the bindings'
  pure-`ID` sort, while degrading to exactly today's behavior when `ID` is 0 - the case
  of a session hydrated from a cache file written before this change, which self-heals on
  the first poll. A pure-`ID` comparator would order every such session ahead of every
  real one until that poll landed.

  **Corrected 2026-08-03: this was written as "provably the same total order" and that is
  false.** The equivalence rests on the monotonicity assumption and that assumption fails
  under clock skew: if the wall clock moves backwards between two session creations while
  tmux's id counter keeps climbing, `(Created, ID)` and pure `ID` disagree for that pair -
  t=100/`$5` and t=99/`$6` sort `$5, $6` by id and `$6, $5` by `(Created, ID)` - and
  vigil's rows disagree with `M-j`'s walk. The exposure is pre-existing, since `Created`
  was already the primary key, but it is a real hole and nothing detects it. See
  `docs/superpowers/2026-08-03-session-hopping-handoff.md`.

  The mode keeps the name `created`. It still means creation order; it is now exact
  rather than second-granular.

The session table's index column (`view.indexCol`) needs no change: it renders the
loop index over the already-sorted list, so it inherits the new order.

Accepted limitation, stated because it is a real hole and not an oversight: pressing `s`
(cycle sort) or `f` (cycle filter) in vigil makes the orders diverge again, and nothing
detects it. Part B's bindings walk every session in ID order regardless of vigil's
filter. This is accepted on the user's statement that they neither sort nor filter.

### Tests

Per the standing warning in `CLAUDE.md`, every test below gets a mutation check - delete
or invert the subject, watch the test fail, restore - with the failing output pasted into
the task report.

- Two sessions with identical `Created` and out-of-order `ID` sort by `ID`. This is the
  test that would pass with the change deleted if written against distinct `Created`
  values, so it must use a tie.
- A session with `ID` 0 among sessions with real IDs still sorts by `Created` (the cache
  degradation path).
- `ListSessions` parses `ID` from a `$N` field, and a `pane_current_path` containing a
  pipe still yields the correct name, path and flags - the existing pipe test, re-pinned
  against the new field positions.
- `internal/fetch/tmux_test.go` and `realtmux_manual_test.go` fixtures updated to the new
  format string.

## B. Bindings (dotfiles)

One new script, `~/dotfiles/scripts/scripts/tmux-hop`, with one ordered-list function
shared by every caller. No vigil dependency of any kind - not the binary, not the daemon,
not the socket.

```
tmux-hop next     # switch to the following session, wrapping
tmux-hop prev     # switch to the preceding session, wrapping
tmux-hop <n>      # switch to the n-th session, 0-based
```

The ordered list:

```sh
tmux list-sessions -F '#{session_id}|#{session_name}' \
  | sed 's/^\$//' \
  | sort -t'|' -k1,1n \
  | cut -d'|' -f2-
```

`next`/`prev` resolve the target with a single `awk` pass over that list, comparing
session names with `==` and wrapping at both ends. `<n>` is `sed -n "$((n+1))p"`.

This replaces the two inline `run-shell` bodies at `.tmux.conf:44-45` and fixes three
latent defects in them along the way:

- **`grep "^${current}$"` treats the session name as a regex.** Names contain
  metacharacters; `SC-223374 Add bulk "Report Investigation" action` is a live example.
  `awk`'s `==` compares exactly, which subsumes the problem rather than escaping around
  it, and also removes the `grep -A1`/`grep -B1` context trick and the two `[ "$x" = "$current" ]`
  wrap-around special cases.
- **`switch-client -t "$name"` is not an exact match.** Without a `=` prefix tmux may
  resolve `SC-223477` against `SC-2234770`. This is the same load-bearing-prefix hazard
  already documented for `session.QueueItem.SessionPrefix()`'s trailing space. All
  switches become `switch-client -t "=$name"`.
- **`cut -d: -f2` truncates a name containing a colon.** The delimiter becomes `|` with
  `-f2-`.

  **Corrected 2026-08-03: this originally claimed the fix makes a colon-named session
  resolvable, and it does not.** `cut -d'|' -f2-` fixes **extraction** only. A colon-named
  session is untargetable by name on *any* tmux build, because `:` is tmux's own
  session:window target separator: on a fresh, non-sanitizing server
  `has-session -t '=a.b:c[d+e]'` fails with `can't find session: a.b`, and the `=` prefix
  does not change that. The value of the fix is that the old code would have **silently
  switched to a different real session named `a.b`**, where the new code attempts the true
  full name and fails loudly - the same trade as defect 2. Scope is also narrower than
  stated: `lib/tmux.sh`'s `session_name_from_title` already strips `:` and `.` at creation
  (lines 110-111), so this can only reach a session a human named by hand.

`.tmux.conf` changes:

```
bind -n M-j run-shell -b '$HOME/scripts/tmux-hop next'
bind -n M-k run-shell -b '$HOME/scripts/tmux-hop prev'
bind -n M-0 run-shell -b '$HOME/scripts/tmux-hop 0'
...
bind -n M-9 run-shell -b '$HOME/scripts/tmux-hop 9'
bind -n M-o run-shell -b '{ cd "#{pane_current_path}" && gh pr view --web; } || tmux display-message "no PR for this branch"'
```

Ten literal digit bindings rather than a loop, because `.tmux.conf` has no loop
construct. **Indices are 0-based**, matching the panel's index column, so `M-0` is the
first row. No `M-<digit>` binding exists today, so there is nothing to collide with.

`M-o` deliberately covers the **current session only**. Opening another session's PR is
`M-3 M-o` - two keystrokes, now that hopping is one. This keeps the binding stateless and
free of any index-resolution logic, and `gh pr view --web` in the pane's own directory
needs no vigil data. The `{ ...; } ||` grouping is so a failed `cd` also reports rather
than silently doing nothing; the message is then slightly wrong for that case, which is
accepted over a second error branch.

**Corrected 2026-08-03: that fallback is not unconditional.** A literal double quote in
`#{pane_current_path}` - not a single quote, which is inert inside `sh`'s double quotes -
makes the expanded string an `sh` parse error, and a parse error swallows **both** sides of
the `||`, so the `no PR for this branch` message never appears either. The user gets the
shell's `unexpected EOF while looking for matching '"'` in a tmux view-mode overlay
instead. Accepted risk: no worktree path in this toolchain contains a `"`.

The one ordering dependency between parts: **`M-<n>` only matches the panel's index
column once part A lands.** Before that, indices drift on tied creation seconds exactly
as `M-j`/`M-k` do today.

### Tests

`~/dotfiles` has no Go suite; verification is by hand and recorded in the handoff:

- Five sessions, `M-j` five times returns to the start; `M-k` five times likewise.
- A session whose name contains a `.`, a `:` and a `"` is reachable by both `M-j` and
  `M-<n>` and is not matched by a prefix of another name.
- `M-<n>` for each of 0-9 lands on the row vigil's panel draws at that index, with the
  panel visible during the check.
- `M-o` in a session with a PR opens it; in a session without one, the tmux message
  appears rather than nothing.

## C. Instrumentation for complaint 1

Two halves, with different lifetimes on purpose.

**Permanent, vigil.** The daemon logs one line when a poll's session set shrinks:

```
session dropped: <name>
```

Daemon-only and not user-visible, following the `slow poll` precedent exactly - a session
leaving the list is not a failure, and a self-polling client has no log to write to. It
inherits `poll`'s threading rule: `poll` is synchronous per tick, so the previous poll's
name set is a plain `Server` field needing no mutex, the same argument that covers
`gitMemo`. Unlike `slow poll` it needs no rate limit, because it is edge-triggered by an
event the user causes.

This is worth keeping past the diagnosis: it is the only record of when vigil learned a
session was gone, and complaint 1 is the second time that question has come up.

**Temporary, dotfiles.** Timestamps appended to `/tmp/vigil-hop-timing.log`, to be
removed once the timeline is read. Hi-resolution stamps come from
`perl -MTime::HiRes -e 'printf "%.3f\n", Time::HiRes::time'` - macOS `date` has no `%N`,
and `perl` is already a dependency of `git-worktree-cleanup`'s path resolution.

Four points in `git-worktree-done`: `main` entry, after `switch-client`, immediately
before `display-popup`, and after `display-popup` returns. Three in
`git-worktree-cleanup`: `main` entry, after `kill_tmux_session`, and `main` exit.

Combined with the daemon line, one real `prefix d` yields the whole timeline:

```
prefix d pressed          t+0.000
switch-client returned    t+?
popup opened              t+?
cleanup started           t+?
kill-session returned     t+?
daemon: session dropped   t+?
cleanup finished, popup closes  t+?
```

The gap between `kill-session returned` and `daemon: session dropped` is vigil's; every
other gap is dotfiles'. If the largest gap is between `daemon: session dropped` and
`cleanup finished`, the popup-occlusion hypothesis is confirmed and the fix is on the
dotfiles side.

**Complaint 1 is not fixed by this spec.** It gets a follow-up spec written against the
measured timeline.

### Tests

- A poll whose session set shrinks logs exactly one line naming the departed session; a
  poll whose set is unchanged logs nothing; a poll whose set *grows* logs nothing.
- Two sessions departing in one poll log two lines.
- Mutation check on each, as above. The "unchanged set logs nothing" test is the one that
  would pass with the feature deleted, so it must be paired with a positive case in the
  same run.

## D. Notify hook quoting (vigil)

The default in `internal/config/config.go:57` becomes the adjacent-quoting form, so each
shell-quoted placeholder concatenates with its neighbours into a single word:

```
tmux display-message -d 5000 "vigil: "{session}" → "{new_state}
```

Verified to produce exactly one correctly-quoted argument for a session name containing
double quotes:

```
[vigil: SC-223374 Add bulk "Report Investigation" action → approved]
```

This is a fix to the default only. `ExpandHook`'s quoting is correct and does not change;
`rawPlaceholders` is not widened - the hook body is what was wrong.

The form is ugly, and that is inherent: `ExpandHook` guarantees each placeholder is one
shell word, so any hook needing a placeholder *inside* a larger string has to concatenate
rather than interpolate. A comment on the default records this, since the next person to
edit it will reach for the readable form that does not work.

### Tests

- `ExpandHook` on the default `notify` hook, with a session name containing a double
  quote and a space, produces a string that `sh -c` reduces to a single argument. The
  assertion is on the argument count and content after shell parsing, not on the expanded
  string - asserting the string would pass with the quoting still wrong.
- Mutation check: restore the old default, watch the test fail.

## Out of scope

- Fixing complaint 1. Part C only measures it.
- Any vigil CLI surface for hopping. Rejected: tmux navigation must not depend on vigil
  being installed or its daemon running.
- Reimplementing vigil's sort in bash. Rejected: two implementations of one order, which
  is the drift failure mode this repository already documents twice.
- Making vigil's list ignore `s`/`f` so the orders cannot diverge. Not needed under the
  stated usage, and it would remove working features to protect a convention.
- Lowering `tmux_interval`. The measurement shows removal is already at the 1s floor;
  cutting the cadence would quadruple the tmux subprocess rate to fix a gap that is not
  the reported problem.

# Phase 4: state after the branch

Written 2026-07-30, with `phase-4-dispatch` finished in both repositories and **not yet
merged, not yet installed, and not yet verified on a real machine**. That last point is the
most important thing on this page; see "Verification status".

- Design: `docs/superpowers/specs/2026-07-29-phase-4-dispatch-design.md`.
- Executed plan: `docs/superpowers/plans/2026-07-29-phase-4-dispatch.md`, corrected eleven
  times during execution. Where a brief and the shipped code disagree, the code is right.
- Prior state: `docs/superpowers/2026-07-29-phase-3-handoff.md`. Still current on everything
  phase 4 did not touch, and superseded on the effect-ownership race, which is now closed.

Branches: `phase-4-dispatch` in `~/vigil` (head `ddb0176`, from `main` at `ae701e7`) and in
`~/dotfiles` (head `f6fd857`, from `master` at `fe5c784`). Neither half works without the
other.

## What landed

**`vigil dispatch [--cwd <path>] <url-or-id>`.** Validates its input, generates a job id,
dials the daemon socket, spawns a daemon and retries if none answers, writes one `Request`
frame, and waits up to 15 seconds for its job to appear in a snapshot. **Exit 0 means
accepted, not succeeded** - the job outlives the CLI, which is the point.

**The socket is bidirectional.** Clients write `Request`, the daemon writes `Snapshot`, so
direction disambiguates the two frame types and no envelope is needed. `protocol.Version`
stays **1**: `Snapshot.Jobs` is additive, so an old panel ignores it and a new panel sees
nil against an old daemon.

**The snapshot is the ack.** There is no response frame. A refusal is registered as a job in
state `refused`, so it is visible in every panel rather than only to the CLI, and the CLI
can report its reason. This is why `JobRefused` exists as a state distinct from `JobFailed`:
refused means never accepted, failed means accepted, ran and lost. Conflating them made
`vigil dispatch` exit non-zero for work the daemon had actually started.

**A serialized job queue in the daemon**, one job at a time, because two concurrent
`git worktree add` calls in one repository contend on the index lock. Jobs run on their own
goroutine - `poll` is synchronous per tick, so a job run there would freeze every panel's
snapshot stream for the length of a dispatch. Output is streamed line by line into the job's
`Status` through a new `fetch.StreamCommander`.

**`VIGIL_CLIENT`.** The daemon has no tty, so it resolves the most recently active tmux
client per job and exports it into the hook's environment. The shell side reads it in two
places and it is load-bearing for three things: which client gets switched at the end, what
size the session's window is created at, and which orientation the panel picks. It travels
as an environment variable rather than a flag because the alternative threads a parameter
through five levels, one of which re-quotes its arguments into a command string with
`printf '%q'` and runs them through `bash -c`.

**The popup tunnel is gone.** `dispatch-from-chrome` reads the Chrome tab and calls
`vigil dispatch`. It keeps its iTerm activate-or-attach branch: the job ends with a
`switch-client` and the daemon resolves the most recently active client, so with nothing
attached anywhere there is nothing for the teleport to land on.

**`vigil`'s `d` key submits to the daemon** on the same path the CLI uses, which is what
removes the 15-second `RunHook` timeout that could not cover a real dispatch.
`action.Dispatch` is deleted; its validation moved to `dispatch.Validate`, where it guards
the daemon rather than one client.

**One line of dispatch state renders in every client**, below the table in both panel and
dashboard mode, costing a row only while a job exists.

## You must migrate your hook before this works

The design requires:

```toml
dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {input}"
```

The live config still reads `dispatch --detached --non-interactive {input}`. Left alone, the
first dispatch takes `run_worktree_popup`'s non-inline branch and runs `tmux display-popup`
from a client-less daemon, and `--detached` skips the teleport anyway. `vigil` now warns at
startup when the hook still contains `--detached` or `DISPATCH_IN_POPUP`.

Note also that **a hook body cannot contain `${VAR}`**: `ExpandHook` reads every `{...}` as a
placeholder, so a braced shell expansion fails before reaching `sh`. Use `$VAR`.

## Verification status

**Nothing here has been verified on a real machine.** The branch is green - `go test -race`
13/13 packages, `golangci-lint` 0 issues, 89/89 bats - and has been through a task review per
task, eleven fix rounds, a whole-branch review and a scoped re-review of its fix wave. None
of that is the same as having run a dispatch.

The plan's Task 11 exists precisely because **the bats tmux stub returns a constant
`pane_width` and cannot observe real geometry**. That blind spot hid phase 3's 175-column
defect through seven per-task reviews. The headline claim - that a dispatched session comes
out at the client's size with the right panel orientation - rests entirely on a real-machine
check that has not been run.

The Task 11 checklist, still outstanding, is in the plan. The items that matter most:

- A dispatched session comes out at the target client's size, with the orientation that
  client's aspect ratio implies. **A ~175-column panel means the `VIGIL_CLIENT` thread is
  broken somewhere between the daemon and `client_dimensions`.**
- The teleport lands in the new session's `claude` window.
- **SIGTERM the daemon while a dispatch is in flight** and confirm it exits promptly,
  releases `vigild.sock.lock`, and unlinks its socket. This is the terminal consequence of
  the Critical below; it is fixed and unit-measured, but not observed against the real hook
  chain.
- A dispatch with nothing attached anywhere runs and creates the session without switching.
- `tmux show-environment` in a dispatched session, to confirm `VIGIL_CLIENT` did not leak
  into the new session's environment. If it did, a later manual `shortcut-implement` would
  inherit a stale client.

## The Critical found by the final review, and how

`ExecCommander.RunStream` used `exec.CommandContext`, which kills only the direct child.
`cmd.Wait()` then blocked until every descendant closed the inherited stdout pipe. Measured
before the fix: a 100 ms deadline took 30 seconds in one harness and **never returned** in
another. The consequences compounded across three tasks: `dispatch_timeout` bounded nothing;
jobs are serialized so one hung dispatch blocked all later ones; and `Run` waits on
`pendingEffects`, so `vigil daemon` would never exit, never release its flock and never
unlink its socket - after which **no daemon could ever start again**, and with no daemon
there are no `notify` hooks and no `auto_cleanup`.

Fixed with a process group, a `Cancel` that signals the group, and a `WaitDelay` backstop.
The guard matters: `pid <= 1` falls back to `Process.Kill()` so `kill(-1)` is unreachable
and the daemon cannot signal itself.

It is worth recording *how* this was found. Every per-task review passed. It was caught by a
whole-branch reviewer that **measured** the timeout instead of reading it, and confirmed by
a second reviewer that re-measured independently rather than trusting the first. The
per-task reviews could not have found it: each hand-off was locally correct, and the defect
only exists in the composition of a streaming commander, a serialized queue and a shutdown
that waits.

## Landmines

- **`ExecCommander.Run`, the non-streaming path, has the same defect and is NOT fixed.** It
  uses `cmd.Output()` and is used by the `notify` and `cleanup` hooks. Reproduced: a 100 ms
  deadline blocked over 6 seconds and returned **`err = nil`**. Any hook that backgrounds a
  process wedges that effects goroutine forever, and since `Run` waits on `pendingEffects`
  the terminal consequence is identical - a daemon that cannot be restarted. Shipped hook
  defaults do not background anything, which is why this was left, but it is one hook edit
  away. A `cmd.WaitDelay` on `Run` caps it in one line.
- **A dispatched session with no client attached anywhere still gets 80x24.** `VIGIL_CLIENT`
  fixes the case where a client exists; with none, `client_dimensions` has nothing to
  measure and `-x/-y` are omitted. Same balloon as phase 3, narrower window.
- **The bats tmux stub cannot observe geometry.** Anything about pane geometry needs a real
  tmux server. This is the second phase in a row where that blind spot hid or could have
  hidden the phase's headline defect.
- **A job dies with the daemon**, by construction: capturing its output requires it to be a
  child. It leaves the same half-made worktree a dismissed popup left.
- **The status line is only as good as the hook's output.** A script that goes quiet for 40
  seconds shows a stale line for 40 seconds.
- **No dismiss key for a failed job.** It occupies its line for its retention window - 10
  seconds for a success, 10 minutes for a failure or refusal.
- **`~/scripts` is a symlink into `~/dotfiles`.** Editing that checkout edits the user's live
  tooling. The shell half of this phase was done in a separate worktree
  (`~/dotfiles-phase4`) after an interrupted agent left a half-written `lib/tmux.sh` in the
  live path and broke dispatch. Do the same next time.

## Process notes

**Eleven plan defects were caught by implementers, not by reviews**, and every one was the
same shape: the brief's *test* contradicted the brief's *implementation* somewhere else in
the same brief. A braced shell expansion that collides with `ExpandHook`'s placeholder
syntax. A status assertion on a raw line the implementation strips before storing. An
acceptance test that depended on the next task's deliverable. A `net.Pipe` test that could
not observe the defect it was written for, which is why its mutation check passed. None of
these were visible in a diff. Every implementer that reported BLOCKED with evidence was
right.

**Reviewers repeatedly found tests that pass under mutation.** A reviewer deleted the entire
job-worker wiring from `Run` and the suite stayed green. Reverting the panel's row
arithmetic left the suite green while the panel overflowed its pane. `ensure_client` could
be emptied entirely with all four of its tests still passing. The lesson is narrow and
worth repeating: **a test is not evidence until it has been seen to fail.**

**The two findings that mattered most were measured, not read.** The Critical above, and the
fact that a timed-out job could never report the timeout because the commander returns
`signal: killed` rather than a deadline error - which passed review only because the test
injected the error from a fake while the one real-commander test asserted merely `err != nil`.

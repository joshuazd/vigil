# `ExecCommander.Run`: the grandchild-holds-the-pipe defect, closed

Written 2026-08-03. Small - two production edits and six tests - but it closes the item phase 4
deferred, and it turned up one live bug that was not part of the item.

Not merged as anything phase-numbered. This is a single fix off `main`, merged as `df5a3dc`.

Installed and live. The daemon does not restart itself, so the old one was killed by hand and a
client had spawned a replacement within 3s; the four open panels re-exec'd on their own. All five
processes were confirmed on the new binary by inode rather than by pid - `execSelf` is
`syscall.Exec`, so a re-exec'd panel keeps its pid and its start time, and neither shows the
swap.

## What was wrong

Phase 4 fixed this shape in `RunStream` and left it in `Run`, on the reasoning that shipped hook
defaults background nothing, so the defect was latent. The mechanism is unchanged from phase
4's account: `exec.CommandContext` kills only the direct child, and `Wait` does not return until
every descendant that inherited the output fd has closed it. `Run` reaches it through
`cmd.Output()`, whose buffers os/exec also copies through a goroutine.

**Cancellation cannot help on this path, and that is the whole shape of it.** A hook that
backgrounds work exits immediately and successfully; by the time any deadline fires there is no
direct child left to kill. The hook timeout bounded nothing.

Measured at the `RunHook` seam rather than in the commander, since the consequence lives at the
hook level - a `notify` hook of `sleep 30 & echo notified`:

| | Elapsed |
|---|---|
| before | **30.01s** - the descendant's whole life |
| after | **2.01s** - the wait delay |

The daemon runs transition effects on goroutines it waits for in `Run`, so one such hook meant a
daemon that never exited, never released its flock and never unlinked its socket - after which
**no daemon could start at all**. That is why a latent defect was still worth ranking.

## The fix

1. `cmd.WaitDelay = waitDelay` in `Run`. `streamWaitDelay` became `waitDelay`, shared by both
   paths at the same 2s.
2. `exitedCleanly(cmd, err)` on **both** paths, mapping `exec.ErrWaitDelay` to success when the
   process exited 0.

**`Run` deliberately did not get `RunStream`'s process group kill.** Killing descendants is
right for a dispatch that must actually die and wrong for a `notify` hook whose backgrounded
work is the thing the user asked for. The delay bounds the wait either way; the group kill
additionally destroys, and only one of the two callers wants that.

### The second edit is not cosmetic, and it fixes a live bug

The delay alone converts every such hook from a hang into a **failure it is not**. Watched:
adding `WaitDelay` to `Run` made `TestRunReportsACleanExitDespiteALeakedDescendant` fail with
`exec: WaitDelay expired before I/O complete` for a command that exited 0. On `Run` that error
reaches `MergePR`, which reads hook output to decide whether a PR merged, so it would report a
merge that happened as one that did not.

**`RunStream` already had that bug in production.** It has had `WaitDelay` since phase 4, and
`RunHookStream`'s error becomes a failed job, so a dispatch hook that exited 0 while leaving a
worker behind was reported as a failed job - a refusal visible in every panel - for a dispatch
that had worked. `TestRunStreamReportsACleanExitDespiteALeakedDescendant` failed on the
unmodified tree, before any edit. Whether the shipped dispatch chain actually triggers it was
**not** determined; the mechanism is now closed either way.

## Verification

`make test` (`-race`) and `make lint` green. Every new test was watched to fail first, and each
mutation's output is below rather than summarised.

| Test | Mutation | Result |
|---|---|---|
| `TestRunIsBoundedByADescendantHoldingThePipe` | no `WaitDelay` (the original tree) | `returned after 10.014878167s` |
| `TestRunReportsACleanExitDespiteALeakedDescendant` | `WaitDelay` without `exitedCleanly` | `got exec: WaitDelay expired before I/O complete, want nil` |
| `TestRunStreamReportsACleanExitDespiteALeakedDescendant` | the original tree | `got exec: WaitDelay expired before I/O complete, want nil` |
| `TestRunStillReportsAKilledCommandAsAnError` | `exitedCleanly` returns nil always | `got nil, want a kill error` (with 3 phase-4 tests) |
| `TestExitedCleanlyKeepsTheErrorWhenTheProcessFailed` | drop the `ProcessState` check | `got <nil>, want the error kept` |
| `TestRunHookIsBoundedByAHookThatBackgroundsWork` | revert the `Run` fix | `returned after 30.009952375s` |

### The ProcessState check has no live path, and it was measured rather than assumed

`TestRunStillReportsAKilledCommandAsAnError` passed on the unmodified tree, which by this
repository's standing warning makes it suspect. Probing os/exec directly (a throwaway program,
not reasoning about the source) established why:

```
exit0 + leak    err=exec: WaitDelay expired before I/O complete  isWaitDelay=true   success=true
exit3 + leak    err=exit status 3                                isWaitDelay=false  success=false
killed          err=signal: killed                               isWaitDelay=false  success=false
```

**os/exec gives the exit status precedence over the delay**, so `ErrWaitDelay` only ever
surfaces after a clean exit. `exitedCleanly`'s `cmd.ProcessState.Success()` therefore cannot be
reached with a failed process through either commander - it is a fail-closed backstop against
that precedence changing, not a live branch. Rather than ship a guard nothing exercises, it is
pinned by `TestExitedCleanlyKeepsTheErrorWhenTheProcessFailed`, which calls the helper directly
with the pairing os/exec does not currently produce. The mutation table above confirms that test
is the only thing that fails when the check is dropped.

`TestRunStillReportsAKilledCommandAsAnError` was kept but strengthened with a `killBound`
assertion, so it now constrains something the fix could plausibly break - a cancellation
spending the full 2s delay before reporting - rather than only that killed commands error.

## What this does not prove

- **No real-machine verification.** Everything here is `go test`. The 30s → 2s measurement is a
  unit test at the `RunHook` seam, not an observed daemon shutdown. Reproducing the original
  failure live needs a deliberately backgrounding `notify` hook, which no shipped default is.
- **Whether the `RunStream` half was ever hit in production is unknown.** The bug is real and
  the test proves the mechanism; nobody traced the dispatch chain to see if it leaves a
  descendant holding the pipe on the success path. If a user has ever seen a job marked failed
  after a dispatch that plainly worked, this is the first thing to suspect.
- **The 2s delay is inherited, not derived.** It was phase 4's choice for `RunStream` and is now
  shared. Nothing measured whether 2s is the right amount of output to wait for; it is a bound,
  and any bound beats none.
- **Output truncation is silent.** A command whose descendant is still writing at the 2s mark
  now returns success with partial output. For hooks that is right. No caller of `Run` other
  than the hooks can reach it, because git, gh and tmux do not background anything - that is a
  claim about today's callers, and a future caller that does background work would inherit
  silent truncation.

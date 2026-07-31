# Binary refresh and job dismissal: state after the branch

Written 2026-07-30. **Updated 2026-07-31: merged, installed, and verified on the real
machine.** The original version of this page led with "not yet merged, not yet installed,
not yet verified" - all three are now false, and the results are under "Then it was run on
the real machine".

- Design: `docs/superpowers/specs/2026-07-30-binary-refresh-and-job-dismiss-design.md`.
- Executed plan: `docs/superpowers/plans/2026-07-30-binary-refresh-and-job-dismiss.md`,
  amended once during execution. Where the plan and the shipped code disagree, the code is
  right.
- Prior state: `docs/superpowers/2026-07-30-phase-4-handoff.md`. This branch is the two items
  that handoff deferred under "Deferred, with the user's decision on each" - a stale panel
  after `make install`, and a failed job line with no way to clear it - both hit on the first
  day of real dispatch use, neither speculative.

**Merged into local `main` as `832a86e`** on 2026-07-31, 19 commits from `6a2a97c`. The
branch and its worktree are gone; use `git log 6a2a97c..832a86e` to read the work. Nothing
was pushed - `origin/main` is well behind local `main`, as it was for phases 2 through 4.
This is a single-repository change; nothing in `~/dotfiles` is required for it.

## What landed

**A `dismiss` request type.** `protocol.RequestDismiss`, sent with an **empty ID**. The
daemon's `handleRequest` routes it to `jobs.dismissTerminal()`, which removes every job in
`JobFailed` or `JobRefused` and leaves queued, running and succeeded jobs untouched, then
publishes immediately if anything changed. Every other request type still falls through to
`submit`'s existing reason switch, unchanged. There is no ack frame - the job disappearing
from the next snapshot is the ack, the same contract dispatch already has.

**`esc` gained a layer.** The cascade was confirm prompt / multi-selection / dispatch prompt,
then quit. It is now confirm prompt / multi-selection / dispatch prompt, then dismiss a
failed or refused job, then quit. The layer is entered only when `m.jobs` actually contains a
dismissable job, so esc-to-quit is unaffected everywhere else, including the self-polling
path, which never populates `m.jobs` at all.

**`internal/selfbin`.** A new leaf package that stats the running executable's path and
returns a comparable `Stamp{Size, ModNano}` - not `main.version`, because a `git describe
--dirty` build string is identical across two consecutive dirty builds and would miss the
change that matters most during development. Stat, not version, is also the right signal for
how `make install` works: it renames a new file over `~/.local/bin/vigil`, so a running
process keeps its old inode while the path resolves to the new file underneath it.

**Every client re-execs when its binary changes on disk** - on macOS only; see the Linux
landmine below. The check piggybacks on the existing per-tick message on both the daemon-fed
and self-polling paths, rate-limited to `binCheckInterval` (10s), and defers while a confirm
prompt, a dispatch prompt, or a multi-selection is open - the same three states the esc
cascade already protects, because they are unsaved user intent. A floor of
`binRestartFloor` (30s) since the process's own start guards against a pathological re-exec
loop if a stat is somehow nondeterministic.

Three things about `checkBinary` are not what the plan described, and each is a defect found
in the whole-branch review rather than in a per-task one:

- **A changed stamp has to be seen on two consecutive checks before it fires.** `make
  install` renames, which is atomic, but `make build` writes `./vigil` in place, and the
  design explicitly wants that case caught. A stat landing mid-write sees a new mtime and a
  short size; exec'ing that file fails, and by then the TUI is torn down, so the user's panel
  pane is simply gone. The checks are `binCheckInterval` apart, so a half-written file never
  survives the pair. It costs up to one extra check interval of latency before a restart.
- **A failed startup probe is "unknown", not a prior value.** `newModel` discards the ok from
  its startup probe, so a failure left `binAtStart` zero and any later *successful* stat
  compared unequal to it - a restart for a binary that never changed, and, when the failure
  is systematic, a loop the "a re-exec'd process stamps at its own startup" argument does not
  defend against. `checkBinary` now adopts the first stamp it can actually get as the
  baseline, which is what `daemonHealth` was already doing with `!m.binOnDisk.Zero()`.
- **The model returns `tea.Quit`, which it did not before.** See the process note below.

`checkBinary` returns whether this process should quit, both tick arms act on that, and the
restart path calls `m.cancel()` first exactly as the esc-quit path does. `main` then inspects
the returned model after `p.Run()` returns and `syscall.Exec`s the same path with the same
`os.Args` and `os.Environ()`, through an injectable seam (`execSelf`) so this is testable
without a second copy of the binary. The exec cannot happen inside `Update`: Bubble Tea owns
raw mode and the alt screen there.

**The daemon publishes its own binary stamp, and does not restart itself.** `Snapshot.DaemonBin`
carries the stamp the daemon recorded at its own startup. A client compares that against its
own cached on-disk stamp (no extra stat - Part B already has one) and `Model.daemonHealth()`
gains a third case, `daemon outdated`, at the lowest precedence after `no daemon` and
`daemon stale Ns`. An absent stamp - an old daemon that predates this field - counts as
outdated. There is no key to restart the daemon; killing it is sufficient, because a client
respawns it on the next failed probe through the existing `spawnDaemonOnce` path. The
alternative - the daemon noticing its own staleness and restarting or handing off - was
designed and rejected: `closeClients` drops every connection, so every panel would bounce
through daemon-lost and back on every install, and it would add new lifecycle code exactly
where the phase 4 Critical showed lifecycle bugs cost the most. A daemon whose own probe
fails publishes the zero stamp and is called outdated by every client, which is the correct
fail-safe; it now logs that once, because previously it accused itself in silence.
`daemon_bin` carries **no** `omitempty`: `encoding/json` never treats a struct as empty, so
the tag was a no-op, and the additive claim rests on old clients ignoring an unknown key
rather than on the key being absent.

## What was verified and how

`go test -race ./...` across all 14 packages, and `golangci-lint run`, both clean. Every one
of the nine implementation tasks carries its own per-task review, each reported clean, and a
whole-branch review then found six findings on top of them - one Critical (Part B was
unreachable), two Important (the zero startup stamp, the torn read) and three minor. All six
are fixed; the three behavioural ones each have a test that was watched to fail against the
unfixed code and against a targeted mutation of the fix.

### Then it was run on the real machine, 2026-07-31

Against the developer's live setup: 7 work sessions, 8 marked panel panes, a daemon that had
been up since the previous afternoon. Every check below was **observed**, not inferred. The
previous binary was copied to `~/.local/bin/.vigil.rollback` first and was not needed.

- [x] **A panel re-execs itself after `make install`.** Measured at **21 seconds** - the two
      consecutive checks the torn-read fix requires, at a 10s interval. The PID was
      **unchanged** (`syscall.Exec` replaces the image, not the process), the pane stayed
      alive, the table came back intact, and `ps -o args=` still showed `--panel`, so argv
      survived. Tracked by comparing the process's mapped executable inode
      (`lsof -p PID -a -d txt`) against the installed file's, because the PID cannot move.
- [x] **`daemon outdated` appears in a live panel** while the daemon is on the older image,
      in the status bar alongside the session counts.
- [x] **It clears when the daemon is restarted.** Killing the daemon had a client respawn it
      within seconds through the existing `spawnDaemonOnce` path, and the marker went.
- [x] **`esc` clears a failed line across every panel at once.** A dispatch of
      `vigil-verify-not-a-real-target` was accepted, ran, and failed with the hook's real
      reason; the red line appeared in **all 8** panes; one esc press in **one** pane cleared
      it in **all 8**; that pane did not quit.
- [x] **`esc` still quits when there is nothing to dismiss.** Confirmed on a panel with no
      job line, which is the behaviour change most likely to annoy someone.
- [x] **`vigil dispatch` still exits 0 on acceptance** against the new daemon.

### What real use taught that the design did not say

**The first install after this lands cannot re-exec anything.** A panel can only notice its
binary changed if it is already running a binary that has this feature. Every panel running
at the moment this merges predates it, so that one time they must be respawned by hand:

```bash
tmux list-panes -a -F '#{pane_id} #{@vigil_panel}' | awk '$2==1{print $1}' |
  while read -r p; do tmux respawn-pane -k -t "$p" "$HOME/.local/bin/vigil --panel"; done
```

From the second install onward it is automatic. This is inherent to the feature, not a
defect, but it is the kind of thing that reads as "the feature does not work" the first time.

**A respawned panel shows `daemon lost, polling directly` for a few seconds** before its
first snapshot arrives, then reconnects on its own. Pre-existing behaviour, unrelated to this
branch, but it is the first thing a fresh panel prints and it looks alarming.

## What is still NOT verified

- **Nothing on Linux.** See the landmine below: the feature is a silent no-op there, and that
  claim is reasoned from `/proc/self/exe` semantics, not measured.
- **The re-exec has never been observed on a `make build` that writes in place**, only on
  `make install`'s rename. The two-check rule exists for exactly that case and is unit-tested,
  but the real torn-read window has not been hit deliberately.
- **No dashboard has been watched re-exec**, only panels. A dashboard defers while a prompt or
  a selection is open, and that deferral is unit-tested only.

## Landmines

- **On Linux this feature silently does nothing.** `.goreleaser.yml` ships
  `goos: [darwin, linux]`. On Linux `os.Executable()` reads `/proc/self/exe`, and after a
  rename-over that resolves to `"/path/vigil (deleted)"`; the stat then fails, `Current()`
  returns false, and both the client re-exec and the client's own `binOnDisk` become
  permanent no-ops - which also means `daemon outdated` never renders there, since
  `daemonHealth` requires a non-zero `binOnDisk`. It fails closed, so the Linux binary is
  safe, it just does not have this feature. **No Linux fallback was implemented**; a fix
  would have to strip the `" (deleted)"` suffix or read `/proc/self/maps`, and neither has
  been written or tested. Nothing in the code says this today beyond `Current()`'s
  fail-closed comment.
- **A client that loses its daemon keeps a stale job line it cannot clear.** `m.jobs` is not
  cleared by `handleDaemonLost`, and the esc dismiss layer is gated on `m.daemonConn != nil`,
  so a failed or refused job stays rendered with no key able to dismiss it until a daemon
  comes back. Pre-existing from phase 4 - the retention window was always the only way out -
  but this branch is what makes it visible, because it adds the key that works everywhere
  else. The gate is correct as written (there is nowhere to send the frame), so the fix, if
  one is wanted, is clearing or greying `m.jobs` on daemon loss, which is a behaviour
  decision rather than a bug fix and was left alone.
- **A dismiss racing a `vigil dispatch` ack.** The CLI waits up to 15s for its job id to
  appear in a snapshot. Dismissal only touches terminal states (`JobFailed`, `JobRefused`),
  and a job the CLI is still waiting on is queued or running, so today the race is closed by
  construction rather than by timing. **If dismissal is ever extended to succeeded jobs, this
  reopens**: a success dismissed within the ack window would make the CLI report failure for
  work that actually succeeded.
- **A re-exec drops the dashboard's cursor, filter and sort.** None of the three are
  persisted across the exec. Invisible for a panel, which carries no such state; a small,
  real cost for a dashboard, paid once at install time.
- **`daemon outdated` depends on the daemon and the client resolving the same path.** A
  daemon spawned from a binary other than the one the client is running - a `./vigil` in a
  worktree, say - reads as outdated forever. That may be an arguably correct read, but it is
  a confusing way for a user to be told.
- **`ExecCommander.Run`'s missing `WaitDelay` is still outstanding and untouched.** It is the
  phase 4 handoff's top landmine, the non-streaming path used by the `notify` and `cleanup`
  hooks, and this branch does not touch it. A hook that backgrounds a process still wedges
  the daemon permanently; shipped hook defaults do not background anything, which remains the
  only reason it is safe to leave for now.

## Process notes

**The fourth plan defect shipped, and no per-task review could have caught it.** For the
whole of this branch's execution, Part B was dead code: `checkBinary` set `restartRequested`,
`RestartRequested()` read it, `main.restartIfRequested` called that after `p.Run()` returned
- and nothing ever returned `tea.Quit`, so `p.Run()` never returned. Both tick arms called
`m.checkBinary(time.Now())` for its side effect and then returned their normal command. The
design says it plainly ("the model sets a restart flag and returns `tea.Quit`"); the plan's
briefs did not, and the code followed the briefs.

What made it invisible is worth naming, because it is a test-design failure rather than an
attention failure. **Every restart test sat on one side or the other of the broken seam.**
Task 6's tests called `checkBinary` directly and asserted the flag. Task 7's called
`restartIfRequested` directly with a stub model and asserted the exec. Both halves were
correct and both were tested. Nothing drove `Update`, so nothing asserted the one thing that
joins them, and each per-task review saw a task that was internally complete. A whole-branch
review found it in the only way it could be found: by asking what reads the flag.

The fix adds `TestASelfPollTickQuitsWhenTheBinaryChanged` and
`TestADaemonFedTickQuitsWhenTheBinaryChanged`, which go through `Update` with a real
`CollectTickMsg` and `RenderTickMsg` and assert the returned command is `tea.Quit`. The
lesson generalizes past this branch: **a feature whose halves are tested separately needs one
test that starts where the runtime starts.**

**Two of three earlier plan defects in this branch were caught by implementers, not by
review.**

- Task 3's brief built the daemon-routing test for the unknown-type refusal with a nil
  stream (`newJobs(&config.Config{}, nil, nil, ...)`). `submit`'s reason switch checks
  `j.stream == nil` before it checks `req.Type`, so the test as written would have refused on
  "does not stream" and never reached the type check it existed to pin - passing, but for the
  wrong reason, and unable to catch a broken type-routing arm. The implementer ran the
  brief's test text verbatim, watched it fail with the wrong message, and fixed the test's
  commander setup to match the pattern the codebase already uses for this exact refusal
  (`newJobs(testJobsConfig(), newBlockingStream(), fetch.NewMockCommander(), ...)`), leaving
  its assertions untouched.
- Task 5's brief instructed adding a `selfbin` import to `client.go` alongside the other
  modified files. `client.go` never names the `selfbin` type directly - it only forwards
  `snap.DaemonBin`, whose type is inferred - so the import would be unused and the build
  would fail. The implementer caught this before it became a RED cycle and skipped the import
  for that one file, noting why.

**The third was caught by a reviewer, not the implementer.** Task 7's original test,
`TestRestartIfRequestedExecsTheSamePathAndArgv`, asserted only `gotArgv[0] == exe`. An
implementation that dropped `os.Args[1:]` entirely - `execSelf(exe, []string{exe},
os.Environ())` - would still pass: `gotArgv[0]` is still the executable path. That is the
exact shape of "`vigil --panel` re-execs as a dashboard," the failure this task exists to
prevent, and the implementer's own self-review initially mischaracterized the Step 5 mutation
as covering it, when that mutation only proved `restartIfRequested` was reached, not that
argv or environ were inspected. A reviewer found the gap; the fix added an explicit
`reflect.DeepEqual(gotArgv[1:], os.Args[1:])` check and an environ-length check, and both were
confirmed to fail against the two mutations they were written for (argv truncation, environ
drop) before being reverted.

**The controller amended the plan mid-flight to drop a test-only setter.** Task 7's original
brief called for an exported `SetRestartRequestedForTest` on `model.Model` so `main`'s test
could force the restart flag - production API that would exist solely for a test, in a
package that has none. The controller replaced it with a one-method interface
(`restartRequester`, asserting only `RestartRequested() bool`) that `restartIfRequested`
type-asserts against, plus a compile-time check
(`TestTheRealModelSatisfiesRestartRequester`) pinning that the real `model.Model` has not
drifted from the interface `main` depends on. `internal/model` gained no test-only exports
because of this. The amendment landed as `d6bc733`, after the implementer had already been
briefed directly with the amended design - the on-disk brief file was stale at the time
because the controller's edit was landing in the main checkout while this branch's worktree
was mid-flight, and the implementer worked from the corrected instructions rather than the
stale file, recording the discrepancy in their report.

## The lint fix in this same pass

`make lint` failed on four staticcheck `SA5011` findings in test files added by this branch
(`internal/daemon/daemon_test.go`, `internal/model/dismiss_test.go`), each a nil check on a
bound pointer identifier followed by `t.Fatal` with no dereference-shadowing `return`, then a
dereference of the same identifier on the next line. Staticcheck's no-return whitelist covers
`os.Exit`, `log.Fatal*` and `panic`; it does not include `(*testing.T).Fatal`, which only
calls `runtime.Goexit()` and is invisible to a plain control-flow read. Without an explicit
`return`, the dereference is - correctly, per that model - reachable in the nil case. The
codebase already had the fix for this exact shape at `internal/fetch/pr_test.go:66-68`
(nil check, `t.Fatal`, `return`, then dereference), which was the only site of this pattern
not flagged. An explicit `return` was added after each of the two `t.Fatal` calls, matching
that existing convention; nothing else in either test changed, and `make test` and `make
lint` are both clean after the fix.

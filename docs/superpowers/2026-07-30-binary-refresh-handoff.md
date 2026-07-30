# Binary refresh and job dismissal: state after the branch

Written 2026-07-30, with `binary-refresh` finished in this repository and **not yet merged,
not yet installed, and not yet verified on a real machine**. That last point is the most
important thing on this page; see "What was NOT verified".

- Design: `docs/superpowers/specs/2026-07-30-binary-refresh-and-job-dismiss-design.md`.
- Executed plan: `docs/superpowers/plans/2026-07-30-binary-refresh-and-job-dismiss.md`,
  amended once during execution. Where the plan and the shipped code disagree, the code is
  right.
- Prior state: `docs/superpowers/2026-07-30-phase-4-handoff.md`. This branch is the two items
  that handoff deferred under "Deferred, with the user's decision on each" - a stale panel
  after `make install`, and a failed job line with no way to clear it - both hit on the first
  day of real dispatch use, neither speculative.

Branch: `binary-refresh` in `~/vigil` (head `2b81aa0`, from `main` at `6a2a97c`). This is a
single-repository change; nothing in `~/dotfiles` is required for it.

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

**Every client re-execs when its binary changes on disk.** The check piggybacks on the
existing per-tick message on both the daemon-fed and self-polling paths, rate-limited to
`binCheckInterval` (10s), and defers while a confirm prompt, a dispatch prompt, or a
multi-selection is open - the same three states the esc cascade already protects, because
they are unsaved user intent. A floor of `binRestartFloor` (30s) since the process's own
start guards against a pathological re-exec loop if a stat is somehow nondeterministic. The
exec cannot happen inside `Update` - Bubble Tea owns raw mode and the alt screen there - so
the model sets a flag and returns `tea.Quit`; `main` inspects the returned model after
`p.Run()` returns and `syscall.Exec`s the same path with the same `os.Args` and
`os.Environ()`, through an injectable seam (`execSelf`) so this is testable without a second
copy of the binary.

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
where the phase 4 Critical showed lifecycle bugs cost the most.

## What was verified and how

`go test -race ./...` across all 14 packages, and `golangci-lint run`, both clean. Every one
of the nine implementation tasks carries its own per-task review, each reported clean. That
is the complete list. No real binary has been installed and no real panel has been watched
re-exec.

## What was NOT verified

**No real-machine check has been run at all.** The phase 4 handoff recorded that both of that
phase's real defects were invisible to the test suite, and one of them - the PATH bug in
`dispatch-from-chrome` - was structurally uncatchable by it, because the bats harness put a
`vigil` stub on `PATH` in the one environment where the bug could not occur. Nothing here has
had that kind of exposure yet. Specifically unobserved:

- A panel actually re-execing after `make install` and coming back with its table intact.
- `daemon outdated` actually appearing in a live panel while the daemon is on the old image,
  and actually clearing once the daemon is restarted.
- `esc` actually clearing a failed job line across multiple live panels at once, not just in
  the daemon's in-memory job table as asserted by a test against a `net.Pipe`.
- The daemon accepting a real `vigil dispatch` after this branch is installed.

Step 2 of the task-10 brief is the checklist for all of this, and it is explicitly out of
scope for this document: it installs over the developer's live binary and restarts their
running daemon and panels, which needs their authorization and is being handled separately.

## Landmines

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

**Two of three plan defects in this branch were caught by implementers, not by review.**

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

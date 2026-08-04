package fetch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRunStreamDeliversOneCallPerLine(t *testing.T) {
	var got []string
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "printf 'one\\ntwo\\nthree\\n'"},
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	want := []string{"one", "two", "three"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A script that dies mid-sentence still said something, and the status line
// should show it.
func TestRunStreamDeliversAFinalUnterminatedLine(t *testing.T) {
	var got []string
	c := &ExecCommander{}
	_ = c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "printf 'no newline'"},
		func(line string) { got = append(got, line) })
	if len(got) != 1 || got[0] != "no newline" {
		t.Errorf("got %q, want [\"no newline\"]", got)
	}
}

func TestRunStreamReturnsTheExitError(t *testing.T) {
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", "exit 3"}, func(string) {})
	if err == nil {
		t.Fatal("got nil, want an exit error")
	}
}

func TestRunStreamPassesEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	var got []string
	c := &ExecCommander{}
	err := c.RunStream(context.Background(), dir, []string{"VIGIL_CLIENT=/dev/ttys009"},
		"sh", []string{"-c", "printf '%s\\n' \"${VIGIL_CLIENT}\"; pwd"},
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if len(got) != 2 || got[0] != "/dev/ttys009" {
		t.Fatalf("got %q, want the client first", got)
	}
	// macOS resolves /var to /private/var, so compare the resolved paths.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(got[1])
	if gotDir != wantDir {
		t.Errorf("got dir %q, want %q", gotDir, wantDir)
	}
}

func TestRunStreamKilledByContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := &ExecCommander{}
	start := time.Now()
	err := c.RunStream(ctx, "", nil, "sh", []string{"-c", "sleep 5"}, func(string) {})
	if err == nil {
		t.Fatal("got nil, want a kill error")
	}
	// The error alone is not the claim: it arrived at 5.04s before the
	// process group fix, because the direct child's own kill did not end
	// Wait. A deadline that does not bound wall clock bounds nothing.
	if elapsed := time.Since(start); elapsed > killBound {
		t.Fatalf("returned after %v, want the 50ms deadline to bound it", elapsed)
	}
}

// killBound is the wall clock a 50-100ms deadline is allowed to take. Well
// under WaitDelay (2s), so a regression that loses the process group kill and
// falls back to the backstop fails this rather than passing slowly.
const killBound = 1500 * time.Millisecond

// TestRunStreamKillsAGrandchildHoldingThePipe is the measurement finding 1 was
// made by. exec.CommandContext signals only the direct sh; both output streams
// are an io.PipeWriter, so Wait blocks until every descendant that inherited
// the fd closes it. A hook that backgrounds work - which the real dispatch
// chain does - therefore outlived dispatch_timeout entirely, and because Run
// waits on the job goroutine before returning, the daemon could not be shut
// down either.
func TestRunStreamKillsAGrandchildHoldingThePipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := &ExecCommander{}
	start := time.Now()
	err := c.RunStream(ctx, "", nil, "sh",
		[]string{"-c", "exec 2>&1; sleep 30 & echo started; wait"}, func(string) {})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("got nil, want a kill error")
	}
	if elapsed > killBound {
		t.Fatalf("returned after %v, want the 100ms deadline to bound it", elapsed)
	}
}

// leakedDescendant exits 0 immediately and leaves a child holding the stdout it
// inherited. cmd.Output() collects into buffers, so os/exec copies through a
// goroutine that ends only at EOF - which needs every writer closed, not just
// the direct child. This is the RunStream defect phase 4 fixed, on the path
// notify and cleanup take.
const leakedDescendant = "exec 2>&1; sleep 10 & echo started"

// descendantBound is what a bounded Run is allowed to take: the wait delay plus
// slack. Well under the descendant's own 10s, so a Run with no delay set fails
// this rather than passing slowly.
const descendantBound = waitDelay + 500*time.Millisecond

func TestRunIsBoundedByADescendantHoldingThePipe(t *testing.T) {
	c := &ExecCommander{}
	start := time.Now()
	_, _ = c.Run(context.Background(), "", "sh", "-c", leakedDescendant)
	if elapsed := time.Since(start); elapsed > descendantBound {
		t.Fatalf("returned after %v, want the wait delay to bound it", elapsed)
	}
}

// The bound above is worth nothing if it converts every such hook into a
// failure: a command that exited 0 while a descendant held its pipe succeeded
// with truncated output. MergePR reads a hook's output to decide whether the PR
// merged, so a spurious error there reports a merge that happened as one that
// did not.
func TestRunReportsACleanExitDespiteALeakedDescendant(t *testing.T) {
	c := &ExecCommander{}
	out, err := c.Run(context.Background(), "", "sh", "-c", leakedDescendant)
	if err != nil {
		t.Errorf("got %v, want nil for a command that exited 0", err)
	}
	if out != "started" {
		t.Errorf("got output %q, want the output it produced before the delay", out)
	}
}

// The other half of that mapping: only a clean exit is forgiven. A killed
// command must still be an error, or a hook timeout becomes silence. The bound
// is the second claim - a cancellation must not spend the wait delay before
// reporting, and killBound is under it so one that did fails here.
//
// The explicit exec is load-bearing and the bound is unportable without it.
// Run has no process group kill by design, so the deadline can only bound wall
// clock when the direct child is the whole tree: macOS /bin/sh is bash, which
// execs the sole command of a -c and leaves one process, while Linux /bin/sh is
// dash, which forks and leaves sleep holding the inherited pipe after its
// parent is signalled - the wait delay then bounds it at 2s and this fails.
// exec pins the single-process precondition on either shell. The leaked
// descendant is TestRunIsBoundedByADescendantHoldingThePipe's subject, not this
// one's.
func TestRunStillReportsAKilledCommandAsAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := &ExecCommander{}
	start := time.Now()
	_, err := c.Run(ctx, "", "sh", "-c", "exec sleep 10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("got nil, want a kill error")
	}
	if elapsed > killBound {
		t.Errorf("returned after %v, want the deadline rather than the wait delay to bound it", elapsed)
	}
}

// os/exec reports the exit status rather than the delay for a process that
// failed, so exitedCleanly's ProcessState check has no live path to reach it
// through Run - it is a backstop against that precedence changing, and a
// backstop nothing exercises is a guard this repository has shipped broken
// before. This reaches the helper directly with the pairing os/exec does not
// currently produce.
func TestExitedCleanlyKeepsTheErrorWhenTheProcessFailed(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 3")
	if err := cmd.Run(); err == nil {
		t.Fatal("want a populated failing ProcessState to test with")
	}
	if err := exitedCleanly(cmd, exec.ErrWaitDelay); !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("got %v, want the error kept for a process that exited non-zero", err)
	}
}

// RunStream has the delay already, so it is bounded; what it does not have is
// the distinction above. A dispatch hook that exits 0 and leaves a worker
// behind reports ErrWaitDelay, and the job runner turns any error into a failed
// job - a refusal toast in every panel for a dispatch that worked.
func TestRunStreamReportsACleanExitDespiteALeakedDescendant(t *testing.T) {
	c := &ExecCommander{}
	var lines []string
	err := c.RunStream(context.Background(), "", nil, "sh",
		[]string{"-c", leakedDescendant}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Errorf("got %v, want nil for a command that exited 0", err)
	}
	if !reflect.DeepEqual(lines, []string{"started"}) {
		t.Errorf("got %q, want the line it produced before the delay", lines)
	}
}

// kill(-1) signals every process the caller owns, and vigild runs as that
// user, so a cancellation that negated a pid it had not checked would take the
// daemon down with the hook. Nothing is signalled at all without a process to
// signal.
func TestKillProcessGroupRefusesToSignalWithoutAProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true")
	if err := killProcessGroup(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("got %v, want ErrProcessDone for a command that never started", err)
	}
}

func TestMockCommanderStreamsConfiguredOutput(t *testing.T) {
	m := NewMockCommander()
	m.On("dispatch", ">>> one\n>>> two", nil)
	var got []string
	if err := m.RunStream(context.Background(), "", nil, "dispatch", nil,
		func(line string) { got = append(got, line) }); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if len(got) != 2 || got[0] != ">>> one" || got[1] != ">>> two" {
		t.Errorf("got %q", got)
	}
	if len(m.Calls) != 1 || m.Calls[0].Name != "dispatch" {
		t.Errorf("RunStream did not record its call: %+v", m.Calls)
	}
}

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

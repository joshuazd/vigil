package fetch

import (
	"context"
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
	err := c.RunStream(ctx, "", nil, "sh", []string{"-c", "sleep 5"}, func(string) {})
	if err == nil {
		t.Fatal("got nil, want a kill error")
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

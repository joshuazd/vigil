package model

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/view"
)

// TestASnapshotsJobsReachTheModel exercises the daemon path
// (handleSnapshot's non-Local branch), which is dropped entirely without a
// live daemonConn or daemonDecoder (see
// TestHandleSnapshotDropsANonLocalSnapshotWithNoConnection in client_test.go).
// liveDecoder gives it one without a real daemon on the other end.
func TestASnapshotsJobsReachTheModel(t *testing.T) {
	m := newTestModel()
	m.daemonDecoder = liveDecoder()
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs: []protocol.Job{{
			ID: "a", Input: "sc-12345", State: protocol.JobRunning, Status: "classifying",
		}},
	})
	got := updated.(Model)
	if len(got.jobs) != 1 || got.jobs[0].ID != "a" {
		t.Fatalf("got jobs %+v", got.jobs)
	}
	if !strings.Contains(got.View(), "sc-12345") {
		t.Error("the view does not show the job")
	}
}

// The input is deliberately nothing like a session name. panelModel's fixture
// holds "SC-1 alpha", so a job whose input was "sc-1" was matched by a
// substring search that only case sensitivity kept off the table row - one
// strings.ToLower anywhere in the render path and this test would have passed
// with the job line deleted. The assertion is now the marker and the input
// together, which no table row can produce.
func TestThePanelShowsTheJobLineToo(t *testing.T) {
	m := panelModel(t)
	m.daemonDecoder = liveDecoder()
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs:  []protocol.Job{{ID: "a", Input: "zz-98765", State: protocol.JobRunning}},
	})
	rendered := updated.(Model).View()
	if !strings.Contains(rendered, view.JobRunningMarker+" zz-98765") {
		t.Errorf("the panel does not show the job line: %q", rendered)
	}
	for _, s := range m.sessions {
		if strings.Contains(s.Name, "zz-98765") {
			t.Fatalf("the fixture session %q can satisfy the assertion above", s.Name)
		}
	}
}

func TestNoJobsMeansNoJobLine(t *testing.T) {
	m := newTestModel()
	m.daemonDecoder = liveDecoder()
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{Epoch: m.epoch})
	if strings.Contains(updated.(Model).View(), view.JobRunningMarker) {
		t.Error("the view shows a job line with no jobs")
	}
}

func TestTheDispatchKeySubmitsAndValidates(t *testing.T) {
	m := newTestModel()
	m.dispatchActive = true
	m.dispatchInput.SetValue("   ")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.dispatchActive {
		t.Error("the input stayed open")
	}
	if cmd == nil {
		t.Fatal("no command was returned")
	}
	msg := cmd()
	result, ok := msg.(ActionResultMsg)
	if !ok {
		t.Fatalf("got %T, want ActionResultMsg", msg)
	}
	if result.OK {
		t.Error("empty input was accepted")
	}
	if !strings.Contains(result.Message, "empty") {
		t.Errorf("got %q, want an empty-input reason", result.Message)
	}
}

// --- dispatchCwd ---

func TestDispatchCwdResolvesTheMainWorktree(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("git", "worktree /Users/x/portal\nHEAD abc\nbranch refs/heads/main\n\nworktree /Users/x/sc-1\nHEAD def\n", nil)
	if got := dispatchCwd(context.Background(), cmd, "/Users/x/sc-1"); got != "/Users/x/portal" {
		t.Errorf("got %q, want /Users/x/portal", got)
	}
}

func TestDispatchCwdFallsBackToGetwdWithNoGitRoot(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if got := dispatchCwd(context.Background(), fetch.NewMockCommander(), ""); got != wd {
		t.Errorf("got %q, want %q", got, wd)
	}
}

func TestDispatchCwdFallsBackToGetwdWhenGitFails(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	cmd := fetch.NewMockCommander()
	cmd.On("git", "", errors.New("not a repository"))
	if got := dispatchCwd(context.Background(), cmd, "/some/worktree"); got != wd {
		t.Errorf("got %q, want %q", got, wd)
	}
}

// TestTheDispatchKeySubmitsValidInputToTheDaemon covers the path
// TestTheDispatchKeySubmitsAndValidates does not: a valid input actually
// reaching dispatch.Submit and getting acked. Mutating dispatchCmd to skip
// the submission (or dispatchCwd to always return "") would leave every
// other model test green, because none of them dispatch valid input to a
// real socket.
func TestTheDispatchKeySubmitsValidInputToTheDaemon(t *testing.T) {
	// Unix socket paths are length-limited; t.TempDir can be long on macOS.
	dir, err := os.MkdirTemp("", "vd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	sockDir := filepath.Join(dir, "vigil")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(sockDir, "vigild.sock"))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		req, err := protocol.NewRequestDecoder(conn).Next()
		if err != nil {
			return
		}
		_ = protocol.Encode(conn, &protocol.Snapshot{
			Version: protocol.Version,
			Jobs:    []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}},
		})
	}()

	m := newTestModel()
	m.dispatchActive = true
	m.dispatchInput.SetValue("sc-12345")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no command was returned")
	}
	msg := cmd()
	result, ok := msg.(ActionResultMsg)
	if !ok {
		t.Fatalf("got %T, want ActionResultMsg", msg)
	}
	if !result.OK {
		t.Errorf("got %+v, want OK", result)
	}
}

// --- panel row accounting ---

// TestPanelViewFitsItsPaneWithAJobLine is TestPanelViewFitsItsPane's missing
// case: mutating panelView's job-line branch back to the no-job row count
// (m.height-1) leaves every other panel test green, because none of them set
// m.jobs, and a panel that overflows its pane by one row is immediately
// visible to a user and invisible to the rest of the suite.
func TestPanelViewFitsItsPaneWithAJobLine(t *testing.T) {
	m := panelModel(t)
	m.jobs = []protocol.Job{{ID: "a", Input: "sc-1", State: protocol.JobRunning}}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Errorf("rendered %d lines, want %d", len(lines), m.height)
	}
}

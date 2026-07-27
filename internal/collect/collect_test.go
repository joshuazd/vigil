package collect

import (
	"context"
	"testing"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

func TestSnapshotPopulatesSessionsWithGitState(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha\n1700000001|beta|/tmp/beta", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}",
		"alpha|1\nbeta|0", nil)

	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs["git rev-parse --show-toplevel"] = func(ctx context.Context, dir string, args []string) (string, error) {
		switch dir {
		case "/tmp/alpha":
			return "/repo/alpha", nil
		case "/tmp/beta":
			return "/repo/beta", nil
		}
		return "", nil
	}
	cmd.HandlerFuncs["git branch --show-current"] = func(ctx context.Context, dir string, args []string) (string, error) {
		switch dir {
		case "/repo/alpha":
			return "main", nil
		case "/repo/beta":
			return "feature", nil
		}
		return "", nil
	}
	cmd.On("git", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].Name != "alpha" {
		t.Errorf("got name %q, want alpha", sessions[0].Name)
	}
	if !sessions[0].HasBell {
		t.Error("alpha should have a bell flag")
	}
	if sessions[1].HasBell {
		t.Error("beta should not have a bell flag")
	}
	if sessions[0].PanePath != "/tmp/alpha" {
		t.Errorf("got pane path %q, want /tmp/alpha", sessions[0].PanePath)
	}

	if sessions[0].Git.Branch != "main" {
		t.Errorf("alpha: got branch %q, want main", sessions[0].Git.Branch)
	}
	if sessions[0].Git.GitRoot != "/repo/alpha" {
		t.Errorf("alpha: got gitRoot %q, want /repo/alpha", sessions[0].Git.GitRoot)
	}
	if sessions[1].Git.Branch != "feature" {
		t.Errorf("beta: got branch %q, want feature", sessions[1].Git.Branch)
	}
	if sessions[1].Git.GitRoot != "/repo/beta" {
		t.Errorf("beta: got gitRoot %q, want /repo/beta", sessions[1].Git.GitRoot)
	}
}

func TestSnapshotReturnsErrorWhenTmuxFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", context.DeadlineExceeded)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("want error when tmux enumeration fails")
	}
}

func TestSnapshotWithNoSessionsReturnsEmpty(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}", "", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}

func TestNewDefaultsGitWorkers(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	if c.GitWorkers != 8 {
		t.Errorf("got %d git workers, want 8", c.GitWorkers)
	}
}

func TestSnapshotDeduplicatesPRFetchesByBranchAndGitRoot(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/repo/alpha\n1700000001|beta|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}",
		"alpha|0\nbeta|0", nil)

	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs["git rev-parse --show-toplevel"] = func(ctx context.Context, dir string, args []string) (string, error) {
		return "/repo/alpha", nil
	}
	cmd.HandlerFuncs["git branch --show-current"] = func(ctx context.Context, dir string, args []string) (string, error) {
		return "feature", nil
	}
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	ghCalls := 0
	for _, call := range cmd.Calls {
		if call.Name == "gh" {
			ghCalls++
		}
	}
	if ghCalls != 1 {
		t.Errorf("got %d gh calls, want 1 (dedup should happen)", ghCalls)
	}

	if sessions[0].PR == nil || sessions[0].PR.Number != 42 {
		t.Error("alpha: PR should be populated from dedup")
	}
	if sessions[1].PR == nil || sessions[1].PR.Number != 42 {
		t.Error("beta: PR should be populated from dedup")
	}
	if sessions[0].PR != sessions[1].PR {
		t.Error("both sessions should share the same *PRStatus pointer")
	}
}

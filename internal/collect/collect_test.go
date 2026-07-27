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
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)

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

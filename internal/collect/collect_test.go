package collect

import (
	"context"
	"testing"
	"time"

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
	t.Setenv("VIGIL_GIT_WORKERS", "0")
	c := New(&config.Config{}, fetch.NewMockCommander())
	if c.GitWorkers != defaultGitWorkers {
		t.Errorf("got %d git workers, want %d", c.GitWorkers, defaultGitWorkers)
	}
}

func TestNewDefaultsPRInterval(t *testing.T) {
	t.Setenv("VIGIL_PR_INTERVAL", "0")
	c := New(&config.Config{}, fetch.NewMockCommander())
	if c.PRInterval != defaultPRInterval {
		t.Errorf("got PR interval %s, want %s", c.PRInterval, defaultPRInterval)
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

// singleBranchCommander answers tmux and git for one session on one branch,
// leaving the gh response to the caller.
func singleBranchCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	return cmd
}

func countGhCalls(cmd *fetch.MockCommander) int {
	n := 0
	for _, call := range cmd.Calls {
		if call.Name == "gh" {
			n++
		}
	}
	return n
}

func TestSnapshotSkipsPRFetchWithinPRInterval(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)

	c := New(&config.Config{}, cmd)
	first, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls on the first Snapshot, want 1", got)
	}

	second, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGhCalls(cmd); got != 1 {
		t.Errorf("got %d gh calls after two Snapshots, want 1 (memo should skip the refetch)", got)
	}
	if second[0].PR != first[0].PR {
		t.Error("second Snapshot should reuse the memoized *PRStatus pointer")
	}
}

func TestSnapshotRefetchesPRAfterPRInterval(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	now = now.Add(c.PRInterval)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGhCalls(cmd); got != 2 {
		t.Errorf("got %d gh calls, want 2 (the PR interval has elapsed)", got)
	}
}

func TestSnapshotKeepsLastPRWhenFetchFails(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }

	first, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if first[0].PR == nil {
		t.Fatal("first Snapshot should populate PR")
	}

	cmd.On("gh", "not json", nil)
	now = now.Add(c.PRInterval)
	second, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if second[0].PR == nil {
		t.Fatal("a failed PR fetch should keep the last known PR, got nil")
	}
	if second[0].PR != first[0].PR {
		t.Errorf("got PR %+v, want the previous pointer %+v", second[0].PR, first[0].PR)
	}
}

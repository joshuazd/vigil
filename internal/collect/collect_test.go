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
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}", "", nil)
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
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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

func TestNewDefaultsGitInterval(t *testing.T) {
	t.Setenv("VIGIL_GIT_INTERVAL", "0")
	c := New(&config.Config{}, fetch.NewMockCommander())
	if c.GitInterval != defaultGitInterval {
		t.Errorf("got git interval %s, want %s", c.GitInterval, defaultGitInterval)
	}
}

// countGitCalls counts calls to "git rev-parse --show-toplevel", the first
// (and therefore one-per-attempt) call FetchGitStatus makes. Counting every
// "git" call would also pick up "git remote get-url origin", which the PR
// fetch path runs on its own cadence to resolve owner/repo.
func countGitCalls(cmd *fetch.MockCommander) int {
	n := 0
	for _, call := range cmd.Calls {
		if call.Name == "git" && len(call.Args) == 2 && call.Args[0] == "rev-parse" && call.Args[1] == "--show-toplevel" {
			n++
		}
	}
	return n
}

func TestSnapshotSkipsGitFetchWithinGitInterval(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	firstCalls := countGitCalls(cmd)
	if firstCalls == 0 {
		t.Fatal("first Snapshot should have made git calls")
	}

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got != firstCalls {
		t.Errorf("got %d git calls after two Snapshots, want %d (memo should skip the refetch)", got, firstCalls)
	}
}

func TestSnapshotRefetchesGitAfterGitInterval(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	firstCalls := countGitCalls(cmd)

	now = now.Add(c.GitInterval)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got <= firstCalls {
		t.Errorf("got %d git calls, want more than %d (the git interval has elapsed)", got, firstCalls)
	}
}

// TestInvalidateForcesARefetchOfGitAndPR pins the escape hatch a forced
// refresh (an action, the model's Refresh key) relies on: without it, a
// caller that just changed state would have to wait out GitInterval and
// PRInterval to see the memoized values replaced.
func TestInvalidateForcesARefetchOfGitAndPR(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got != 1 {
		t.Fatalf("got %d git calls on the first Snapshot, want 1", got)
	}
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls on the first Snapshot, want 1", got)
	}

	c.Invalidate()
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got != 2 {
		t.Errorf("got %d git calls after Invalidate, want 2 (the memo should have been dropped)", got)
	}
	if got := countGhCalls(cmd); got != 2 {
		t.Errorf("got %d gh calls after Invalidate, want 2 (the memo should have been dropped)", got)
	}
}

// TestSnapshotAppliesMemoizedGitStatusWhenSkipped pins the highest-blast-radius
// failure mode: a tick that skips the git fetch must still populate Git from
// the memo. A fillGit that skips without applying the memo leaves Git zeroed,
// blanking every git column two ticks out of three at the daemon's cadence.
func TestSnapshotAppliesMemoizedGitStatusWhenSkipped(t *testing.T) {
	cmd := singleBranchCommander()

	c := New(&config.Config{}, cmd)
	first, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if first[0].Git.Branch != "feature" {
		t.Fatalf("first Snapshot: got branch %q, want feature", first[0].Git.Branch)
	}

	second, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if second[0].Git.Branch != "feature" {
		t.Errorf("second Snapshot: got branch %q, want feature (memoized Git should carry over on a skipped tick)", second[0].Git.Branch)
	}
	if second[0].Git.GitRoot != "/repo/alpha" {
		t.Errorf("second Snapshot: got gitRoot %q, want /repo/alpha", second[0].Git.GitRoot)
	}
}

func TestSnapshotGitGatingIndependentOfPRElapsing(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	firstGit, firstGh := countGitCalls(cmd), countGhCalls(cmd)

	now = now.Add(c.GitInterval)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got <= firstGit {
		t.Errorf("got %d git calls, want more than %d (git interval elapsed)", got, firstGit)
	}
	if got := countGhCalls(cmd); got != firstGh {
		t.Errorf("got %d gh calls, want %d (PR interval has not elapsed)", got, firstGh)
	}
}

func TestSnapshotPRGatingIndependentOfGitElapsing(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }
	c.GitInterval = 10 * time.Second
	c.PRInterval = 1 * time.Second

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	firstGit, firstGh := countGitCalls(cmd), countGhCalls(cmd)

	now = now.Add(c.PRInterval)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if got := countGitCalls(cmd); got != firstGit {
		t.Errorf("got %d git calls, want %d (git interval has not elapsed)", got, firstGit)
	}
	if got := countGhCalls(cmd); got <= firstGh {
		t.Errorf("got %d gh calls, want more than %d (PR interval elapsed)", got, firstGh)
	}
}

package collect

import (
	"context"
	"sync"
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
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
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

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}

	c.RefreshRemote(ctx)
	if got := countGhCalls(cmd); got != 1 {
		t.Errorf("got %d gh calls after two passes, want 1 (the store should skip the refetch)", got)
	}
	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("third Snapshot: %v", err)
	}
	if second[0].PR != first[0].PR {
		t.Error("the second Snapshot should reuse the stored *PRStatus pointer")
	}
}

func TestSnapshotRefetchesPRAfterPRInterval(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "MERGED"}`, nil)

	now := time.Unix(1700000000, 0)
	c := New(&config.Config{}, cmd)
	c.clock = func() time.Time { return now }

	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	now = now.Add(c.PRInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
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

	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if first[0].PR == nil {
		t.Fatal("the first pass should populate PR")
	}

	cmd.On("gh", "not json", nil)
	now = now.Add(c.PRInterval)
	c.RefreshRemote(ctx)
	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("third Snapshot: %v", err)
	}
	if second[0].PR == nil {
		t.Fatal("a failed PR fetch should keep the last known PR, got nil")
	}
	if second[0].PR != first[0].PR {
		t.Errorf("got PR %+v, want the previous pointer %+v", second[0].PR, first[0].PR)
	}
	if second[0].PRPending {
		t.Error("a failed refetch must not re-mark the branch pending: Detect would stop seeing it")
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

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGitCalls(cmd); got != 1 {
		t.Fatalf("got %d git calls on the first Snapshot, want 1", got)
	}
	if got := countGhCalls(cmd); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}

	c.Invalidate()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGitCalls(cmd); got != 2 {
		t.Errorf("got %d git calls after Invalidate, want 2 (the memo should have been dropped)", got)
	}
	if got := countGhCalls(cmd); got != 2 {
		t.Errorf("got %d gh calls after Invalidate, want 2 (every entry should have been made due)", got)
	}
}

// TestInvalidateDoesNotReMarkBranchesPending is why Invalidate zeroes
// fetchedAt instead of dropping the entries. A pending session is skipped by
// transition.Detect, so an Invalidate that blanked the store would have the
// forced refresh swallow the very transition it was pressed to go and find.
func TestInvalidateDoesNotReMarkBranchesPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	c.Invalidate()
	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("Invalidate must leave the branch resolved: it makes entries due, it does not forget them")
	}
	if sessions[0].PR == nil {
		t.Error("Invalidate must leave the last known PR readable until the refetch lands")
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

	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	firstGit, firstGh := countGitCalls(cmd), countGhCalls(cmd)

	now = now.Add(c.GitInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
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

	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	firstGit, firstGh := countGitCalls(cmd), countGhCalls(cmd)

	now = now.Add(c.PRInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	if got := countGitCalls(cmd); got != firstGit {
		t.Errorf("got %d git calls, want %d (git interval has not elapsed)", got, firstGit)
	}
	if got := countGhCalls(cmd); got <= firstGh {
		t.Errorf("got %d gh calls, want more than %d (PR interval elapsed)", got, firstGh)
	}
}

// TestSnapshotIssuesNoGhCalls is the entire point of the change and nothing
// else pins it. Snapshot may do local work only; every network call belongs to
// a poller running on its own goroutine.
func TestSnapshotIssuesNoGhCalls(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := countGhCalls(cmd); got != 0 {
		t.Errorf("got %d gh calls inside Snapshot, want 0: publication must never be behind a network call", got)
	}
}

// TestSnapshotMarksAnUnresolvedBranchPending and its sibling below are the
// two halves of the contract Detect reads: pending means "never resolved",
// and it must clear once the poller has an answer - including the answer
// "there is no PR".
func TestSnapshotMarksAnUnresolvedBranchPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !sessions[0].PRPending {
		t.Error("want PRPending on the first Snapshot: nothing has fetched a PR yet")
	}
	if sessions[0].PR != nil {
		t.Errorf("got PR %+v, want nil before any pass has run", sessions[0].PR)
	}
}

func TestRefreshRemoteResolvesTheBranchForTheNextSnapshot(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("PRPending should have cleared once the poller had an answer")
	}
	if sessions[0].PR == nil || sessions[0].PR.Number != 42 {
		t.Errorf("got PR %+v, want number 42", sessions[0].PR)
	}
}

// TestAResolvedBranchWithNoPRIsNotPending is the case that would otherwise
// mute a session forever: gh answering "there is no PR" is an answer, and the
// session has to become visible to Detect.
func TestAResolvedBranchWithNoPRIsNotPending(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", "", nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("a branch gh answered for is resolved, even when the answer is that it has no PR")
	}
	if sessions[0].PR != nil {
		t.Errorf("got PR %+v, want nil", sessions[0].PR)
	}
}

// TestASessionWithNoGitRootIsNeverPending: a session outside a repository can
// never have a PR, so marking it pending would hide it from Detect for the
// life of the process.
func TestASessionWithNoGitRootIsNeverPending(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.On("git", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("a session with no git root must not be marked pending")
	}
}

// TestPassEvictsABranchThatLeftTheWorkingSet replaces the pruning the old
// per-Snapshot memo rebuild did for free. Without it the store grows for the
// life of the daemon and a renamed branch keeps its old PR forever.
func TestPassEvictsABranchThatLeftTheWorkingSet(t *testing.T) {
	branch := "feature"
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return branch, nil
		},
	}
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	oldKey := "feature\x00/repo/alpha"
	c.prs.mu.Lock()
	_, present := c.prs.entries[oldKey]
	c.prs.mu.Unlock()
	if !present {
		t.Fatalf("fixture is broken: %q should be in the store after a pass", oldKey)
	}

	branch = "renamed"
	c.Invalidate() // drop the git memo so the new branch is read
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)

	c.prs.mu.Lock()
	_, stillPresent := c.prs.entries[oldKey]
	c.prs.mu.Unlock()
	if stillPresent {
		t.Errorf("%q is still in the store after leaving the working set", oldKey)
	}
}

// TestAFailedEnumerationDoesNotWipeThePRStore: a Snapshot that cannot
// enumerate tmux knows nothing about the working set, so it must not post one.
// A regression that tracked before the error check would post an empty set; a
// pass prunes unconditionally, so the store would be wiped, every session would
// come back PRPending, and Detect skips a pending session - one transient tmux
// failure would silently swallow a real Done, its notify hook and its cleanup.
func TestAFailedEnumerationDoesNotWipeThePRStore(t *testing.T) {
	tmuxBroken := false
	cmd := singleBranchCommander()
	cmd.HandlerFuncs["tmux"] = func(_ context.Context, _ string, args []string) (string, error) {
		if tmuxBroken {
			return "", context.DeadlineExceeded
		}
		if len(args) > 0 && args[0] == "list-windows" {
			return "alpha|0", nil
		}
		return "1700000000|alpha|/repo/alpha", nil
	}
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshRemote(ctx)
	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if sessions[0].PRPending || sessions[0].PR == nil {
		t.Fatalf("fixture is broken: want the branch resolved before the failure, got PRPending=%v PR=%+v",
			sessions[0].PRPending, sessions[0].PR)
	}

	tmuxBroken = true
	if _, err := c.Snapshot(ctx); err == nil {
		t.Fatal("want an error when tmux enumeration fails")
	}
	c.RefreshRemote(ctx)

	tmuxBroken = false
	sessions, err = c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after recovery: %v", err)
	}
	if sessions[0].PRPending {
		t.Error("a failed enumeration must not re-mark the branch pending: Detect would stop seeing it")
	}
	if sessions[0].PR == nil || sessions[0].PR.Number != 42 {
		t.Errorf("got PR %+v, want number 42 still in the store", sessions[0].PR)
	}
}

// TestSnapshotAndRefreshRemoteAreRaceFree drives the two goroutines the design
// actually creates against each other. -race is the assertion; the loop counts
// are only there to make a window.
func TestSnapshotAndRefreshRemoteAreRaceFree(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	ctx := context.Background()
	c := New(&config.Config{}, cmd)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := c.Snapshot(ctx); err != nil {
				t.Errorf("Snapshot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			c.RefreshRemote(ctx)
		}
	}()
	wg.Wait()
}

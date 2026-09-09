package fetch

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFetchGitMetaResolvesBranchAndUnpushed(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "/repo", nil)
	mock.OnArgs("git branch --show-current", "feat-branch", nil)
	mock.OnArgs("git rev-parse --verify origin/feat-branch", "abc123", nil)
	mock.OnArgs("git rev-list origin/feat-branch..HEAD --count", "2", nil)
	mock.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	mock.OnArgs("git merge-base origin/main HEAD", "def456", nil)
	mock.OnArgs("git log -1 --format=%ct def456", fmt.Sprintf("%d", 1000000000), nil)

	status := FetchGitMeta(context.Background(), mock, "/repo")
	if status.Branch != "feat-branch" {
		t.Errorf("branch: got %q", status.Branch)
	}
	if status.GitRoot != "/repo" {
		t.Errorf("gitRoot: got %q, want /repo", status.GitRoot)
	}
	if status.Unpushed != 2 {
		t.Errorf("unpushed: got %d, want 2", status.Unpushed)
	}
	if status.RebaseAgeSecs == nil {
		t.Error("rebase age: got nil, want a value")
	}
}

// The counts are the poller's job from here on. A FetchGitMeta that quietly
// filled them would put status --porcelain back on the publication path with
// nothing else in the suite noticing.
func TestFetchGitMetaRunsNoStatusCall(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "/repo", nil)
	mock.OnArgs("git branch --show-current", "feat-branch", nil)
	mock.OnArgs("git --no-optional-locks status --porcelain", " M file1.go\n?? file2.go", nil)

	status := FetchGitMeta(context.Background(), mock, "/repo")
	if status.Modified != 0 || status.Added != 0 || status.Deleted != 0 {
		t.Errorf("got counts ~%d +%d -%d, want all zero", status.Modified, status.Added, status.Deleted)
	}
	n := mock.CallCountFunc(func(c MockCall) bool {
		return c.Name == "git" && len(c.Args) > 1 && c.Args[1] == "status"
	})
	if n != 0 {
		t.Errorf("got %d status calls, want 0", n)
	}
}

func TestFetchGitMetaNoGitRoot(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "", fmt.Errorf("not a git repo"))

	status := FetchGitMeta(context.Background(), mock, "/not-a-repo")
	if status.Branch != "" {
		t.Errorf("expected empty branch, got %q", status.Branch)
	}
	if status.GitRoot != "" {
		t.Errorf("expected empty gitRoot, got %q", status.GitRoot)
	}
	if n := mock.CallCountFunc(func(c MockCall) bool { return c.Name == "git" }); n != 1 {
		t.Errorf("got %d git calls after rev-parse failed, want 1: FetchGitMeta must not probe a path that is not a repository", n)
	}
}

func TestFetchDirtyCountsEveryXYCode(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git --no-optional-locks status --porcelain",
		"M  file1\n M file2\nMM file3\nA  file4\n?? file5\nD  file6\n D file7\nRM file8\nC  file9", nil)

	m, a, d := FetchDirtyCounts(context.Background(), mock, "/repo")
	// M, " M", MM = 3 modified; RM x='R' adds 1, y='M' adds 1 = 5
	if m != 5 {
		t.Errorf("modified: got %d, want 5", m)
	}
	// A, ??, C = 3 added
	if a != 3 {
		t.Errorf("added: got %d, want 3", a)
	}
	// D, " D" = 2 deleted
	if d != 2 {
		t.Errorf("deleted: got %d, want 2", d)
	}
}

// A detached HEAD - which is what an in-progress rebase looks like - has no
// branch, and FetchGitStatus's early return meant it showed no counts at all.
// FetchDirtyCounts only needs a root, so that gap closes. Pinned because it is
// a deliberate behaviour change, not a side effect.
func TestFetchDirtyCountsNeedsNoBranch(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git --no-optional-locks status --porcelain", " M file1.go\n?? file2.go", nil)

	m, a, _ := FetchDirtyCounts(context.Background(), mock, "/repo")
	if m != 1 || a != 1 {
		t.Errorf("got ~%d +%d, want ~1 +1", m, a)
	}
}

func TestDetectDefaultBranch(t *testing.T) {
	// Clear cache for test isolation
	defaultBranchCache.Delete("/repo")

	mock := NewMockCommander()
	mock.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)

	branch := DetectDefaultBranch(context.Background(), mock, "/repo")
	if branch != "main" {
		t.Errorf("got %q, want main", branch)
	}

	// Clean up
	defaultBranchCache.Delete("/repo")
}

func TestMainWorktreeIsTheFirstWorktreeListed(t *testing.T) {
	m := NewMockCommander()
	m.On("git", "worktree /Users/x/portal\nHEAD abc\nbranch refs/heads/main\n\nworktree /Users/x/sc-1\nHEAD def\n", nil)
	if got := MainWorktree(context.Background(), m, "/Users/x/sc-1"); got != "/Users/x/portal" {
		t.Errorf("got %q, want /Users/x/portal", got)
	}
}

func TestMainWorktreeIsEmptyWhenGitFails(t *testing.T) {
	m := NewMockCommander()
	m.On("git", "", errors.New("not a repository"))
	if got := MainWorktree(context.Background(), m, "/tmp"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

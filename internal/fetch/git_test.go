package fetch

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFetchGitStatusBasic(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "/repo", nil)
	mock.OnArgs("git branch --show-current", "feat-branch", nil)
	mock.OnArgs("git --no-optional-locks status --porcelain", " M file1.go\n?? file2.go\n D file3.go", nil)
	mock.OnArgs("git rev-parse --verify origin/feat-branch", "abc123", nil)
	mock.OnArgs("git rev-list origin/feat-branch..HEAD --count", "2", nil)
	mock.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	mock.OnArgs("git merge-base origin/main HEAD", "def456", nil)
	mock.OnArgs("git log -1 --format=%ct def456", fmt.Sprintf("%d", 1000000000), nil)

	status := FetchGitStatus(context.Background(), mock, "/repo")
	if status.Branch != "feat-branch" {
		t.Errorf("branch: got %q", status.Branch)
	}
	if status.Modified != 1 {
		t.Errorf("modified: got %d, want 1", status.Modified)
	}
	if status.Added != 1 {
		t.Errorf("added: got %d, want 1", status.Added)
	}
	if status.Deleted != 1 {
		t.Errorf("deleted: got %d, want 1", status.Deleted)
	}
	if status.Unpushed != 2 {
		t.Errorf("unpushed: got %d, want 2", status.Unpushed)
	}
}

func TestFetchGitStatusNoGitRoot(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git rev-parse --show-toplevel", "", fmt.Errorf("not a git repo"))

	status := FetchGitStatus(context.Background(), mock, "/not-a-repo")
	if status.Branch != "" {
		t.Errorf("expected empty branch, got %q", status.Branch)
	}
}

func TestParsePorcelainStatuses(t *testing.T) {
	mock := NewMockCommander()
	mock.OnArgs("git --no-optional-locks status --porcelain",
		"M  file1\n M file2\nMM file3\nA  file4\n?? file5\nD  file6\n D file7\nRM file8\nC  file9", nil)

	m, a, d := parsePorcelain(context.Background(), mock, "/repo")
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

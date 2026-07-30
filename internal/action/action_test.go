package action

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

// --- MergePR ---

func TestMergePR_HookSucceeds(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]any{"merge": "echo done"}}
	cmd := fetch.NewMockCommander()
	out, err := MergePR(context.Background(), cfg, cmd, t.TempDir(), "feat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "merged" {
		t.Errorf("got %q, want %q", out, "merged")
	}
}

func TestMergePR_HookFailsStateMerged(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Hooks: map[string]any{"merge": "false"}}
	cmd := fetch.NewMockCommander()
	cmd.On("sh", "", fmt.Errorf("exit status 1"))
	cmd.OnArgs("gh pr view feat --json state --jq .state", "MERGED", nil)

	out, err := MergePR(context.Background(), cfg, cmd, dir, "feat")
	if err != nil {
		t.Fatalf("expected success when PR is merged despite hook failure, got: %v", err)
	}
	if out != "merged (branch cleanup may have failed)" {
		t.Errorf("got %q, want %q", out, "merged (branch cleanup may have failed)")
	}
}

func TestMergePR_HookFailsPRNotMerged(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Hooks: map[string]any{"merge": "false"}}
	cmd := fetch.NewMockCommander()
	cmd.On("sh", "", fmt.Errorf("exit status 1"))
	cmd.OnArgs("gh pr view feat --json state --jq .state", "OPEN", nil)

	out, err := MergePR(context.Background(), cfg, cmd, dir, "feat")
	if err == nil {
		t.Fatal("expected error when hook fails and PR is not merged")
	}
	if !strings.HasPrefix(out, "merge failed: ") {
		t.Errorf("expected 'merge failed: ' prefix, got %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("message must be single-line, got %q", out)
	}
}

// --- ApprovePR ---

func TestApprovePR_UsesApproveHook(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]any{"approve": "echo approved"}}
	cmd := fetch.NewMockCommander()
	out, err := ApprovePR(context.Background(), cfg, cmd, "/repo", "feat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "approved" {
		t.Errorf("got %q, want %q", out, "approved")
	}
}

// --- CleanupSession ---

func TestCleanupSession_CustomHook(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]any{"cleanup": "echo cleaned"}}
	cmd := fetch.NewMockCommander()
	// CurrentSession returns empty — not current session
	cmd.On("tmux", "", nil)

	_, err := CleanupSession(context.Background(), cfg, cmd, "mysession", "/path", "feat", "/repo")
	// Hook runs via sh -c
	_ = err
}

func TestCleanupSession_BuiltinKillsSession(t *testing.T) {
	cfg := &config.Config{} // no cleanup hook configured
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", nil) // CurrentSession + kill-session

	out, err := CleanupSession(context.Background(), cfg, cmd, "mysession", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "killed session") {
		t.Errorf("expected killed session in output, got %q", out)
	}
}

// TestCleanupSession_KillsTheExactSessionByName pins the "=" exact-match
// prefix on kill-session's target. Without it, tmux falls back to prefix and
// fnmatch matching on -t, so `kill-session -t alpha` can hit an unrelated
// session like "alpha2" or "alpha|pha" instead of the one this call means to
// destroy. SwitchClient and CapturePane in this package already use "=";
// kill-session did not.
func TestCleanupSession_KillsTheExactSessionByName(t *testing.T) {
	cfg := &config.Config{}
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", nil)

	if _, err := CleanupSession(context.Background(), cfg, cmd, "mysession", "", "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var killed bool
	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) == 3 &&
			c.Args[0] == "kill-session" && c.Args[1] == "-t" && c.Args[2] == "=mysession" {
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no exact `tmux kill-session -t =mysession` in %+v", cmd.Calls)
	}
}

func TestCleanupSession_SkipsWorktreeForNonWorktree(t *testing.T) {
	cfg := &config.Config{}
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", nil)

	// Pass a path that doesn't have a .git file (isWorktree returns false)
	out, err := CleanupSession(context.Background(), cfg, cmd, "mysession", "/nonexistent", "feat", "/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not attempt worktree removal
	for _, c := range cmd.Calls {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "worktree" {
			t.Error("should not attempt worktree removal for non-worktree path")
		}
	}
	_ = out
}

func TestCleanupSession_HookWithMultiLineOutput(t *testing.T) {
	// The user's real cleanup hook emits ~9 colored info lines.
	// Verify we don't leak that into the notification message.
	multiLine := "\x1b[32m>>> killing session\x1b[0m\n" +
		"\x1b[32m>>> removing worktree\x1b[0m\n" +
		"\x1b[32m>>> Cleanup complete!\x1b[0m\n"
	hook := "printf '%s' '" + multiLine + "'"
	cfg := &config.Config{Hooks: map[string]any{"cleanup": hook}}
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", nil) // CurrentSession returns ""

	out, err := CleanupSession(context.Background(), cfg, cmd, "s", "/p", "feat", "/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "cleaned up" {
		t.Errorf("expected 'cleaned up' on success, got %q", out)
	}
}

// --- RebaseAndPush ---

func TestRebaseAndPush_HappyPath(t *testing.T) {
	cmd := fetch.NewMockCommander()
	// DetectDefaultBranch: symbolic-ref returns main
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	// fetch, merge-tree, rebase, push all succeed
	cmd.On("git fetch", "", nil)
	cmd.On("git merge-tree", "", nil)
	cmd.On("git rebase", "", nil)
	cmd.On("git push", "", nil)

	out, err := RebaseAndPush(context.Background(), cmd, "/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "rebased and pushed" {
		t.Errorf("got %q", out)
	}
}

func TestRebaseAndPush_NoDefaultBranch(t *testing.T) {
	cmd := fetch.NewMockCommander()
	// All git commands fail — DetectDefaultBranch returns ""
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git symbolic-ref": func(_ context.Context, _ string, _ []string) (string, error) {
			return "", fmt.Errorf("not found")
		},
		"git rev-parse": func(_ context.Context, _ string, _ []string) (string, error) {
			return "", fmt.Errorf("not found")
		},
	}

	// Use a unique path to avoid sync.Map cache hits from other tests
	out, err := RebaseAndPush(context.Background(), cmd, t.TempDir())
	if err == nil {
		t.Fatal("expected error for no default branch")
	}
	if !strings.Contains(err.Error(), "no default branch") {
		t.Errorf("expected 'no default branch' error, got: %v", err)
	}
	if !strings.HasPrefix(out, "rebase failed: ") {
		t.Errorf("expected 'rebase failed: ' prefix, got %q", out)
	}
}

func TestRebaseAndPush_FetchFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git fetch": func(_ context.Context, _ string, _ []string) (string, error) {
			return "", fmt.Errorf("network error")
		},
	}

	out, err := RebaseAndPush(context.Background(), cmd, "/repo")
	if err == nil || !strings.Contains(err.Error(), "fetch failed") {
		t.Errorf("expected 'fetch failed' error, got: %v", err)
	}
	if !strings.HasPrefix(out, "rebase failed: ") {
		t.Errorf("expected 'rebase failed: ' prefix, got %q", out)
	}
}

func TestRebaseAndPush_ConflictsDetected(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.On("git fetch", "", nil)
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git merge-tree": func(_ context.Context, _ string, _ []string) (string, error) {
			return "", fmt.Errorf("conflict")
		},
	}

	out, err := RebaseAndPush(context.Background(), cmd, "/repo")
	if err == nil || !strings.Contains(err.Error(), "conflicts detected") {
		t.Errorf("expected 'conflicts detected' error, got: %v", err)
	}
	if !strings.HasPrefix(out, "rebase failed: ") {
		t.Errorf("expected 'rebase failed: ' prefix, got %q", out)
	}
}

func TestRebaseAndPush_RebaseFailsAbortsRebase(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.On("git fetch", "", nil)
	cmd.On("git merge-tree", "", nil)
	rebaseCalls := 0
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git rebase": func(_ context.Context, _ string, args []string) (string, error) {
			rebaseCalls++
			if len(args) > 0 && args[0] == "--abort" {
				return "", nil // abort succeeds
			}
			return "", fmt.Errorf("merge conflict")
		},
	}

	out, err := RebaseAndPush(context.Background(), cmd, "/repo")
	if err == nil || !strings.Contains(err.Error(), "rebase failed") {
		t.Errorf("expected 'rebase failed' error, got: %v", err)
	}
	if rebaseCalls < 2 {
		t.Error("expected rebase --abort to be called after failure")
	}
	if !strings.HasPrefix(out, "rebase failed: ") {
		t.Errorf("expected 'rebase failed: ' prefix, got %q", out)
	}
}

func TestRebaseAndPush_PushFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.On("git fetch", "", nil)
	cmd.On("git merge-tree", "", nil)
	cmd.On("git rebase", "", nil)
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git push": func(_ context.Context, _ string, _ []string) (string, error) {
			return "", fmt.Errorf("rejected")
		},
	}

	out, err := RebaseAndPush(context.Background(), cmd, "/repo")
	if err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Errorf("expected 'push failed' error, got: %v", err)
	}
	if !strings.HasPrefix(out, "rebase failed: ") {
		t.Errorf("expected 'rebase failed: ' prefix, got %q", out)
	}
}

// --- ToggleDraft ---

func TestToggleDraft_DraftToReady(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("gh", "", nil)

	out, err := ToggleDraft(context.Background(), cmd, "/repo", "feat", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "marked ready" {
		t.Errorf("got %q, want 'marked ready'", out)
	}
}

func TestToggleDraft_ReadyToDraft(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("gh", "", nil)

	out, err := ToggleDraft(context.Background(), cmd, "/repo", "feat", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "converted to draft" {
		t.Errorf("got %q, want 'converted to draft'", out)
	}
}


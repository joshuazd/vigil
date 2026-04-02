package action

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

// MergePR squash-merges the PR for the given branch.
func MergePR(ctx context.Context, cfg *config.Config, cmd fetch.Commander, gitRoot, branch string) (string, error) {
	out, err := cfg.RunHook("merge", map[string]string{
		"branch": branch, "git_root": gitRoot,
	}, gitRoot, 30_000_000_000) // 30s
	if err != nil {
		// gh pr merge --delete-branch exits 1 if branch deletion fails
		// even though merge itself succeeded — verify PR state
		combined := strings.ToLower(out + " " + err.Error())
		if strings.Contains(combined, "merged") {
			return out, nil
		}
		// Check if the PR was actually merged despite the error
		state, stateErr := cmd.Run(ctx, gitRoot, "gh", "pr", "view", branch, "--json", "state", "--jq", ".state")
		if stateErr == nil && strings.TrimSpace(state) == "MERGED" {
			return "merged (branch cleanup may have failed)", nil
		}
		return out, err
	}
	return out, nil
}

// ApprovePR approves the PR for the given branch.
func ApprovePR(ctx context.Context, cfg *config.Config, gitRoot, branch string) (string, error) {
	return cfg.RunHook("approve", map[string]string{
		"branch": branch, "git_root": gitRoot,
	}, gitRoot, 30_000_000_000)
}

// CleanupSession kills a tmux session and optionally removes its worktree.
// If the session is the current tmux session, it switches to the most recent
// other session first so the user isn't kicked out of tmux.
func CleanupSession(ctx context.Context, cfg *config.Config, cmd fetch.Commander, sessionName, worktreePath, branch, gitRoot string) (string, error) {
	switchAwayIfCurrent(ctx, cmd, sessionName)

	hook := cfg.GetHook("cleanup")
	if hook != "" {
		return cfg.RunHook("cleanup", map[string]string{
			"session": sessionName, "path": worktreePath,
			"branch": branch, "git_root": gitRoot,
		}, "", 30_000_000_000)
	}
	return builtinCleanup(ctx, cmd, sessionName, worktreePath, gitRoot)
}

// switchAwayIfCurrent switches the tmux client to the most recent other session
// if sessionName is the current session. This prevents kill-session from
// dropping the user out of tmux.
func switchAwayIfCurrent(ctx context.Context, cmd fetch.Commander, sessionName string) {
	current := fetch.CurrentSession(ctx, cmd)
	if current == "" || current != sessionName {
		return
	}
	fallback := fetch.MostRecentSession(ctx, cmd, sessionName)
	if fallback != "" {
		_ = fetch.SwitchClient(ctx, cmd, fallback)
	}
}

func builtinCleanup(ctx context.Context, cmd fetch.Commander, sessionName, worktreePath, gitRoot string) (string, error) {
	var messages []string

	// Kill tmux session
	_, err := cmd.Run(ctx, "", "tmux", "kill-session", "-t", sessionName)
	if err == nil {
		messages = append(messages, "killed session "+sessionName)
	}

	// Remove worktree if applicable
	if gitRoot != "" && worktreePath != "" && isWorktree(worktreePath) {
		// Check for dirty state
		dirty, _ := cmd.Run(ctx, worktreePath, "git", "status", "--porcelain")
		if strings.TrimSpace(dirty) != "" {
			messages = append(messages, "warning: uncommitted changes")
		}

		_, err := cmd.Run(ctx, gitRoot, "git", "worktree", "remove", "--force", worktreePath)
		if err != nil {
			return strings.Join(messages, "; "), fmt.Errorf("worktree remove failed: %w", err)
		}
		messages = append(messages, "removed worktree "+worktreePath)
	}

	if len(messages) == 0 {
		return "cleaned up", nil
	}
	return strings.Join(messages, "; "), nil
}

func isWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return !info.IsDir() // worktrees have a .git file, not directory
}

// Dispatch routes input via the dispatch hook.
func Dispatch(ctx context.Context, cfg *config.Config, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("dispatch input must not be empty")
	}
	if len(input) > 500 {
		return "", fmt.Errorf("dispatch input too long")
	}
	for _, c := range input {
		if c < ' ' && c != '\t' && c != '\n' {
			return "", fmt.Errorf("dispatch input contains control characters")
		}
	}
	return cfg.RunHook("dispatch", map[string]string{"input": input}, "", 15_000_000_000)
}

// RebaseAndPush fetches, checks for conflicts, rebases, and force-pushes.
// Uses per-step timeouts since network operations can exceed the default 10s.
func RebaseAndPush(ctx context.Context, cmd fetch.Commander, gitRoot string) (string, error) {
	main := fetch.DetectDefaultBranch(ctx, cmd, gitRoot)
	if main == "" {
		return "", fmt.Errorf("no default branch found")
	}

	// Fetch (network I/O — needs longer timeout)
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 30*time.Second)
	defer fetchCancel()
	if _, err := cmd.Run(fetchCtx, gitRoot, "git", "fetch", "origin", main); err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}

	// Conflict check (local only)
	if _, err := cmd.Run(ctx, gitRoot, "git", "merge-tree", "--write-tree", "HEAD", "origin/"+main); err != nil {
		return "", fmt.Errorf("conflicts detected — rebase skipped")
	}

	// Rebase (can be slow on large histories)
	rebaseCtx, rebaseCancel := context.WithTimeout(ctx, 60*time.Second)
	defer rebaseCancel()
	if _, err := cmd.Run(rebaseCtx, gitRoot, "git", "rebase", "origin/"+main); err != nil {
		// Abort on failure — use a generous timeout so the repo isn't left dirty
		abortCtx, abortCancel := context.WithTimeout(ctx, 30*time.Second)
		defer abortCancel()
		_, _ = cmd.Run(abortCtx, gitRoot, "git", "rebase", "--abort")
		return "", fmt.Errorf("rebase failed: %w", err)
	}

	// Force push (network I/O)
	pushCtx, pushCancel := context.WithTimeout(ctx, 30*time.Second)
	defer pushCancel()
	if _, err := cmd.Run(pushCtx, gitRoot, "git", "push", "--force-with-lease"); err != nil {
		return "", fmt.Errorf("push failed: %w", err)
	}

	return "rebased and pushed", nil
}

// ToggleDraft toggles PR draft status.
func ToggleDraft(ctx context.Context, cmd fetch.Commander, gitRoot, branch string, isDraft bool) (string, error) {
	args := []string{"pr", "ready", branch}
	if !isDraft {
		args = append(args, "--undo")
	}
	if _, err := cmd.Run(ctx, gitRoot, "gh", args...); err != nil {
		return "", fmt.Errorf("toggle draft failed: %w", err)
	}
	if isDraft {
		return "marked ready", nil
	}
	return "converted to draft", nil
}

// OpenPRInBrowser opens a URL in the default browser.
// Uses Run() instead of Start() so the process completes before the caller exits.
func OpenPRInBrowser(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Run()
}

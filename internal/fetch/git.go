package fetch

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

var (
	defaultBranchCache sync.Map // map[string]string
)

// FetchGitMeta resolves everything about panePath's repository except the
// dirty counts: the root, the branch, how many commits are unpushed, and how
// stale the branch's merge base is.
//
// These stay on the publication path because they are cheap and because
// Snapshot's later stages need them: groupByBranchRoot feeds prPoller's
// working set from Branch and GitRoot, dirtyPoller keys its working set on
// GitRoot, and transition.Detect reads both off the event. Timed across five
// real polls on 2026-08-03, none of the calls below ever crossed 30ms.
func FetchGitMeta(ctx context.Context, cmd Commander, panePath string) session.GitStatus {
	gitRoot := gitRoot(ctx, cmd, panePath)
	if gitRoot == "" {
		return session.GitStatus{}
	}

	branch := currentBranch(ctx, cmd, gitRoot)
	if branch == "" {
		return session.GitStatus{GitRoot: gitRoot}
	}

	return session.GitStatus{
		Branch:        branch,
		GitRoot:       gitRoot,
		Unpushed:      unpushedCount(ctx, cmd, gitRoot, branch),
		RebaseAgeSecs: rebaseAge(ctx, cmd, gitRoot, branch),
	}
}

func gitRoot(ctx context.Context, cmd Commander, path string) string {
	out, err := cmd.Run(ctx, path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func currentBranch(ctx context.Context, cmd Commander, gitRoot string) string {
	out, err := cmd.Run(ctx, gitRoot, "git", "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// FetchDirtyCounts counts modified, added and deleted paths in gitRoot. It is
// effectively the whole cost of collecting git state: 0.7-1.6s per worktree on
// 2026-07-31, 1348ms cold at ~/sc-231121 on 2026-09-09, against under 30ms for
// every call FetchGitMeta makes. How slow it is is a property of the
// worktree's index rather than of vigil - core.untrackedCache and
// core.fsmonitor put it under 40ms warm - which is why it is a poller's job
// and not Snapshot's.
//
// It takes a git root rather than a pane path because that is where the
// command runs, and because two sessions in one worktree then share an answer.
//
// A failed call returns zeros, indistinguishable from a clean tree. That is
// what it has always done and nothing gates on the difference.
func FetchDirtyCounts(ctx context.Context, cmd Commander, gitRoot string) (modified, added, deleted int) {
	out, err := cmd.Run(ctx, gitRoot, "git", "--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		xy := line[:2]
		switch xy {
		case "M ", " M", "MM":
			modified++
		case "A ", "??":
			added++
		case "D ", " D":
			deleted++
		default:
			x, y := xy[0], xy[1]
			switch x {
			case 'R', 'U':
				modified++
			case 'C':
				added++
			}
			switch y {
			case 'M':
				modified++
			case 'D':
				deleted++
			}
		}
	}
	return
}

// DetectDefaultBranch finds the default branch via remote HEAD or fallback.
func DetectDefaultBranch(ctx context.Context, cmd Commander, gitRoot string) string {
	if cached, ok := defaultBranchCache.Load(gitRoot); ok {
		return cached.(string)
	}

	out, err := cmd.Run(ctx, gitRoot, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil && out != "" {
		// refs/remotes/origin/main -> main
		parts := strings.Split(strings.TrimSpace(out), "/")
		branch := parts[len(parts)-1]
		defaultBranchCache.Store(gitRoot, branch)
		return branch
	}

	// Fallback: check local branches
	for _, name := range []string{"main", "master"} {
		_, err := cmd.Run(ctx, gitRoot, "git", "rev-parse", "--verify", "refs/heads/"+name)
		if err == nil {
			defaultBranchCache.Store(gitRoot, name)
			return name
		}
	}
	return ""
}

func rebaseAge(ctx context.Context, cmd Commander, gitRoot, branch string) *int {
	main := DetectDefaultBranch(ctx, cmd, gitRoot)
	if main == "" || branch == main {
		return nil
	}

	out, err := cmd.Run(ctx, gitRoot, "git", "merge-base", "origin/"+main, "HEAD")
	if err != nil {
		return nil
	}
	base := strings.TrimSpace(out)

	tsOut, err := cmd.Run(ctx, gitRoot, "git", "log", "-1", "--format=%ct", base)
	if err != nil {
		return nil
	}
	baseTS, err := strconv.ParseInt(strings.TrimSpace(tsOut), 10, 64)
	if err != nil {
		return nil
	}
	age := int(time.Now().Unix() - baseTS)
	return &age
}

// MainWorktree returns the main working tree of gitRoot's repository, or "" if
// git cannot answer. A panel's cwd is usually a linked worktree, and a new
// worktree has to be cut from the main one; `worktree list --porcelain` puts
// the main tree first and has done so for far longer than --path-format has
// existed.
func MainWorktree(ctx context.Context, cmd Commander, gitRoot string) string {
	out, err := cmd.Run(ctx, gitRoot, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			return rest
		}
	}
	return ""
}

func unpushedCount(ctx context.Context, cmd Commander, gitRoot, branch string) int {
	// Check remote ref exists
	_, err := cmd.Run(ctx, gitRoot, "git", "rev-parse", "--verify", "origin/"+branch)
	if err != nil {
		return 0
	}
	out, err := cmd.Run(ctx, gitRoot, "git", "rev-list", "origin/"+branch+"..HEAD", "--count")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

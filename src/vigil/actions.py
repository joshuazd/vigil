from __future__ import annotations

import logging
import subprocess
import webbrowser

from . import config
from .git_status import detect_default_branch

logger = logging.getLogger(__name__)


def merge_pr(git_root: str, branch: str) -> str:
    """Squash-merge the PR for the given branch."""
    return config.run_hook("merge", {"branch": branch, "git_root": git_root}, cwd=git_root)


def approve_pr(git_root: str, branch: str) -> str:
    """Approve the PR for the given branch."""
    return config.run_hook("approve", {"branch": branch, "git_root": git_root}, cwd=git_root)


def cleanup_session(
    session_name: str, worktree_path: str, branch: str = "", git_root: str = "",
) -> str:
    """Kill tmux session and remove worktree. Uses hook if configured, otherwise built-in."""
    hook = config.get_hook("cleanup")
    if hook:
        variables = {
            "session": session_name, "path": worktree_path,
            "branch": branch, "git_root": git_root,
        }
        return config.run_hook("cleanup", variables)
    return _builtin_cleanup(session_name, worktree_path, git_root)


def _builtin_cleanup(session_name: str, worktree_path: str, git_root: str) -> str:
    """Built-in cleanup: kill tmux session, remove worktree if applicable."""
    messages: list[str] = []

    # 1. Kill tmux session
    result = subprocess.run(
        ["tmux", "kill-session", "-t", session_name],
        capture_output=True, text=True, timeout=5,
    )
    if result.returncode != 0:
        logger.warning("kill-session failed for %s: %s", session_name, result.stderr.strip())
    else:
        messages.append(f"killed session {session_name}")

    # 2. Remove worktree if path is actually a worktree
    if git_root and worktree_path:
        if _is_worktree(worktree_path, git_root):
            # Warn if dirty
            dirty = subprocess.run(
                ["git", "-C", worktree_path, "status", "--porcelain"],
                capture_output=True, text=True, timeout=10,
            )
            if dirty.stdout.strip():
                logger.warning("Worktree %s has uncommitted changes", worktree_path)
                messages.append("warning: uncommitted changes")

            rm = subprocess.run(
                ["git", "-C", git_root, "worktree", "remove", "--force", worktree_path],
                capture_output=True, text=True, timeout=30,
            )
            if rm.returncode != 0:
                logger.error("worktree remove failed: %s", rm.stderr.strip())
                raise RuntimeError(f"worktree remove failed: {rm.stderr.strip()}")
            messages.append(f"removed worktree {worktree_path}")
        else:
            logger.debug("%s is not a worktree, skipping removal", worktree_path)

    return "; ".join(messages) if messages else "cleaned up"


def _is_worktree(path: str, git_root: str) -> bool:
    """Check if path is a git worktree by checking git worktree list."""
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "worktree", "list", "--porcelain"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            return False
        # Porcelain format has "worktree <path>" lines
        for line in result.stdout.splitlines():
            if line.startswith("worktree ") and line[9:].rstrip() == path:
                return True
        return False
    except Exception:
        return False


def dispatch(input_str: str) -> str:
    """Route input via the dispatch hook."""
    if not input_str or not input_str.strip():
        raise ValueError("dispatch input must not be empty")
    if len(input_str) > 500:
        raise ValueError("dispatch input too long")
    if any(c < " " and c not in "\t\n" for c in input_str):
        raise ValueError("dispatch input contains control characters")
    return config.run_hook("dispatch", {"input": input_str}, timeout=15)


def rebase_and_push(git_root: str) -> str:
    """Fetch origin default branch, check for conflicts, rebase, and force-push."""
    main = detect_default_branch(git_root)
    if not main:
        raise RuntimeError("No default branch found")

    # Fetch latest
    result = subprocess.run(
        ["git", "-C", git_root, "fetch", "origin", main],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        raise RuntimeError(f"fetch failed: {result.stderr.strip()}")

    # Check for conflicts (pure plumbing, no working tree changes)
    result = subprocess.run(
        ["git", "-C", git_root, "merge-tree", "--write-tree", "HEAD", f"origin/{main}"],
        capture_output=True, text=True, timeout=10,
    )
    if result.returncode != 0:
        raise RuntimeError("conflicts detected — rebase skipped")

    # Rebase
    result = subprocess.run(
        ["git", "-C", git_root, "rebase", f"origin/{main}"],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        # Abort if rebase somehow fails despite clean merge-tree
        subprocess.run(
            ["git", "-C", git_root, "rebase", "--abort"],
            capture_output=True, text=True, timeout=10,
        )
        raise RuntimeError(f"rebase failed: {result.stderr.strip()}")

    # Force push
    result = subprocess.run(
        ["git", "-C", git_root, "push", "--force-with-lease"],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        raise RuntimeError(f"push failed: {result.stderr.strip()}")

    return "rebased and pushed"


def open_pr_in_browser(url: str) -> None:
    """Open a URL in the default browser."""
    webbrowser.open(url)

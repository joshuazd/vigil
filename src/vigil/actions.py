from __future__ import annotations

import os
import shutil
import subprocess
import webbrowser


def merge_pr(git_root: str, branch: str) -> str:
    """Squash-merge the PR for the given branch."""
    result = subprocess.run(
        ["gh", "pr", "merge", branch, "--squash", "--delete-branch"],
        cwd=git_root, capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise subprocess.CalledProcessError(
            result.returncode, result.args, result.stdout, result.stderr,
        )
    return result.stdout.strip()


def approve_pr(git_root: str, branch: str) -> str:
    """Approve the PR for the given branch."""
    result = subprocess.run(
        ["gh", "pr", "review", branch, "--approve"],
        cwd=git_root, capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise subprocess.CalledProcessError(
            result.returncode, result.args, result.stdout, result.stderr,
        )
    return result.stdout.strip()


def cleanup_session(session_name: str, worktree_path: str) -> str:
    """Kill tmux session and remove worktree."""
    cmd = shutil.which("git-worktree-cleanup")
    if not cmd:
        raise FileNotFoundError("git-worktree-cleanup not found on PATH")
    result = subprocess.run(
        [cmd, "--session", session_name, worktree_path],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise subprocess.CalledProcessError(
            result.returncode, result.args, result.stdout, result.stderr,
        )
    return result.stdout.strip()


def dispatch(input_str: str) -> str:
    """Route a Shortcut URL or PR number via the dispatch script."""
    cmd = shutil.which("dispatch")
    if not cmd:
        raise FileNotFoundError("dispatch not found on PATH")
    env = {**os.environ, "DISPATCH_IN_POPUP": "1"}
    result = subprocess.run(
        [cmd, "--detached", "--non-interactive", input_str],
        capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        raise subprocess.CalledProcessError(
            result.returncode, result.args, result.stdout, result.stderr,
        )
    return result.stdout.strip()


def rebase_and_push(git_root: str) -> str:
    """Fetch origin/main, check for conflicts, rebase, and force-push."""
    # Find main branch
    main = None
    for name in ("main", "master"):
        result = subprocess.run(
            ["git", "-C", git_root, "rev-parse", "--verify", f"refs/heads/{name}"],
            capture_output=True, text=True,
        )
        if result.returncode == 0:
            main = name
            break
    if not main:
        raise RuntimeError("No main/master branch found")

    # Fetch latest
    result = subprocess.run(
        ["git", "-C", git_root, "fetch", "origin", main],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"fetch failed: {result.stderr.strip()}")

    # Check for conflicts (pure plumbing, no working tree changes)
    result = subprocess.run(
        ["git", "-C", git_root, "merge-tree", "--write-tree", "HEAD", f"origin/{main}"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError("conflicts detected — rebase skipped")

    # Rebase
    result = subprocess.run(
        ["git", "-C", git_root, "rebase", f"origin/{main}"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        # Abort if rebase somehow fails despite clean merge-tree
        subprocess.run(
            ["git", "-C", git_root, "rebase", "--abort"],
            capture_output=True, text=True,
        )
        raise RuntimeError(f"rebase failed: {result.stderr.strip()}")

    # Force push
    result = subprocess.run(
        ["git", "-C", git_root, "push", "--force-with-lease"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"push failed: {result.stderr.strip()}")

    return "rebased and pushed"


def open_pr_in_browser(url: str) -> None:
    """Open a URL in the default browser."""
    webbrowser.open(url)

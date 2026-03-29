from __future__ import annotations

import subprocess
import time

from .models import GitStatus


def fetch(pane_path: str) -> GitStatus:
    """Compute git status for the repo at pane_path."""
    git_root = _git_root(pane_path)
    if not git_root:
        return GitStatus()

    branch = _current_branch(git_root)
    if not branch:
        return GitStatus(git_root=git_root)

    modified, added, deleted = _parse_porcelain(git_root)
    unpushed = _unpushed_count(git_root, branch)
    rebase_age = _rebase_age(git_root, branch)

    return GitStatus(
        branch=branch,
        git_root=git_root,
        modified=modified,
        added=added,
        deleted=deleted,
        unpushed=unpushed,
        rebase_age_seconds=rebase_age,
    )


def _git_root(path: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", path, "rev-parse", "--show-toplevel"],
            capture_output=True, text=True,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except FileNotFoundError:
        return ""


def _current_branch(git_root: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "branch", "--show-current"],
            capture_output=True, text=True,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except FileNotFoundError:
        return ""


def _parse_porcelain(git_root: str) -> tuple[int, int, int]:
    """Parse git status --porcelain, return (modified, added, deleted)."""
    try:
        result = subprocess.run(
            ["git", "--no-optional-locks", "-C", git_root, "status", "--porcelain"],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return 0, 0, 0
    except FileNotFoundError:
        return 0, 0, 0

    modified = added = deleted = 0
    for line in result.stdout.splitlines():
        if len(line) < 2:
            continue
        xy = line[:2]
        if xy in ("M ", " M", "MM"):
            modified += 1
        elif xy in ("A ", "??"):
            added += 1
        elif xy in ("D ", " D"):
            deleted += 1
        else:
            x, y = xy[0], xy[1]
            if x in "RU":
                modified += 1
            elif x == "C":
                added += 1
            if y == "M":
                modified += 1
            elif y == "D":
                deleted += 1

    return modified, added, deleted


def _rebase_age(git_root: str, branch: str) -> int | None:
    """Seconds since the branch last incorporated main. None if on main or error."""
    if branch in ("main", "master"):
        return None
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
        return None
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "merge-base", main, "HEAD"],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return None
        base = result.stdout.strip()
        result = subprocess.run(
            ["git", "-C", git_root, "log", "-1", "--format=%ct", base],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return None
        base_ts = int(result.stdout.strip())
        return int(time.time() - base_ts)
    except (FileNotFoundError, ValueError):
        return None


def _unpushed_count(git_root: str, branch: str) -> int:
    """Count commits ahead of origin/branch."""
    try:
        # Check remote ref exists
        result = subprocess.run(
            ["git", "-C", git_root, "rev-parse", "--verify", f"origin/{branch}"],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return 0
        result = subprocess.run(
            ["git", "-C", git_root, "rev-list", f"origin/{branch}..HEAD", "--count"],
            capture_output=True, text=True,
        )
        return int(result.stdout.strip()) if result.returncode == 0 else 0
    except (FileNotFoundError, ValueError):
        return 0

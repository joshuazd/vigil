from __future__ import annotations

import logging
import subprocess
import time

from .models import GitStatus

logger = logging.getLogger(__name__)

_default_branch_cache: dict[str, str | None] = {}


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
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            logger.warning("git rev-parse failed for %s: %s", path, result.stderr.strip())
            return ""
        return result.stdout.strip()
    except (FileNotFoundError, subprocess.TimeoutExpired) as e:
        logger.warning("git_root failed for %s: %s", path, e)
        return ""


def _current_branch(git_root: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "branch", "--show-current"],
            capture_output=True, text=True, timeout=10,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except (FileNotFoundError, subprocess.TimeoutExpired) as e:
        logger.warning("current_branch failed for %s: %s", git_root, e)
        return ""


def _parse_porcelain(git_root: str) -> tuple[int, int, int]:
    """Parse git status --porcelain, return (modified, added, deleted)."""
    try:
        result = subprocess.run(
            ["git", "--no-optional-locks", "-C", git_root, "status", "--porcelain"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            logger.warning("git status --porcelain failed for %s", git_root)
            return 0, 0, 0
    except (FileNotFoundError, subprocess.TimeoutExpired) as e:
        logger.warning("parse_porcelain failed for %s: %s", git_root, e)
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


def detect_default_branch(git_root: str) -> str | None:
    """Detect the default branch via remote HEAD, falling back to main/master."""
    if git_root in _default_branch_cache:
        return _default_branch_cache[git_root]
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "symbolic-ref", "refs/remotes/origin/HEAD"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode == 0:
            # refs/remotes/origin/main -> main
            ref = result.stdout.strip()
            branch = ref.rsplit("/", 1)[-1]
            _default_branch_cache[git_root] = branch
            return branch
    except (FileNotFoundError, subprocess.TimeoutExpired):
        pass
    # Fallback: check local branches
    for name in ("main", "master"):
        result = subprocess.run(
            ["git", "-C", git_root, "rev-parse", "--verify", f"refs/heads/{name}"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode == 0:
            _default_branch_cache[git_root] = name
            return name
    _default_branch_cache[git_root] = None
    return None


def _rebase_age(git_root: str, branch: str) -> int | None:
    """Seconds since branch last incorporated default branch. None if on default."""
    main = detect_default_branch(git_root)
    if not main or branch == main:
        return None
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "merge-base", main, "HEAD"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            logger.warning("merge-base failed for %s: %s", git_root, result.stderr.strip())
            return None
        base = result.stdout.strip()
        result = subprocess.run(
            ["git", "-C", git_root, "log", "-1", "--format=%ct", base],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            return None
        base_ts = int(result.stdout.strip())
        return int(time.time() - base_ts)
    except (FileNotFoundError, ValueError, subprocess.TimeoutExpired) as e:
        logger.warning("rebase_age failed for %s: %s", git_root, e)
        return None


def _unpushed_count(git_root: str, branch: str) -> int:
    """Count commits ahead of origin/branch."""
    try:
        # Check remote ref exists
        result = subprocess.run(
            ["git", "-C", git_root, "rev-parse", "--verify", f"origin/{branch}"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode != 0:
            return 0
        result = subprocess.run(
            ["git", "-C", git_root, "rev-list", f"origin/{branch}..HEAD", "--count"],
            capture_output=True, text=True, timeout=10,
        )
        return int(result.stdout.strip()) if result.returncode == 0 else 0
    except (FileNotFoundError, ValueError, subprocess.TimeoutExpired) as e:
        logger.warning("unpushed_count failed for %s/%s: %s", git_root, branch, e)
        return 0

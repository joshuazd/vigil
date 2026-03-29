from __future__ import annotations

import json
import subprocess

from .models import PRStatus


def fetch(branch: str, git_root: str) -> PRStatus | None:
    """Fetch PR status for a branch via gh CLI. Returns None if no PR exists."""
    try:
        result = subprocess.run(
            [
                "gh", "pr", "view", branch,
                "--json",
                "number,state,isDraft,url,statusCheckRollup,"
                "reviewDecision,latestReviews,mergeable",
            ],
            cwd=git_root,
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return None

        data = json.loads(result.stdout)
    except (FileNotFoundError, json.JSONDecodeError):
        return None

    number = data.get("number", 0)
    state = data.get("state", "")
    is_draft = data.get("isDraft", False)
    url = data.get("url", "")
    review_decision = data.get("reviewDecision") or ""

    # Check status rollup
    checks = _parse_checks(data.get("statusCheckRollup") or [])

    # Count approvals
    latest_reviews = data.get("latestReviews") or []
    approvals = sum(1 for r in latest_reviews if r.get("state") == "APPROVED")

    # Unresolved review threads (only for open PRs)
    unresolved = 0
    if state == "OPEN":
        unresolved = _fetch_unresolved_threads(git_root, number)

    has_conflicts = data.get("mergeable") == "CONFLICTING"

    return PRStatus(
        number=number,
        state=state,
        is_draft=is_draft,
        url=url,
        checks=checks,
        review_decision=review_decision,
        approvals=approvals,
        unresolved_comments=unresolved,
        has_conflicts=has_conflicts,
    )


def _parse_checks(rollup: list[dict]) -> str:
    """Derive pass/fail/pending from statusCheckRollup array."""
    if not rollup:
        return ""
    statuses = []
    for item in rollup:
        s = item.get("conclusion") or item.get("status") or item.get("state") or ""
        if s:
            statuses.append(s.upper())
    if not statuses:
        return ""
    if any(s in ("FAILURE", "ERROR") for s in statuses):
        return "fail"
    if any(s in ("PENDING", "QUEUED", "IN_PROGRESS", "WAITING") for s in statuses):
        return "pending"
    return "pass"


def _fetch_unresolved_threads(git_root: str, pr_number: int) -> int:
    """Count unresolved, non-outdated review threads via GraphQL."""
    query = """
    query($owner: String!, $repo: String!, $number: Int!) {
      repository(owner: $owner, name: $repo) {
        pullRequest(number: $number) {
          reviewThreads(first: 100) {
            nodes { isResolved isOutdated }
          }
        }
      }
    }
    """
    # Get owner/repo from remote
    nwo = _get_nwo(git_root)
    if not nwo:
        return 0
    owner, repo = nwo

    try:
        result = subprocess.run(
            [
                "gh", "api", "graphql",
                "-f", f"query={query}",
                "-F", f"owner={owner}",
                "-F", f"repo={repo}",
                "-F", f"number={pr_number}",
            ],
            cwd=git_root,
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return 0
        data = json.loads(result.stdout)
        threads = (
            data.get("data", {})
            .get("repository", {})
            .get("pullRequest", {})
            .get("reviewThreads", {})
            .get("nodes", [])
        )
        return sum(
            1 for t in threads
            if not t.get("isResolved", True) and not t.get("isOutdated", False)
        )
    except (FileNotFoundError, json.JSONDecodeError):
        return 0


def _get_nwo(git_root: str) -> tuple[str, str] | None:
    """Extract (owner, repo) from git remote URL."""
    try:
        result = subprocess.run(
            ["git", "-C", git_root, "remote", "get-url", "origin"],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            return None
        url = result.stdout.strip().removesuffix(".git")
        # Handle SSH: git@github.com:owner/repo
        if "github.com:" in url:
            url = url.split("github.com:", 1)[1]
        # Handle HTTPS: https://github.com/owner/repo
        elif "github.com/" in url:
            url = url.split("github.com/", 1)[1]
        else:
            return None
        parts = url.split("/")
        if len(parts) >= 2:
            return parts[0], parts[1]
        return None
    except FileNotFoundError:
        return None

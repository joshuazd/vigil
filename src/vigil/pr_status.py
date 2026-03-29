from __future__ import annotations

import json
import logging
import subprocess
import time

from .models import PRStatus

logger = logging.getLogger(__name__)


def _run_with_retry(
    args: list[str], *, cwd: str | None = None, max_retries: int = 2, backoff: float = 1.0,
) -> subprocess.CompletedProcess:
    """Run subprocess with retry on non-zero exit."""
    for attempt in range(max_retries + 1):
        result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, timeout=15)
        if result.returncode == 0:
            return result
        if attempt < max_retries:
            logger.info("Retrying %s (attempt %d/%d)", args[:3], attempt + 2, max_retries + 1)
            time.sleep(backoff * (attempt + 1))
    return result


def fetch(branch: str, git_root: str) -> PRStatus | None:
    """Fetch PR status for a branch via gh CLI. Returns None if no PR exists."""
    try:
        result = _run_with_retry(
            [
                "gh", "pr", "view", branch,
                "--json",
                "number,state,isDraft,url,statusCheckRollup,"
                "reviewDecision,latestReviews,mergeable",
            ],
            cwd=git_root,
        )
        if result.returncode != 0:
            logger.debug("gh pr view failed for %s: %s", branch, result.stderr.strip())
            return None

        data = json.loads(result.stdout)
    except (FileNotFoundError, json.JSONDecodeError, subprocess.TimeoutExpired) as e:
        logger.warning("pr fetch failed for %s: %s", branch, e)
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
        result = _run_with_retry(
            [
                "gh", "api", "graphql",
                "-f", f"query={query}",
                "-F", f"owner={owner}",
                "-F", f"repo={repo}",
                "-F", f"number={pr_number}",
            ],
            cwd=git_root,
        )
        if result.returncode != 0:
            logger.warning("graphql query failed for PR #%d: %s", pr_number, result.stderr.strip())
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
    except (FileNotFoundError, json.JSONDecodeError, subprocess.TimeoutExpired) as e:
        logger.warning("unresolved threads fetch failed for PR #%d: %s", pr_number, e)
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

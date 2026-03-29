from __future__ import annotations

import json
import os
import tempfile
import time
from dataclasses import asdict
from pathlib import Path

from .models import GitStatus, PRStatus, Session

CACHE_PATH = Path.home() / ".local" / "share" / "vigil" / "cache.json"
MAX_AGE_SECONDS = int(os.environ.get("VIGIL_CACHE_TTL", "30"))


def save(sessions: list[Session]) -> None:
    """Write sessions to cache file (atomic write)."""
    data = {
        "timestamp": int(time.time()),
        "sessions": [_session_to_dict(s) for s in sessions],
    }
    CACHE_PATH.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=CACHE_PATH.parent, suffix=".tmp")
    try:
        with open(fd, "w") as f:
            json.dump(data, f)
        Path(tmp).replace(CACHE_PATH)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def load() -> list[Session] | None:
    """Load sessions from cache. Returns None if missing or stale (>30s)."""
    if not CACHE_PATH.exists():
        return None
    try:
        data = json.loads(CACHE_PATH.read_text())
        ts = data.get("timestamp", 0)
        if time.time() - ts > MAX_AGE_SECONDS:
            return None
        return [_dict_to_session(d) for d in data.get("sessions", [])]
    except (json.JSONDecodeError, KeyError, TypeError):
        return None


def _session_to_dict(s: Session) -> dict:
    d: dict = {
        "name": s.name,
        "pane_path": s.pane_path,
        "created": s.created,
        "has_bell": s.has_bell,
        "git": asdict(s.git),
    }
    if s.pr:
        d["pr"] = asdict(s.pr)
    return d


def _dict_to_session(d: dict) -> Session:
    git_data = d.get("git", {})
    git = GitStatus(
        branch=git_data.get("branch", ""),
        git_root=git_data.get("git_root", ""),
        modified=git_data.get("modified", 0),
        added=git_data.get("added", 0),
        deleted=git_data.get("deleted", 0),
        unpushed=git_data.get("unpushed", 0),
        rebase_age_seconds=git_data.get("rebase_age_seconds"),
    )
    pr = None
    if "pr" in d:
        pr_data = d["pr"]
        pr = PRStatus(
            number=pr_data.get("number", 0),
            state=pr_data.get("state", ""),
            is_draft=pr_data.get("is_draft", False),
            url=pr_data.get("url", ""),
            checks=pr_data.get("checks", ""),
            review_decision=pr_data.get("review_decision", ""),
            approvals=pr_data.get("approvals", 0),
            unresolved_comments=pr_data.get("unresolved_comments", 0),
            has_conflicts=pr_data.get("has_conflicts", False),
        )
    return Session(
        name=d["name"],
        pane_path=d.get("pane_path", ""),
        created=d.get("created", 0),
        has_bell=d.get("has_bell", False),
        git=git,
        pr=pr,
    )

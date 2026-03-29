from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class SessionState(Enum):
    ATTENTION = "attention"    # Bell active — Claude waiting for input
    BLOCKED = "blocked"        # CI fail or changes requested
    UNRESOLVED = "unresolved"  # Unresolved review comments
    MERGEABLE = "mergeable"    # Approved + CI passing — ready to merge
    APPROVED = "approved"      # Approved + CI pending
    PENDING = "pending"        # CI running, no review yet
    REVIEW = "review"          # PR open, CI passing, awaiting review
    DONE = "done"              # PR merged
    IDLE = "idle"              # No PR / fallback


STATE_STYLES = {
    SessionState.ATTENTION: ("bright_yellow", "●"),
    SessionState.BLOCKED: ("bright_red", "●"),
    SessionState.UNRESOLVED: ("bright_red", "○"),
    SessionState.MERGEABLE: ("bright_green", "●"),
    SessionState.APPROVED: ("bright_green", "○"),
    SessionState.PENDING: ("bright_yellow", "○"),
    SessionState.REVIEW: ("cyan", "●"),
    SessionState.DONE: ("dim", "●"),
    SessionState.IDLE: ("", "·"),
}


@dataclass
class GitStatus:
    branch: str = ""
    git_root: str = ""
    modified: int = 0
    added: int = 0
    deleted: int = 0
    unpushed: int = 0
    rebase_age_seconds: int | None = None

    @property
    def is_clean(self) -> bool:
        return self.modified == 0 and self.added == 0 and self.deleted == 0 and self.unpushed == 0

    def rebase_age_display(self) -> str:
        if self.rebase_age_seconds is None or self.rebase_age_seconds < 3600:
            return ""
        if self.rebase_age_seconds < 86400:
            return f"↻ {self.rebase_age_seconds // 3600}h"
        return f"↻ {self.rebase_age_seconds // 86400}d"

    def is_stale(self, threshold: int) -> bool:
        return self.rebase_age_seconds is not None and self.rebase_age_seconds > threshold

    def display(self) -> str:
        parts = []
        if self.modified:
            parts.append(f"~{self.modified}")
        if self.added:
            parts.append(f"+{self.added}")
        if self.deleted:
            parts.append(f"-{self.deleted}")
        if self.unpushed:
            parts.append(f"↑{self.unpushed}")
        age = self.rebase_age_display()
        if age:
            parts.append(age)
        if not parts:
            return "—"
        return " ".join(parts)


@dataclass
class PRStatus:
    number: int = 0
    state: str = ""           # OPEN, MERGED, CLOSED
    is_draft: bool = False
    url: str = ""
    checks: str = ""          # pass, fail, pending, ""
    review_decision: str = "" # APPROVED, CHANGES_REQUESTED, ""
    approvals: int = 0
    unresolved_comments: int = 0
    has_conflicts: bool = False
    title: str = ""
    body: str = ""
    review_comments: list[dict] = field(default_factory=list)

    def display(self) -> str:
        if not self.number:
            return "—"
        parts = [f"#{self.number}"]
        if self.checks == "pass":
            parts.append("✓")
        elif self.checks == "fail":
            parts.append("✗")
        elif self.checks == "pending":
            parts.append("●")
        if self.state == "OPEN":
            if self.review_decision == "APPROVED":
                parts.append("☑")
            elif self.review_decision == "CHANGES_REQUESTED":
                parts.append("✎")
            elif self.approvals > 0:
                parts.append("☑")
            if self.unresolved_comments > 0:
                parts.append(f"☐ {self.unresolved_comments}")
        return " ".join(parts)


@dataclass
class Session:
    name: str
    pane_path: str
    created: int = 0
    is_current: bool = False
    is_last: bool = False
    has_bell: bool = False
    git: GitStatus = field(default_factory=GitStatus)
    pr: PRStatus | None = None

    @property
    def state(self) -> SessionState:
        if self.has_bell:
            return SessionState.ATTENTION
        if not self.pr or not self.pr.number:
            return SessionState.IDLE
        if self.pr.state == "MERGED":
            return SessionState.DONE
        if self.pr.checks == "fail" or self.pr.review_decision == "CHANGES_REQUESTED":
            return SessionState.BLOCKED
        if self.pr.has_conflicts:
            return SessionState.BLOCKED
        if self.pr.unresolved_comments > 0:
            return SessionState.UNRESOLVED
        if self.pr.review_decision == "APPROVED" and self.pr.checks == "pass":
            return SessionState.MERGEABLE
        if self.pr.review_decision == "APPROVED":
            return SessionState.APPROVED
        if self.pr.checks == "pending":
            return SessionState.PENDING
        if self.pr.state == "OPEN" and not self.pr.is_draft:
            return SessionState.REVIEW
        return SessionState.IDLE

    @property
    def indicator(self) -> str:
        if self.is_current:
            return "▸"
        if self.is_last:
            return "·"
        return " "

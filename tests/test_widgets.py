from rich.text import Text

from vigil.models import GitStatus, PRStatus, Session
from vigil.widgets import _colorize_pr, _git_col, _indicator, _pr_col, _session_name, _state_dot


def _make_session(**kwargs):
    defaults = {"name": "test", "pane_path": "/tmp"}
    defaults.update(kwargs)
    return Session(**defaults)


class TestIndicatorCell:
    def test_current_session(self):
        s = _make_session(is_current=True)
        result = _indicator(s)
        assert isinstance(result, Text)
        assert "▸" in result.plain

    def test_last_session(self):
        s = _make_session(is_last=True)
        assert "·" in _indicator(s).plain

    def test_plain_session(self):
        s = _make_session()
        plain = _indicator(s).plain.strip()
        assert plain == ""

    def test_bell_shows_asterisk(self):
        s = _make_session(has_bell=True)
        result = _indicator(s)
        assert "*" in result.plain
        assert "bright_yellow" in str(result.style)


class TestStateDot:
    def test_idle_dot(self):
        s = _make_session()
        result = _state_dot(s)
        assert result.plain == "·"

    def test_attention_dot(self):
        s = _make_session(has_bell=True)
        result = _state_dot(s)
        assert result.plain == "●"
        assert "bright_yellow" in str(result.style)

    def test_blocked_dot(self):
        s = _make_session(pr=PRStatus(number=1, state="OPEN", checks="fail"))
        result = _state_dot(s)
        assert result.plain == "●"
        assert "bright_red" in str(result.style)

    def test_mergeable_dot(self):
        s = _make_session(
            pr=PRStatus(number=1, state="OPEN", review_decision="APPROVED", checks="pass"),
        )
        result = _state_dot(s)
        assert result.plain == "●"
        assert "bright_green" in str(result.style)

    def test_pending_dot(self):
        s = _make_session(pr=PRStatus(number=1, state="OPEN", checks="pending"))
        result = _state_dot(s)
        assert result.plain == "○"

    def test_done_dot(self):
        s = _make_session(pr=PRStatus(number=1, state="MERGED"))
        result = _state_dot(s)
        assert result.plain == "●"
        assert "dim" in str(result.style)


class TestSessionName:
    def test_short_name(self):
        s = _make_session(name="my-feature")
        assert _session_name(s).plain == "my-feature"

    def test_long_name_truncated(self):
        long = "a" * 60
        s = _make_session(name=long)
        result = _session_name(s).plain
        assert len(result) == 51  # 50 chars + ellipsis
        assert result.endswith("…")

    def test_exact_50_not_truncated(self):
        name = "a" * 50
        s = _make_session(name=name)
        assert _session_name(s).plain == name


class TestGitCol:
    def test_clean(self):
        s = _make_session()
        assert _git_col(s).plain == "—"

    def test_dirty(self):
        s = _make_session(git=GitStatus(modified=3, unpushed=1))
        assert _git_col(s).plain == "~3 ↑1"


class TestPrCol:
    def test_no_pr(self):
        s = _make_session()
        assert _pr_col(s).plain == "—"

    def test_no_number(self):
        s = _make_session(pr=PRStatus())
        assert _pr_col(s).plain == "—"

    def test_with_pr(self):
        s = _make_session(pr=PRStatus(number=42, state="OPEN", checks="pass"))
        result = _pr_col(s)
        assert "#42" in result.plain
        assert "✓" in result.plain


class TestColorizePr:
    def test_open_pr_green_number(self):
        pr = PRStatus(number=10, state="OPEN")
        result = _colorize_pr(pr)
        assert "#10" in result.plain
        # First span should be green for open non-draft
        assert "green" in str(result._spans[0].style)

    def test_draft_pr_dim_number(self):
        pr = PRStatus(number=10, state="OPEN", is_draft=True)
        result = _colorize_pr(pr)
        assert "dim" in str(result._spans[0].style)

    def test_merged_pr_magenta(self):
        pr = PRStatus(number=10, state="MERGED")
        result = _colorize_pr(pr)
        assert "magenta" in str(result._spans[0].style)

    def test_closed_pr_red(self):
        pr = PRStatus(number=10, state="CLOSED")
        result = _colorize_pr(pr)
        assert "red" in str(result._spans[0].style)

    def test_checks_pass(self):
        pr = PRStatus(number=1, state="OPEN", checks="pass")
        result = _colorize_pr(pr)
        assert "✓" in result.plain

    def test_checks_fail(self):
        pr = PRStatus(number=1, state="OPEN", checks="fail")
        result = _colorize_pr(pr)
        assert "✗" in result.plain

    def test_checks_pending(self):
        pr = PRStatus(number=1, state="OPEN", checks="pending")
        result = _colorize_pr(pr)
        assert "●" in result.plain

    def test_approved_checkmark(self):
        pr = PRStatus(number=1, state="OPEN", review_decision="APPROVED")
        result = _colorize_pr(pr)
        assert "☑" in result.plain

    def test_changes_requested(self):
        pr = PRStatus(number=1, state="OPEN", review_decision="CHANGES_REQUESTED")
        result = _colorize_pr(pr)
        assert "✎" in result.plain

    def test_partial_approvals(self):
        pr = PRStatus(number=1, state="OPEN", approvals=1)
        result = _colorize_pr(pr)
        assert "☑" in result.plain

    def test_unresolved_comments(self):
        pr = PRStatus(number=1, state="OPEN", unresolved_comments=3)
        result = _colorize_pr(pr)
        assert "☐ 3" in result.plain

    def test_conflicts(self):
        pr = PRStatus(number=1, state="OPEN", has_conflicts=True)
        result = _colorize_pr(pr)
        assert "⚡" in result.plain

    def test_merged_no_review_indicators(self):
        """Merged PRs should not show review/conflict indicators."""
        pr = PRStatus(number=1, state="MERGED", review_decision="APPROVED", has_conflicts=True)
        result = _colorize_pr(pr)
        assert "☑" not in result.plain
        assert "⚡" not in result.plain

    def test_full_open_pr(self):
        """Open PR with all indicators."""
        pr = PRStatus(
            number=99, state="OPEN", checks="fail",
            review_decision="CHANGES_REQUESTED",
            unresolved_comments=2, has_conflicts=True,
        )
        result = _colorize_pr(pr)
        plain = result.plain
        assert "#99" in plain
        assert "✗" in plain
        assert "✎" in plain
        assert "☐ 2" in plain
        assert "⚡" in plain

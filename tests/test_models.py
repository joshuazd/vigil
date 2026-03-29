from vigil.models import GitStatus, PRStatus, Session, SessionState


class TestSessionState:
    def test_bell_is_attention(self):
        s = Session(name="test", pane_path="/tmp", has_bell=True)
        assert s.state == SessionState.ATTENTION

    def test_no_pr_is_idle(self):
        s = Session(name="test", pane_path="/tmp")
        assert s.state == SessionState.IDLE

    def test_merged_is_done(self):
        s = Session(name="test", pane_path="/tmp", pr=PRStatus(number=1, state="MERGED"))
        assert s.state == SessionState.DONE

    def test_ci_fail_is_blocked(self):
        pr = PRStatus(number=1, state="OPEN", checks="fail")
        s = Session(name="test", pane_path="/tmp", pr=pr)
        assert s.state == SessionState.BLOCKED

    def test_changes_requested_is_blocked(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", review_decision="CHANGES_REQUESTED"),
        )
        assert s.state == SessionState.BLOCKED

    def test_conflicts_is_blocked(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", has_conflicts=True),
        )
        assert s.state == SessionState.BLOCKED

    def test_unresolved_comments(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", unresolved_comments=2),
        )
        assert s.state == SessionState.UNRESOLVED

    def test_approved_and_passing_is_mergeable(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", review_decision="APPROVED", checks="pass"),
        )
        assert s.state == SessionState.MERGEABLE

    def test_approved_pending_ci(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", review_decision="APPROVED", checks="pending"),
        )
        assert s.state == SessionState.APPROVED

    def test_ci_pending_is_pending(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", checks="pending"),
        )
        assert s.state == SessionState.PENDING

    def test_open_non_draft_is_review(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", checks="pass"),
        )
        assert s.state == SessionState.REVIEW

    def test_draft_is_idle(self):
        s = Session(
            name="test", pane_path="/tmp",
            pr=PRStatus(number=1, state="OPEN", is_draft=True),
        )
        assert s.state == SessionState.IDLE

    def test_bell_takes_priority_over_pr(self):
        s = Session(
            name="test", pane_path="/tmp", has_bell=True,
            pr=PRStatus(number=1, state="OPEN", checks="fail"),
        )
        assert s.state == SessionState.ATTENTION


class TestIndicator:
    def test_current(self):
        assert Session(name="a", pane_path="/", is_current=True).indicator == "▸"

    def test_last(self):
        assert Session(name="a", pane_path="/", is_last=True).indicator == "·"

    def test_neither(self):
        assert Session(name="a", pane_path="/").indicator == " "


class TestGitStatusDisplay:
    def test_clean(self):
        assert GitStatus().display() == "—"

    def test_mixed(self):
        g = GitStatus(modified=2, added=1, deleted=3, unpushed=5)
        assert g.display() == "~2 +1 -3 ↑5"

    def test_rebase_age_hours(self):
        g = GitStatus(rebase_age_seconds=7200)
        assert g.rebase_age_display() == "↻ 2h"

    def test_rebase_age_days(self):
        g = GitStatus(rebase_age_seconds=172800)
        assert g.rebase_age_display() == "↻ 2d"

    def test_rebase_age_under_threshold(self):
        g = GitStatus(rebase_age_seconds=1800)
        assert g.rebase_age_display() == ""


class TestPRStatusDisplay:
    def test_no_number(self):
        assert PRStatus().display() == "—"

    def test_passing_approved(self):
        p = PRStatus(number=42, state="OPEN", checks="pass", review_decision="APPROVED")
        assert "#42" in p.display()
        assert "✓" in p.display()
        assert "☑" in p.display()

    def test_failing(self):
        p = PRStatus(number=10, state="OPEN", checks="fail")
        assert "✗" in p.display()

    def test_unresolved(self):
        p = PRStatus(number=10, state="OPEN", unresolved_comments=3)
        assert "☐ 3" in p.display()

import subprocess
from unittest.mock import MagicMock, patch

from vigil.tmux import (
    capture_pane,
    get_bell_flags,
    get_current_session,
    get_last_session,
    list_sessions,
)


class TestListSessions:
    @patch("vigil.tmux.subprocess.run")
    def test_parses_multi_line(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="100|alpha|/home/alpha\n200|beta|/home/beta\n",
        )
        result = list_sessions()
        assert len(result) == 2
        assert result[0]["name"] == "alpha"
        assert result[0]["created"] == 100
        assert result[1]["name"] == "beta"

    @patch("vigil.tmux.subprocess.run")
    def test_deduplicates_by_session(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="100|alpha|/a\n100|alpha|/a2\n200|beta|/b\n200|beta|/b2\n",
        )
        result = list_sessions()
        names = [s["name"] for s in result]
        assert names == ["alpha", "beta"]
        assert result[0]["pane_path"] == "/a"

    @patch("vigil.tmux.subprocess.run")
    def test_no_filter_flag_in_command(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="100|sess|/tmp\n")
        list_sessions()
        cmd = mock_run.call_args.args[0]
        assert "-f" not in cmd

    @patch("vigil.tmux.subprocess.run")
    def test_sorted_by_created(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="300|c|/c\n100|a|/a\n200|b|/b\n",
        )
        result = list_sessions()
        assert [s["name"] for s in result] == ["a", "b", "c"]

    @patch("vigil.tmux.subprocess.run")
    def test_malformed_lines_skipped(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="100|alpha|/home/alpha\nbadline\n\n200|beta\n",
        )
        result = list_sessions()
        assert len(result) == 1
        assert result[0]["name"] == "alpha"

    @patch("vigil.tmux.subprocess.run")
    def test_empty_output(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="\n")
        assert list_sessions() == []

    @patch("vigil.tmux.subprocess.run")
    def test_failure_raises(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=1, stdout="", stderr="error", args=["tmux"],
        )
        try:
            list_sessions()
            assert False, "should have raised"
        except subprocess.CalledProcessError:
            pass


class TestGetBellFlags:
    @patch("vigil.tmux.subprocess.run")
    def test_flags_present(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="alpha|1\nalpha|0\nbeta|1\n",
        )
        result = get_bell_flags()
        assert result == {"alpha": True, "beta": True}

    @patch("vigil.tmux.subprocess.run")
    def test_empty_output(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="\n")
        assert get_bell_flags() == {}

    @patch("vigil.tmux.subprocess.run")
    def test_failure_returns_empty(self, mock_run):
        mock_run.side_effect = subprocess.CalledProcessError(1, ["tmux"])
        assert get_bell_flags() == {}


class TestGetCurrentSession:
    @patch("vigil.tmux.subprocess.run")
    def test_happy_path(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="my-session\n")
        assert get_current_session() == "my-session"

    @patch("vigil.tmux.subprocess.run")
    def test_failure_returns_empty(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=1, stdout="", stderr="error", args=["tmux"],
        )
        assert get_current_session() == ""


class TestGetLastSession:
    @patch("vigil.tmux.subprocess.run")
    def test_returns_most_recent_other(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="300|current\n200|other\n100|oldest\n",
        )
        assert get_last_session("current") == "other"

    @patch("vigil.tmux.subprocess.run")
    def test_failure_returns_empty(self, mock_run):
        mock_run.side_effect = subprocess.CalledProcessError(1, ["tmux"])
        assert get_last_session("current") == ""


class TestCapturePaene:
    @patch("vigil.tmux.subprocess.run")
    def test_captures_output(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="line1\nline2\n")
        assert capture_pane("session") == "line1\nline2"

    @patch("vigil.tmux.subprocess.run")
    def test_failure_returns_empty(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=1, stdout="", stderr="error", args=["tmux"],
        )
        assert capture_pane("session") == ""

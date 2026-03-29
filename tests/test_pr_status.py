from unittest.mock import MagicMock, patch

from vigil.pr_status import _get_nwo, _parse_checks


class TestParseChecks:
    def test_empty(self):
        assert _parse_checks([]) == ""

    def test_all_passing(self):
        rollup = [{"conclusion": "SUCCESS"}, {"conclusion": "SUCCESS"}]
        assert _parse_checks(rollup) == "pass"

    def test_failure(self):
        rollup = [{"conclusion": "SUCCESS"}, {"conclusion": "FAILURE"}]
        assert _parse_checks(rollup) == "fail"

    def test_error(self):
        rollup = [{"conclusion": "ERROR"}]
        assert _parse_checks(rollup) == "fail"

    def test_pending(self):
        rollup = [{"conclusion": "SUCCESS"}, {"status": "IN_PROGRESS"}]
        assert _parse_checks(rollup) == "pending"

    def test_queued(self):
        rollup = [{"status": "QUEUED"}]
        assert _parse_checks(rollup) == "pending"

    def test_waiting(self):
        rollup = [{"state": "WAITING"}]
        assert _parse_checks(rollup) == "pending"

    def test_no_statuses_in_items(self):
        rollup = [{"other": "field"}]
        assert _parse_checks(rollup) == ""

    def test_failure_beats_pending(self):
        rollup = [{"conclusion": "FAILURE"}, {"status": "PENDING"}]
        assert _parse_checks(rollup) == "fail"


class TestGetNwo:
    @patch("vigil.pr_status.subprocess.run")
    def test_ssh_url(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0, stdout="git@github.com:octocat/hello-world.git\n",
        )
        assert _get_nwo("/tmp") == ("octocat", "hello-world")

    @patch("vigil.pr_status.subprocess.run")
    def test_https_url(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0, stdout="https://github.com/octocat/hello-world.git\n",
        )
        assert _get_nwo("/tmp") == ("octocat", "hello-world")

    @patch("vigil.pr_status.subprocess.run")
    def test_no_remote(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1, stdout="")
        assert _get_nwo("/tmp") is None

    @patch("vigil.pr_status.subprocess.run")
    def test_non_github(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0, stdout="https://gitlab.com/octocat/hello-world.git\n",
        )
        assert _get_nwo("/tmp") is None

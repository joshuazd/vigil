import json
import subprocess
from unittest.mock import MagicMock, patch

from vigil.pr_status import _fetch_unresolved_threads, _get_nwo, _parse_checks, fetch


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


class TestFetch:
    """End-to-end tests for fetch() with mocked subprocess."""

    _VALID_GH_JSON = json.dumps({
        "number": 42,
        "state": "OPEN",
        "isDraft": False,
        "url": "https://github.com/octocat/hello-world/pull/42",
        "statusCheckRollup": [{"conclusion": "SUCCESS"}],
        "reviewDecision": "APPROVED",
        "latestReviews": [{"state": "APPROVED"}, {"state": "COMMENTED"}],
        "mergeable": "MERGEABLE",
    })

    @patch("vigil.pr_status._fetch_unresolved_threads", return_value=1)
    @patch("vigil.pr_status._run_with_retry")
    def test_valid_json_returns_pr_status(self, mock_retry, mock_threads):
        mock_retry.return_value = MagicMock(returncode=0, stdout=self._VALID_GH_JSON)
        result = fetch("feat", "/repo")
        assert result is not None
        assert result.number == 42
        assert result.state == "OPEN"
        assert result.is_draft is False
        assert result.url == "https://github.com/octocat/hello-world/pull/42"
        assert result.checks == "pass"
        assert result.review_decision == "APPROVED"
        assert result.approvals == 1
        assert result.unresolved_comments == 1
        assert result.has_conflicts is False

    @patch("vigil.pr_status._run_with_retry")
    def test_non_zero_returns_none(self, mock_retry):
        mock_retry.return_value = MagicMock(returncode=1, stderr="no PR found")
        assert fetch("feat", "/repo") is None

    @patch("vigil.pr_status._run_with_retry")
    def test_malformed_json_returns_none(self, mock_retry):
        mock_retry.return_value = MagicMock(returncode=0, stdout="not json{{{")
        assert fetch("feat", "/repo") is None

    @patch("vigil.pr_status._run_with_retry", side_effect=subprocess.TimeoutExpired("gh", 15))
    def test_timeout_returns_none(self, mock_retry):
        assert fetch("feat", "/repo") is None


class TestFetchUnresolvedThreads:
    """Tests for _fetch_unresolved_threads with mocked GraphQL."""

    _GRAPHQL_RESPONSE = json.dumps({
        "data": {
            "repository": {
                "pullRequest": {
                    "reviewThreads": {
                        "nodes": [
                            {"isResolved": False, "isOutdated": False},
                            {"isResolved": True, "isOutdated": False},
                            {"isResolved": False, "isOutdated": True},
                            {"isResolved": False, "isOutdated": False},
                        ],
                    },
                },
            },
        },
    })

    @patch("vigil.pr_status._get_nwo", return_value=("octocat", "hello-world"))
    @patch("vigil.pr_status._run_with_retry")
    def test_counts_unresolved_non_outdated(self, mock_retry, mock_nwo):
        mock_retry.return_value = MagicMock(returncode=0, stdout=self._GRAPHQL_RESPONSE)
        assert _fetch_unresolved_threads("/repo", 42) == 2

    @patch("vigil.pr_status._get_nwo", return_value=None)
    def test_no_nwo_returns_zero(self, mock_nwo):
        assert _fetch_unresolved_threads("/repo", 42) == 0

    @patch("vigil.pr_status._get_nwo", return_value=("octocat", "hello-world"))
    @patch("vigil.pr_status._run_with_retry")
    def test_graphql_failure_returns_zero(self, mock_retry, mock_nwo):
        mock_retry.return_value = MagicMock(returncode=1, stderr="error")
        assert _fetch_unresolved_threads("/repo", 42) == 0

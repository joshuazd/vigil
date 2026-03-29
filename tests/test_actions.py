import subprocess
from unittest.mock import MagicMock, patch

import pytest

from vigil.actions import (
    approve_pr,
    cleanup_session,
    dispatch,
    merge_pr,
    open_pr_in_browser,
    rebase_and_push,
)


class TestMergePR:
    @patch("vigil.actions.subprocess.run")
    def test_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="merged\n")
        assert merge_pr("/repo", "feat") == "merged"

    @patch("vigil.actions.subprocess.run")
    def test_failure_raises(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=1, stdout="", stderr="error", args=["gh"],
        )
        with pytest.raises(subprocess.CalledProcessError):
            merge_pr("/repo", "feat")


class TestApprovePR:
    @patch("vigil.actions.subprocess.run")
    def test_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="approved\n")
        assert approve_pr("/repo", "feat") == "approved"


class TestRebaseAndPush:
    def _mock_run_side_effects(self, main_exists=True, fetch_ok=True, merge_ok=True,
                                rebase_ok=True, push_ok=True):
        """Build a side_effect list for sequential subprocess.run calls."""
        results = []
        # rev-parse --verify main
        results.append(MagicMock(returncode=0 if main_exists else 1))
        if not main_exists:
            # rev-parse --verify master
            results.append(MagicMock(returncode=1))
            return results
        # fetch
        results.append(MagicMock(returncode=0 if fetch_ok else 1, stderr="fetch err"))
        if not fetch_ok:
            return results
        # merge-tree
        results.append(MagicMock(returncode=0 if merge_ok else 1))
        if not merge_ok:
            return results
        # rebase
        results.append(MagicMock(returncode=0 if rebase_ok else 1, stderr="rebase err"))
        if not rebase_ok:
            # rebase --abort
            results.append(MagicMock(returncode=0))
            return results
        # push
        results.append(MagicMock(returncode=0 if push_ok else 1, stderr="push err"))
        return results

    @patch("vigil.actions.subprocess.run")
    def test_success(self, mock_run):
        mock_run.side_effect = self._mock_run_side_effects()
        assert rebase_and_push("/repo") == "rebased and pushed"

    @patch("vigil.actions.subprocess.run")
    def test_no_main_branch(self, mock_run):
        mock_run.side_effect = self._mock_run_side_effects(main_exists=False)
        with pytest.raises(RuntimeError, match="No main/master"):
            rebase_and_push("/repo")

    @patch("vigil.actions.subprocess.run")
    def test_conflicts_detected(self, mock_run):
        mock_run.side_effect = self._mock_run_side_effects(merge_ok=False)
        with pytest.raises(RuntimeError, match="conflicts"):
            rebase_and_push("/repo")

    @patch("vigil.actions.subprocess.run")
    def test_push_failure(self, mock_run):
        mock_run.side_effect = self._mock_run_side_effects(push_ok=False)
        with pytest.raises(RuntimeError, match="push failed"):
            rebase_and_push("/repo")


class TestCleanupSession:
    @patch("vigil.actions.shutil.which", return_value=None)
    def test_missing_script(self, mock_which):
        with pytest.raises(FileNotFoundError, match="git-worktree-cleanup"):
            cleanup_session("test-session", "/tmp/wt")


class TestDispatch:
    @patch("vigil.actions.shutil.which", return_value=None)
    def test_missing_script(self, mock_which):
        with pytest.raises(FileNotFoundError, match="dispatch"):
            dispatch("https://example.com")


class TestOpenPR:
    @patch("vigil.actions.webbrowser.open")
    def test_opens_url(self, mock_open):
        open_pr_in_browser("https://github.com/pr/1")
        mock_open.assert_called_once_with("https://github.com/pr/1")

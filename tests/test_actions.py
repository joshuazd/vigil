import subprocess
from unittest.mock import MagicMock, patch

import pytest

import vigil.actions
from vigil.actions import (
    approve_pr,
    cleanup_session,
    dispatch,
    merge_pr,
    open_pr_in_browser,
    rebase_and_push,
)
from vigil.config import HookNotConfigured


@pytest.fixture(autouse=True)
def _reset_config():
    """Reset cached config between tests."""
    vigil.actions.config._config = None
    yield
    vigil.actions.config._config = None


class TestMergePR:
    @patch("vigil.actions.config.run_hook", return_value="merged")
    def test_success(self, mock_hook):
        assert merge_pr("/repo", "feat") == "merged"
        mock_hook.assert_called_once_with(
            "merge", {"branch": "feat", "git_root": "/repo"}, cwd="/repo",
        )

    @patch("vigil.actions.config.run_hook", side_effect=HookNotConfigured("merge"))
    def test_hook_not_configured(self, mock_hook):
        with pytest.raises(HookNotConfigured):
            merge_pr("/repo", "feat")

    @patch(
        "vigil.actions.config.run_hook",
        side_effect=subprocess.CalledProcessError(1, "cmd", "", "error"),
    )
    def test_failure_raises(self, mock_hook):
        with pytest.raises(subprocess.CalledProcessError):
            merge_pr("/repo", "feat")


class TestApprovePR:
    @patch("vigil.actions.config.run_hook", return_value="approved")
    def test_success(self, mock_hook):
        assert approve_pr("/repo", "feat") == "approved"
        mock_hook.assert_called_once_with(
            "approve", {"branch": "feat", "git_root": "/repo"}, cwd="/repo",
        )

    @patch(
        "vigil.actions.config.run_hook",
        side_effect=subprocess.CalledProcessError(1, "cmd", "", "error"),
    )
    def test_failure_raises(self, mock_hook):
        with pytest.raises(subprocess.CalledProcessError):
            approve_pr("/repo", "feat")


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

    @patch("vigil.actions.subprocess.run")
    def test_fetch_failure(self, mock_run):
        mock_run.side_effect = self._mock_run_side_effects(fetch_ok=False)
        with pytest.raises(RuntimeError, match="fetch failed"):
            rebase_and_push("/repo")

    @patch("vigil.actions.subprocess.run")
    def test_rebase_failure_triggers_abort(self, mock_run):
        effects = self._mock_run_side_effects(rebase_ok=False)
        mock_run.side_effect = effects
        with pytest.raises(RuntimeError, match="rebase failed"):
            rebase_and_push("/repo")
        # Should have called rebase --abort (5th call)
        assert mock_run.call_count == 5
        abort_call = mock_run.call_args_list[4]
        assert "rebase" in abort_call.args[0] and "--abort" in abort_call.args[0]


class TestCleanupSession:
    @patch("vigil.actions.config.run_hook", return_value="hook cleaned up")
    @patch("vigil.actions.config.get_hook", return_value="my-cleanup {session} {path}")
    def test_hook_based_cleanup(self, mock_get, mock_run):
        result = cleanup_session("test-session", "/tmp/wt", branch="feat", git_root="/repo")
        assert result == "hook cleaned up"
        mock_get.assert_called_once_with("cleanup")
        mock_run.assert_called_once_with(
            "cleanup",
            {"session": "test-session", "path": "/tmp/wt", "branch": "feat", "git_root": "/repo"},
        )

    @patch("vigil.actions._is_worktree", return_value=True)
    @patch("vigil.actions.subprocess.run")
    @patch("vigil.actions.config.get_hook", return_value=None)
    def test_builtin_cleanup(self, mock_get, mock_run, mock_is_wt):
        mock_run.side_effect = [
            # tmux kill-session
            MagicMock(returncode=0),
            # git status --porcelain (clean)
            MagicMock(returncode=0, stdout=""),
            # git worktree remove
            MagicMock(returncode=0),
        ]
        result = cleanup_session("test-session", "/tmp/wt", branch="feat", git_root="/repo")
        assert "killed session" in result
        assert "removed worktree" in result

    @patch("vigil.actions._is_worktree", return_value=False)
    @patch("vigil.actions.subprocess.run")
    @patch("vigil.actions.config.get_hook", return_value=None)
    def test_builtin_cleanup_not_a_worktree(self, mock_get, mock_run, mock_is_wt):
        mock_run.return_value = MagicMock(returncode=0)  # tmux kill-session
        result = cleanup_session("test-session", "/tmp/wt", branch="feat", git_root="/repo")
        assert "killed session" in result
        # Should NOT attempt worktree removal
        assert mock_run.call_count == 1

    @patch("vigil.actions._is_worktree", return_value=True)
    @patch("vigil.actions.subprocess.run")
    @patch("vigil.actions.config.get_hook", return_value=None)
    def test_builtin_cleanup_worktree_remove_fails(self, mock_get, mock_run, mock_is_wt):
        mock_run.side_effect = [
            MagicMock(returncode=0),  # tmux kill-session
            MagicMock(returncode=0, stdout=""),  # git status --porcelain
            MagicMock(returncode=1, stderr="error removing"),  # git worktree remove
        ]
        with pytest.raises(RuntimeError, match="worktree remove failed"):
            cleanup_session("test-session", "/tmp/wt", branch="feat", git_root="/repo")

    @patch("vigil.actions._is_worktree", return_value=True)
    @patch("vigil.actions.subprocess.run")
    @patch("vigil.actions.config.get_hook", return_value=None)
    def test_builtin_cleanup_dirty_worktree(self, mock_get, mock_run, mock_is_wt):
        mock_run.side_effect = [
            MagicMock(returncode=0),  # tmux kill-session
            MagicMock(returncode=0, stdout=" M file.txt\n"),  # dirty
            MagicMock(returncode=0),  # git worktree remove
        ]
        result = cleanup_session("test-session", "/tmp/wt", branch="feat", git_root="/repo")
        assert "uncommitted changes" in result


class TestDispatch:
    def test_empty_input_rejected(self):
        with pytest.raises(ValueError, match="empty"):
            dispatch("")

    def test_whitespace_only_rejected(self):
        with pytest.raises(ValueError, match="empty"):
            dispatch("   ")

    def test_too_long_rejected(self):
        with pytest.raises(ValueError, match="too long"):
            dispatch("x" * 501)

    def test_control_chars_rejected(self):
        with pytest.raises(ValueError, match="control"):
            dispatch("hello\x00world")

    @patch("vigil.actions.config.run_hook", return_value="ok")
    def test_success(self, mock_hook):
        assert dispatch("https://example.com") == "ok"
        mock_hook.assert_called_once_with(
            "dispatch", {"input": "https://example.com"}, timeout=15,
        )

    @patch("vigil.actions.config.run_hook", side_effect=HookNotConfigured("dispatch"))
    def test_hook_not_configured(self, mock_hook):
        with pytest.raises(HookNotConfigured):
            dispatch("https://example.com")

    @patch(
        "vigil.actions.config.run_hook",
        side_effect=subprocess.CalledProcessError(1, "cmd", "", "error"),
    )
    def test_failure_raises(self, mock_hook):
        with pytest.raises(subprocess.CalledProcessError):
            dispatch("https://example.com")


class TestTimeoutPropagation:
    @patch("vigil.actions.config.run_hook", side_effect=subprocess.TimeoutExpired("sh", 30))
    def test_merge_pr_timeout(self, mock_hook):
        with pytest.raises(subprocess.TimeoutExpired):
            merge_pr("/repo", "feat")

    @patch("vigil.actions.config.run_hook", side_effect=subprocess.TimeoutExpired("sh", 30))
    def test_approve_pr_timeout(self, mock_hook):
        with pytest.raises(subprocess.TimeoutExpired):
            approve_pr("/repo", "feat")

    @patch("vigil.actions.subprocess.run", side_effect=subprocess.TimeoutExpired("git", 10))
    def test_rebase_and_push_timeout(self, mock_run):
        with pytest.raises(subprocess.TimeoutExpired):
            rebase_and_push("/repo")


class TestTimeoutValues:
    @patch("vigil.actions.config.run_hook", return_value="ok")
    def test_merge_pr_delegates_to_hook(self, mock_hook):
        merge_pr("/repo", "feat")
        mock_hook.assert_called_once()

    @patch("vigil.actions.config.run_hook", return_value="ok")
    def test_approve_pr_delegates_to_hook(self, mock_hook):
        approve_pr("/repo", "feat")
        mock_hook.assert_called_once()


class TestOpenPR:
    @patch("vigil.actions.webbrowser.open")
    def test_opens_url(self, mock_open):
        open_pr_in_browser("https://github.com/pr/1")
        mock_open.assert_called_once_with("https://github.com/pr/1")

from unittest.mock import MagicMock, patch

from vigil.git_status import (
    _parse_porcelain,
    _rebase_age,
    _unpushed_count,
    detect_default_branch,
    fetch,
)


class TestParsePortcelain:
    @patch("vigil.git_status.subprocess.run")
    def test_empty_output(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="")
        assert _parse_porcelain("/repo") == (0, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_modified_files(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="M  file1\n M file2\nMM file3\n")
        assert _parse_porcelain("/repo") == (3, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_added_files(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="A  file1\n?? file2\n")
        assert _parse_porcelain("/repo") == (0, 2, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_deleted_files(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="D  file1\n D file2\n")
        assert _parse_porcelain("/repo") == (0, 0, 2)

    @patch("vigil.git_status.subprocess.run")
    def test_renamed_counts_as_modified(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="R  old -> new\n")
        assert _parse_porcelain("/repo") == (1, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_copied_counts_as_added(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="C  src -> dst\n")
        assert _parse_porcelain("/repo") == (0, 1, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_mixed_statuses(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0, stdout="M  a\n?? b\nD  c\nA  d\n M e\n",
        )
        assert _parse_porcelain("/repo") == (2, 2, 1)

    @patch("vigil.git_status.subprocess.run")
    def test_short_lines_skipped(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="X\n\nM  file\n")
        assert _parse_porcelain("/repo") == (1, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_nonzero_returncode(self, mock_run):
        mock_run.return_value = MagicMock(returncode=128, stdout="")
        assert _parse_porcelain("/repo") == (0, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_file_not_found(self, mock_run):
        mock_run.side_effect = FileNotFoundError
        assert _parse_porcelain("/repo") == (0, 0, 0)

    @patch("vigil.git_status.subprocess.run")
    def test_unmerged_counts_as_modified(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="U  conflict\n")
        assert _parse_porcelain("/repo") == (1, 0, 0)


class TestUnpushedCount:
    @patch("vigil.git_status.subprocess.run")
    def test_ahead_by_three(self, mock_run):
        mock_run.side_effect = [
            MagicMock(returncode=0),  # rev-parse origin/feat
            MagicMock(returncode=0, stdout="3\n"),  # rev-list --count
        ]
        assert _unpushed_count("/repo", "feat") == 3

    @patch("vigil.git_status.subprocess.run")
    def test_no_remote(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1)
        assert _unpushed_count("/repo", "feat") == 0

    @patch("vigil.git_status.subprocess.run")
    def test_value_error_returns_zero(self, mock_run):
        mock_run.side_effect = [
            MagicMock(returncode=0),
            MagicMock(returncode=0, stdout="not-a-number\n"),
        ]
        assert _unpushed_count("/repo", "feat") == 0

    @patch("vigil.git_status.subprocess.run")
    def test_file_not_found(self, mock_run):
        mock_run.side_effect = FileNotFoundError
        assert _unpushed_count("/repo", "feat") == 0


class TestDetectDefaultBranch:
    @patch("vigil.git_status.subprocess.run")
    def test_from_remote_head(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0, stdout="refs/remotes/origin/main\n",
        )
        assert detect_default_branch("/repo") == "main"

    @patch("vigil.git_status.subprocess.run")
    def test_fallback_to_master(self, mock_run):
        mock_run.side_effect = [
            MagicMock(returncode=1),  # symbolic-ref fails
            MagicMock(returncode=1),  # rev-parse main fails
            MagicMock(returncode=0),  # rev-parse master succeeds
        ]
        assert detect_default_branch("/repo") == "master"

    @patch("vigil.git_status.subprocess.run")
    def test_no_default_branch(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1)
        assert detect_default_branch("/repo") is None


class TestRebaseAge:
    @patch("vigil.git_status.detect_default_branch", return_value="main")
    def test_default_branch_returns_none(self, mock_detect):
        assert _rebase_age("/repo", "main") is None

    @patch("vigil.git_status.detect_default_branch", return_value=None)
    def test_no_default_branch(self, mock_detect):
        assert _rebase_age("/repo", "feat") is None

    @patch("vigil.git_status.time.time", return_value=1000.0)
    @patch("vigil.git_status.subprocess.run")
    @patch("vigil.git_status.detect_default_branch", return_value="main")
    def test_happy_path(self, mock_detect, mock_run, mock_time):
        mock_run.side_effect = [
            MagicMock(returncode=0, stdout="abc123\n"),  # merge-base
            MagicMock(returncode=0, stdout="500\n"),  # log --format=%ct
        ]
        assert _rebase_age("/repo", "feat") == 500

    @patch("vigil.git_status.subprocess.run")
    @patch("vigil.git_status.detect_default_branch", return_value="main")
    def test_merge_base_failure(self, mock_detect, mock_run):
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="")
        assert _rebase_age("/repo", "feat") is None


class TestFetch:
    @patch("vigil.git_status._rebase_age", return_value=None)
    @patch("vigil.git_status._unpushed_count", return_value=2)
    @patch("vigil.git_status._parse_porcelain", return_value=(1, 0, 0))
    @patch("vigil.git_status._current_branch", return_value="feat")
    @patch("vigil.git_status._git_root", return_value="/repo")
    def test_full_fetch(self, mock_root, mock_branch, mock_porcelain, mock_unpushed, mock_rebase):
        result = fetch("/some/path")
        assert result.branch == "feat"
        assert result.git_root == "/repo"
        assert result.modified == 1
        assert result.unpushed == 2

    @patch("vigil.git_status._git_root", return_value="")
    def test_no_git_root(self, mock_root):
        result = fetch("/some/path")
        assert result.branch == ""
        assert result.git_root == ""

    @patch("vigil.git_status._current_branch", return_value="")
    @patch("vigil.git_status._git_root", return_value="/repo")
    def test_no_branch(self, mock_root, mock_branch):
        result = fetch("/some/path")
        assert result.git_root == "/repo"
        assert result.branch == ""

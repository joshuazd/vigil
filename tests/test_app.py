import asyncio
from unittest.mock import patch

import pytest

import vigil.actions
from vigil.app import VigilApp, _check_dependencies
from vigil.models import GitStatus, Session


@pytest.fixture(autouse=True)
def _reset_config():
    """Reset cached config between tests."""
    vigil.actions.config._config = None
    yield
    vigil.actions.config._config = None


class TestCheckDependencies:
    @patch("shutil.which", side_effect=lambda cmd: None if cmd == "gh" else f"/usr/bin/{cmd}")
    def test_missing_gh(self, mock_which):
        try:
            _check_dependencies()
            assert False, "Should have raised SystemExit"
        except SystemExit as e:
            assert "gh" in str(e)

    @patch("shutil.which", return_value="/usr/bin/fake")
    def test_all_present(self, mock_which):
        _check_dependencies()  # should not raise


class TestCheckDependenciesAllMissing:
    @patch("shutil.which", return_value=None)
    def test_all_missing(self, mock_which):
        try:
            _check_dependencies()
            assert False, "Should have raised SystemExit"
        except SystemExit as e:
            assert "tmux" in str(e)
            assert "git" in str(e)
            assert "gh" in str(e)


class TestAppSmoke:
    def test_app_composes(self):
        async def _test():
            app = VigilApp()
            async with app.run_test():
                assert app.query_one("#session-table") is not None
                assert app.query_one("#status-bar") is not None
                assert app.query_one("#detail-panel") is not None

        asyncio.run(_test())


class TestRebasePushMainRejection:
    @patch("vigil.app.detect_default_branch", return_value="main")
    def test_rebase_main_branch_warns(self, mock_detect):
        """action_rebase_push should notify warning when branch is main."""
        async def _test():
            app = VigilApp()
            async with app.run_test(notifications=True) as pilot:
                session = Session(
                    name="test",
                    pane_path="/tmp",
                    git=GitStatus(branch="main", git_root="/repo"),
                )
                app.sessions = [session]
                table = app.query_one("#session-table")
                table.update_sessions([session])
                await pilot.press("b")
                assert any("Can't rebase main" in n.message for n in app._notifications)

        asyncio.run(_test())

    @patch("vigil.app.detect_default_branch", return_value="master")
    def test_rebase_master_branch_warns(self, mock_detect):
        """action_rebase_push should notify warning when branch is master."""
        async def _test():
            app = VigilApp()
            async with app.run_test(notifications=True) as pilot:
                session = Session(
                    name="test",
                    pane_path="/tmp",
                    git=GitStatus(branch="master", git_root="/repo"),
                )
                app.sessions = [session]
                table = app.query_one("#session-table")
                table.update_sessions([session])
                await pilot.press("b")
                assert any("Can't rebase master" in n.message for n in app._notifications)

        asyncio.run(_test())

import asyncio
from unittest.mock import patch

from vigil.app import VigilApp, _check_dependencies


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


class TestAppSmoke:
    def test_app_composes(self):
        async def _test():
            app = VigilApp()
            async with app.run_test():
                assert app.query_one("#session-table") is not None
                assert app.query_one("#status-bar") is not None
                assert app.query_one("#detail-panel") is not None

        asyncio.run(_test())

import json
import time
from unittest.mock import patch

from vigil.cache import load, save
from vigil.models import GitStatus, PRStatus, Session


def _make_session(**kwargs):
    defaults = dict(name="test", pane_path="/tmp", created=1000)
    defaults.update(kwargs)
    return Session(**defaults)


class TestRoundTrip:
    def test_basic_session(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            sessions = [_make_session()]
            save(sessions)
            loaded = load()
            assert loaded is not None
            assert len(loaded) == 1
            assert loaded[0].name == "test"

    def test_session_with_pr(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            pr = PRStatus(number=42, state="OPEN", checks="pass", url="https://example.com")
            sessions = [_make_session(git=GitStatus(branch="feat"), pr=pr)]
            save(sessions)
            loaded = load()
            assert loaded[0].pr.number == 42
            assert loaded[0].pr.checks == "pass"

    def test_session_with_git(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            git = GitStatus(branch="feat", modified=3, unpushed=1)
            sessions = [_make_session(git=git)]
            save(sessions)
            loaded = load()
            assert loaded[0].git.branch == "feat"
            assert loaded[0].git.modified == 3


class TestStaleCache:
    def test_stale_returns_none(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            data = {"timestamp": int(time.time()) - 60, "sessions": []}
            cache_path.write_text(json.dumps(data))
            assert load() is None

    def test_missing_returns_none(self, tmp_path):
        cache_path = tmp_path / "nonexistent.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            assert load() is None

    def test_malformed_returns_none(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        cache_path.write_text("not json")
        with patch("vigil.cache.CACHE_PATH", cache_path):
            assert load() is None


class TestCachePermissions:
    def test_file_permissions_are_0600(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        with patch("vigil.cache.CACHE_PATH", cache_path):
            save([_make_session()])
            mode = cache_path.stat().st_mode & 0o777
            assert mode == 0o600


class TestCacheVersionCheck:
    def test_wrong_version_returns_none(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        data = {"version": 999, "timestamp": int(time.time()), "sessions": []}
        cache_path.write_text(json.dumps(data))
        with patch("vigil.cache.CACHE_PATH", cache_path):
            assert load() is None

    def test_no_version_returns_none(self, tmp_path):
        cache_path = tmp_path / "cache.json"
        data = {"timestamp": int(time.time()), "sessions": []}
        cache_path.write_text(json.dumps(data))
        with patch("vigil.cache.CACHE_PATH", cache_path):
            assert load() is None

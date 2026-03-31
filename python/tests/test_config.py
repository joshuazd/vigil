import subprocess
from unittest.mock import MagicMock, patch

import pytest

import vigil.config as config


class TestLoadConfig:
    def setup_method(self):
        config._config = None

    def test_missing_file_returns_empty_dict(self, tmp_path):
        missing = tmp_path / "nonexistent.toml"
        with patch.object(config, "CONFIG_PATH", missing):
            result = config.load_config()
            assert result == {}

    def test_valid_toml_parsed(self, tmp_path):
        cfg = tmp_path / "config.toml"
        cfg.write_text('[hooks]\nmerge = "custom merge"\n')
        with patch.object(config, "CONFIG_PATH", cfg):
            result = config.load_config()
            assert result == {"hooks": {"merge": "custom merge"}}

    def test_invalid_toml_returns_empty_dict(self, tmp_path):
        cfg = tmp_path / "config.toml"
        cfg.write_text("not valid [[[ toml")
        with patch.object(config, "CONFIG_PATH", cfg):
            result = config.load_config()
            assert result == {}

    def test_config_is_cached(self, tmp_path):
        cfg = tmp_path / "config.toml"
        cfg.write_text('[hooks]\nmerge = "one"\n')
        with patch.object(config, "CONFIG_PATH", cfg):
            first = config.load_config()
            cfg.write_text('[hooks]\nmerge = "two"\n')
            second = config.load_config()
            assert first is second
            assert first["hooks"]["merge"] == "one"


class TestGetHook:
    def setup_method(self):
        config._config = None

    def test_returns_configured_hook(self):
        config._config = {"hooks": {"merge": "my-merge {branch}"}}
        assert config.get_hook("merge") == "my-merge {branch}"

    def test_returns_builtin_default_for_merge(self):
        config._config = {}
        assert config.get_hook("merge") == "gh pr merge {branch} --squash --delete-branch"

    def test_returns_builtin_default_for_approve(self):
        config._config = {}
        assert config.get_hook("approve") == "gh pr review {branch} --approve"

    def test_returns_none_for_unconfigured_no_default(self):
        config._config = {}
        assert config.get_hook("cleanup") is None
        assert config.get_hook("dispatch") is None

    def test_empty_string_returns_none(self):
        config._config = {"hooks": {"merge": ""}}
        assert config.get_hook("merge") is None


class TestExpandHook:
    def test_basic_substitution(self):
        result = config.expand_hook("echo {branch}", {"branch": "feat"})
        assert result == "echo feat"

    def test_values_are_shell_escaped_spaces(self):
        result = config.expand_hook("echo {msg}", {"msg": "hello world"})
        assert result == "echo 'hello world'"

    def test_values_are_shell_escaped_quotes(self):
        result = config.expand_hook("echo {msg}", {"msg": "it's done"})
        assert "it" in result
        assert "done" in result

    def test_values_are_shell_escaped_semicolons(self):
        result = config.expand_hook("echo {msg}", {"msg": "a; rm -rf /"})
        # semicolon must be quoted/escaped, not bare
        assert ";" not in result or "'" in result

    def test_unknown_placeholder_raises(self):
        with pytest.raises(ValueError, match="Unknown placeholder"):
            config.expand_hook("echo {missing}", {"branch": "feat"})


class TestRunHook:
    def setup_method(self):
        config._config = None

    def test_successful_command_returns_stdout(self):
        config._config = {"hooks": {"greet": "echo {name}"}}
        mock_result = MagicMock(returncode=0, stdout="hello\n", stderr="")
        with patch("vigil.config.subprocess.run", return_value=mock_result) as mock_run:
            out = config.run_hook("greet", {"name": "world"})
            assert out == "hello"
            mock_run.assert_called_once()

    def test_failed_command_raises(self):
        config._config = {"hooks": {"bad": "exit 1"}}
        mock_result = MagicMock(returncode=1, stdout="", stderr="fail")
        with patch("vigil.config.subprocess.run", return_value=mock_result):
            with pytest.raises(subprocess.CalledProcessError):
                config.run_hook("bad", {})

    def test_hook_not_configured_raises(self):
        config._config = {}
        with pytest.raises(config.HookNotConfigured):
            config.run_hook("cleanup", {"branch": "feat"})


class TestGetSetting:
    def setup_method(self):
        config._config = None

    def test_returns_default(self):
        config._config = {}
        assert config.get_setting("git_interval") == "3"

    def test_returns_toml_value(self):
        config._config = {"settings": {"git_interval": 5}}
        assert config.get_setting("git_interval") == "5"

    def test_env_var_overrides_toml(self):
        config._config = {"settings": {"git_interval": 5}}
        with patch.dict("os.environ", {"VIGIL_GIT_INTERVAL": "10"}):
            assert config.get_setting("git_interval") == "10"

    def test_env_var_overrides_default(self):
        config._config = {}
        with patch.dict("os.environ", {"VIGIL_LOG_LEVEL": "DEBUG"}):
            assert config.get_setting("log_level") == "DEBUG"

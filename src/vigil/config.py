from __future__ import annotations

import logging
import os
import shlex
import subprocess
import sys
from pathlib import Path

if sys.version_info >= (3, 11):
    import tomllib
else:
    import tomli as tomllib

logger = logging.getLogger(__name__)

CONFIG_PATH = Path.home() / ".config" / "vigil" / "config.toml"

HOOK_DEFAULTS: dict[str, str] = {
    "merge": "gh pr merge {branch} --squash --delete-branch",
    "approve": "gh pr review {branch} --approve",
    "notify": 'tmux display-message "vigil: {session} → {new_state}"',
}

# Settings with their TOML key, env var override, and default value.
SETTING_DEFAULTS: dict[str, tuple[str, str]] = {
    "git_interval": ("VIGIL_GIT_INTERVAL", "3"),
    "pr_interval": ("VIGIL_PR_INTERVAL", "30"),
    "cache_ttl": ("VIGIL_CACHE_TTL", "30"),
    "log_level": ("VIGIL_LOG_LEVEL", "INFO"),
    "git_workers": ("VIGIL_GIT_WORKERS", "8"),
    "capture_window": ("VIGIL_CAPTURE_WINDOW", ""),
    "stale_threshold": ("VIGIL_STALE_THRESHOLD", "86400"),
    "notifications_enabled": ("VIGIL_NOTIFICATIONS", "true"),
    "auto_cleanup": ("VIGIL_AUTO_CLEANUP", "false"),
}


class HookNotConfigured(Exception):
    """Raised when a hook has no configuration and no built-in default."""


_config: dict | None = None


def load_config() -> dict:
    """Load and cache the TOML config file. Returns empty dict if missing."""
    global _config
    if _config is not None:
        return _config
    if not CONFIG_PATH.exists():
        _config = {}
        return _config
    try:
        _config = tomllib.loads(CONFIG_PATH.read_text())
    except Exception:
        logger.warning("Failed to parse %s, using defaults", CONFIG_PATH)
        _config = {}
    return _config


def reload_config() -> dict:
    """Force reload the config file."""
    global _config
    _config = None
    return load_config()


def get_setting(name: str) -> str:
    """Return a setting value. Priority: env var > TOML [settings] > default."""
    env_var, default = SETTING_DEFAULTS[name]
    # Env var overrides everything
    env_val = os.environ.get(env_var)
    if env_val is not None:
        return env_val
    # Then TOML [settings] section
    config = load_config()
    settings = config.get("settings", {})
    if name in settings:
        return str(settings[name])
    return default


def get_hook(name: str) -> str | None:
    """Return the hook command template, built-in default, or None."""
    config = load_config()
    hooks = config.get("hooks", {})
    if name in hooks:
        value = hooks[name]
        return value if value else None  # empty string = disabled
    return HOOK_DEFAULTS.get(name)


def expand_hook(template: str, variables: dict[str, str]) -> str:
    """Expand {placeholders} in template with shell-escaped values."""
    escaped = {k: shlex.quote(v) for k, v in variables.items()}
    try:
        return template.format_map(escaped)
    except KeyError as e:
        raise ValueError(f"Unknown placeholder in hook template: {e}") from e


def run_hook(
    name: str,
    variables: dict[str, str],
    *,
    cwd: str | None = None,
    timeout: int = 30,
) -> str:
    """Get hook template, expand it, and run via sh -c. Raises HookNotConfigured if no hook."""
    template = get_hook(name)
    if not template:
        raise HookNotConfigured(f"{name} hook not configured")
    cmd = expand_hook(template, variables)
    logger.debug("Running hook %s: %s", name, cmd)
    result = subprocess.run(
        ["sh", "-c", cmd],
        cwd=cwd,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        logger.error("Hook %s failed: %s", name, result.stderr.strip())
        raise subprocess.CalledProcessError(
            result.returncode, cmd, result.stdout, result.stderr,
        )
    return result.stdout.strip()

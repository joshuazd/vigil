from __future__ import annotations

import logging
import subprocess

logger = logging.getLogger(__name__)


def _run(args: list[str]) -> str:
    logger.debug("tmux: %s", args)
    result = subprocess.run(args, capture_output=True, text=True, timeout=5)
    if result.returncode != 0:
        logger.error("tmux failed: %s stderr=%s", args, result.stderr.strip())
        raise subprocess.CalledProcessError(
            result.returncode, args, result.stdout, result.stderr,
        )
    return result.stdout


def list_sessions() -> list[dict]:
    """Return list of {name, pane_path, created} sorted by creation time."""
    # Get first pane of first window per session (same as tmux-monitor)
    raw = _run([
        "tmux", "list-panes", "-a",
        "-F", "#{session_created}|#{session_name}|#{pane_current_path}",
        "-f", "#{&&:#{==:#{window_index},1},#{==:#{pane_index},1}}",
    ])
    sessions = []
    for line in sorted(raw.strip().splitlines()):
        if not line:
            continue
        parts = line.split("|", 2)
        if len(parts) < 3:
            continue
        sessions.append({
            "created": int(parts[0]),
            "name": parts[1],
            "pane_path": parts[2],
        })
    return sessions


def get_current_session() -> str:
    """Return name of the current tmux session, or empty string if outside tmux."""
    try:
        return _run(["tmux", "display-message", "-p", "#{session_name}"]).strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return ""


def get_last_session(current: str) -> str:
    """Return name of the most recently active session other than current."""
    try:
        raw = _run([
            "tmux", "list-sessions",
            "-F", "#{session_activity}|#{session_name}",
        ])
    except subprocess.CalledProcessError:
        return ""
    for line in sorted(raw.strip().splitlines(), reverse=True):
        parts = line.split("|", 1)
        if len(parts) == 2 and parts[1] != current:
            return parts[1]
    return ""


def get_bell_flags() -> dict[str, bool]:
    """Return {session_name: True} for sessions with bell flag active."""
    try:
        raw = _run([
            "tmux", "list-windows", "-a",
            "-F", "#{session_name}|#{window_bell_flag}",
        ])
    except subprocess.CalledProcessError:
        return {}
    bells: dict[str, bool] = {}
    for line in raw.strip().splitlines():
        parts = line.split("|", 1)
        if len(parts) == 2 and parts[1] == "1":
            bells[parts[0]] = True
    return bells


def capture_pane(session_name: str, lines: int = 20, window: str = "0") -> str:
    """Capture last N lines from a window of a session."""
    try:
        return _run([
            "tmux", "capture-pane",
            "-t", f"={session_name}:{window}",
            "-p", "-e", "-S", f"-{lines}",
        ]).rstrip()
    except subprocess.CalledProcessError:
        return ""


def switch_client(session_name: str) -> None:
    """Switch tmux client to the given session."""
    _run(["tmux", "switch-client", "-t", f"={session_name}"])

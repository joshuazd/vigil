from __future__ import annotations

from rich.text import Text
from textual.widgets import DataTable, Input, RichLog, Static

from . import tmux
from .models import STATE_STYLES, Session, SessionState


class SessionTable(DataTable):
    """Session list as a sortable DataTable."""

    SCOPED_CSS = False
    DEFAULT_CSS = """
    SessionTable {
        width: 1fr;
        height: 1fr;
        background: $background;
    }
    SessionTable:focus {
        background-tint: initial;
    }
    SessionTable > .datatable--cursor {
        background: $surface;
        color: initial;
        text-style: none;
    }
    SessionTable:focus > .datatable--cursor {
        background: $surface;
        color: initial;
        text-style: none;
    }
    SessionTable > .datatable--even-row {
        background: $background;
    }
    """

    def __init__(self, **kwargs) -> None:
        super().__init__(
            cursor_type="row",
            show_header=False,
            zebra_stripes=False,
            cursor_foreground_priority="renderable",
            **kwargs,
        )
        self._sessions: list[Session] = []
        self._row_keys: list[str] = []  # session name per row, in order
        self._columns_added = False

    def _ensure_columns(self) -> None:
        if self._columns_added:
            return
        self.add_column(" ", key="indicator", width=3)
        self.add_column(" ", key="state", width=2)
        self.add_column("Session", key="session", width=52)
        self.add_column("Git", key="git", width=18)
        self.add_column("PR", key="pr", width=22)
        self._columns_added = True

    def update_sessions(self, sessions: list[Session]) -> None:
        self._ensure_columns()

        new_keys = [s.name for s in sessions]
        new_map = {s.name: s for s in sessions}
        old_set = set(self._row_keys)
        new_set = set(new_keys)

        # Remove gone sessions
        for key in old_set - new_set:
            self.remove_row(key)

        # Add new sessions
        for key in new_keys:
            if key not in old_set:
                s = new_map[key]
                self.add_row(
                    _indicator(s),
                    _state_dot(s),
                    _session_name(s),
                    _git_col(s),
                    _pr_col(s),
                    key=key,
                )

        # Update existing sessions
        for key in old_set & new_set:
            s = new_map[key]
            self.update_cell(key, "indicator", _indicator(s))
            self.update_cell(key, "state", _state_dot(s))
            self.update_cell(key, "session", _session_name(s))
            self.update_cell(key, "git", _git_col(s))
            self.update_cell(key, "pr", _pr_col(s))

        self._sessions = sessions
        self._row_keys = new_keys

    def get_selected(self) -> Session | None:
        if not self._sessions:
            return None
        idx = self.cursor_row
        if 0 <= idx < len(self._sessions):
            # Map cursor index to session via ordered rows
            try:
                row_key = self._row_keys[idx]
                for s in self._sessions:
                    if s.name == row_key:
                        return s
            except IndexError:
                pass
        return None


class DetailPanel(RichLog):
    """Shows details for the selected session using RichLog."""

    DEFAULT_CSS = """
    DetailPanel {
        width: 1fr;
        height: 1fr;
        max-height: 50%;
        border-top: solid $accent;
    }
    DetailPanel:focus {
        background-tint: initial;
    }
    DetailPanel.hidden {
        display: none;
    }
    """

    def __init__(self, **kwargs) -> None:
        super().__init__(max_lines=200, wrap=False, markup=False, **kwargs)

    def update_session(self, session: Session | None) -> None:
        self.clear()
        if session is None:
            return

        # Header: icons + values
        header = Text()
        if session.git.branch:
            header.append("⎇ ", style="dim")
            header.append(session.git.branch, style="cyan")
        if session.pr and session.pr.number:
            header.append("  ")
            header.append_text(_colorize_pr(session.pr))
        # Git changes (without rebase age — show that separately)
        git_parts = []
        if session.git.modified:
            git_parts.append(f"~{session.git.modified}")
        if session.git.added:
            git_parts.append(f"+{session.git.added}")
        if session.git.deleted:
            git_parts.append(f"-{session.git.deleted}")
        if session.git.unpushed:
            git_parts.append(f"↑{session.git.unpushed}")
        if git_parts:
            header.append("  ± ", style="dim")
            header.append(" ".join(git_parts))
        # Rebase age
        age = session.git.rebase_age_display()
        if age:
            assert session.git.rebase_age_seconds is not None
            hours = session.git.rebase_age_seconds // 3600
            if hours < 24:
                header.append(f"  ↻ rebased {hours}h ago", style="dim")
            else:
                days = session.git.rebase_age_seconds // 86400
                header.append(f"  ↻ rebased {days}d ago", style="dim")
        state_style, state_dot = STATE_STYLES[session.state]
        header.append(f"  {state_dot} {session.state.value}", style=state_style)
        self.write(header)
        self.write("")

        # Captured pane output with ANSI colors
        available = max(4, self.size.height - 3) if self.size.height > 0 else 20
        pane_output = tmux.capture_pane(session.name, lines=available)
        if pane_output:
            for pane_line in pane_output.splitlines()[-available:]:
                stripped = pane_line.rstrip()
                if stripped:
                    self.write(Text.from_ansi(f"  {stripped}"))


class StatusBar(Static):
    """Top status bar showing session counts by state."""

    DEFAULT_CSS = """
    StatusBar {
        dock: top;
        width: 1fr;
        height: 1;
        background: $panel;
        padding: 0 1;
    }
    """

    def update_counts(self, sessions: list[Session]) -> None:
        counts: dict[SessionState, int] = {}
        for s in sessions:
            counts[s.state] = counts.get(s.state, 0) + 1
        total = len(sessions)

        line = Text()
        line.append("vigil", style="bold")
        line.append(f" · {total} sessions")

        for state in SessionState:
            n = counts.get(state, 0)
            if n > 0:
                style, _ = STATE_STYLES[state]
                line.append(f" · {n} {state.value}", style=style)

        self.update(line)


class DispatchInput(Input):
    """Input for dispatching a Shortcut URL or PR number."""

    DEFAULT_CSS = """
    DispatchInput {
        dock: bottom;
        width: 1fr;
        border: round $accent;
        margin: 0 1;
    }
    DispatchInput.hidden {
        display: none;
    }
    """

    def __init__(self, **kwargs) -> None:
        super().__init__(placeholder="Shortcut URL or PR number...", **kwargs)
        self.add_class("hidden")


# --- Cell renderers ---

def _indicator(s: Session) -> Text:
    if s.has_bell:
        return Text(" * ", style="bright_yellow")
    return Text(f" {s.indicator} ")


def _state_dot(s: Session) -> Text:
    style, dot = STATE_STYLES[s.state]
    return Text(dot, style=style)


def _session_name(s: Session) -> Text:
    name = s.name[:50] + "…" if len(s.name) > 50 else s.name
    return Text(name)


def _git_col(s: Session) -> Text:
    return Text(s.git.display())


def _pr_col(s: Session) -> Text:
    if not s.pr or not s.pr.number:
        return Text("—")
    return _colorize_pr(s.pr)


def _colorize_pr(pr) -> Text:
    """Apply colors to PR display string. Matches tmux-monitor palette."""
    rich = Text()
    if pr.is_draft:
        num_style = "dim"
    elif pr.state == "MERGED":
        num_style = "magenta"
    elif pr.state == "CLOSED":
        num_style = "red"
    else:
        num_style = "green"
    rich.append(f"#{pr.number}", style=num_style)
    if pr.checks == "pass":
        rich.append(" ✓", style="bright_green")
    elif pr.checks == "fail":
        rich.append(" ✗", style="bright_red")
    elif pr.checks == "pending":
        rich.append(" ●", style="bright_yellow")
    if pr.state == "OPEN":
        if pr.review_decision == "APPROVED":
            rich.append(" ☑", style="bright_green")
        elif pr.review_decision == "CHANGES_REQUESTED":
            rich.append(" ✎", style="bright_red")
        elif pr.approvals > 0:
            rich.append(" ☑", style="bright_yellow")
        if pr.unresolved_comments > 0:
            rich.append(f" ☐ {pr.unresolved_comments}", style="bright_yellow")
        if pr.has_conflicts:
            rich.append(" ⚡", style="bright_red")
    return rich

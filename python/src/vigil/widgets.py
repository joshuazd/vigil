from __future__ import annotations

from enum import Enum

from rich.text import Text
from textual.widgets import DataTable, Input, RichLog, Static

from . import config, tmux
from .models import STATE_STYLES, Session, SessionState, SortMode


class DetailMode(Enum):
    PANE = "pane"
    PR_DESC = "pr"
    PR_COMMENTS = "comments"


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

    def update_sessions(
        self,
        sessions: list[Session],
        *,
        filter_state: SessionState | None = None,
        selected: set[str] | None = None,
    ) -> None:
        self._ensure_columns()
        self._sessions = sessions
        sel = selected or set()

        visible = [s for s in sessions if s.state == filter_state] if filter_state else sessions
        new_keys = [s.name for s in visible]
        new_map = {s.name: s for s in visible}
        old_set = set(self._row_keys)
        new_set = set(new_keys)

        # Full rebuild if order changed (e.g. sort mode switch)
        order_changed = (
            new_keys != self._row_keys
            and new_set == old_set
            and len(new_keys) == len(self._row_keys)
        )

        if order_changed:
            self.clear()
            old_set = set()

        # Remove gone sessions
        for key in old_set - new_set:
            self.remove_row(key)

        # Add new sessions
        for key in new_keys:
            if key not in old_set:
                s = new_map[key]
                self.add_row(
                    _indicator(s, key in sel),
                    _state_dot(s),
                    _session_name(s),
                    _git_col(s),
                    _pr_col(s),
                    key=key,
                )

        # Update existing sessions
        for key in old_set & new_set:
            s = new_map[key]
            self.update_cell(key, "indicator", _indicator(s, key in sel))
            self.update_cell(key, "state", _state_dot(s))
            self.update_cell(key, "session", _session_name(s))
            self.update_cell(key, "git", _git_col(s))
            self.update_cell(key, "pr", _pr_col(s))

        self._row_keys = new_keys

    def select_session(self, name: str) -> bool:
        """Move cursor to the row matching *name*. Returns True on success."""
        try:
            idx = self._row_keys.index(name)
        except ValueError:
            return False
        self.move_cursor(row=idx)
        return True

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

    _MODE_ORDER = list(DetailMode)

    def __init__(self, **kwargs) -> None:
        super().__init__(max_lines=200, wrap=False, markup=False, **kwargs)
        self._mode: DetailMode | None = None  # None = auto-select
        self._last_session_name: str = ""

    def _auto_mode(self, session: Session) -> DetailMode:
        """Pick detail mode based on session state."""
        if session.state == SessionState.UNRESOLVED:
            return DetailMode.PR_COMMENTS
        if session.state in (SessionState.REVIEW, SessionState.PENDING, SessionState.BLOCKED):
            return DetailMode.PR_DESC
        return DetailMode.PANE

    @property
    def mode(self) -> DetailMode:
        return self._mode or DetailMode.PANE

    def cycle_mode(self) -> None:
        """Advance to next detail mode (manual override)."""
        modes = self._MODE_ORDER
        idx = modes.index(self.mode)
        self._mode = modes[(idx + 1) % len(modes)]

    def update_session(self, session: Session | None) -> None:
        self.clear()
        if session is None:
            return

        # Reset to auto-select when cursor moves to a different session
        if session.name != self._last_session_name:
            self._mode = None
            self._last_session_name = session.name

        active_mode = self._mode if self._mode is not None else self._auto_mode(session)

        self._render_header(session, active_mode)
        self.write("")

        if active_mode == DetailMode.PR_DESC:
            self.auto_scroll = False
            self.wrap = True
            self._render_pr_desc(session)
            self.scroll_home(animate=False)
        elif active_mode == DetailMode.PR_COMMENTS:
            self.auto_scroll = False
            self.wrap = True
            self._render_pr_comments(session)
            self.scroll_home(animate=False)
        else:
            self.auto_scroll = True
            self.wrap = False
            self._render_pane(session)

    def _render_header(self, session: Session, active_mode: DetailMode) -> None:
        header = Text()
        if session.git.branch:
            header.append("⎇ ", style="dim")
            header.append(session.git.branch, style="cyan")
        if session.pr and session.pr.number:
            header.append("  ")
            header.append_text(_colorize_pr(session.pr))
        # Git changes
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
        if age and session.git.rebase_age_seconds is not None:
            threshold = int(config.get_setting("stale_threshold"))
            stale = session.git.is_stale(threshold)
            hours = session.git.rebase_age_seconds // 3600
            if hours < 24:
                label = f"  ↻ rebased {hours}h ago"
            else:
                days = session.git.rebase_age_seconds // 86400
                label = f"  ↻ rebased {days}d ago"
            if stale:
                header.append(f"  ⚠{label.strip()}", style="bright_red")
            else:
                header.append(label, style="dim")
        state_style, state_dot = STATE_STYLES[session.state]
        header.append(f"  {state_dot} {session.state.value}", style=state_style)
        # Mode indicator
        header.append(f"  [{active_mode.value}]", style="dim")
        self.write(header)

    def _render_pane(self, session: Session) -> None:
        available = max(4, self.size.height - 3) if self.size.height > 0 else 20
        window = config.get_setting("capture_window") or None
        pane_output = tmux.capture_pane(session.name, lines=available, window=window)
        if pane_output:
            for pane_line in pane_output.splitlines()[-available:]:
                stripped = pane_line.rstrip()
                if stripped:
                    self.write(Text.from_ansi(f"  {stripped}"))

    def _render_pr_desc(self, session: Session) -> None:
        if not session.pr or not session.pr.number:
            self.write(Text("  No PR", style="dim"))
            return
        if session.pr.title:
            self.write(Text(f"  {session.pr.title}", style="bold"))
        if session.pr.body:
            self.write("")
            for line in session.pr.body.splitlines():
                self.write(Text(f"  {line}"))

    def _render_pr_comments(self, session: Session) -> None:
        if not session.pr or not session.pr.review_comments:
            self.write(Text("  No review comments", style="dim"))
            return
        unresolved = [c for c in session.pr.review_comments if not c.get("resolved")]
        if not unresolved:
            self.write(Text("  All comments resolved", style="dim"))
            return
        for i, c in enumerate(unresolved):
            if i > 0:
                self.write("")
            path = c.get("path", "")
            author = c.get("author", "")
            header = Text()
            header.append(f"  {author}", style="cyan")
            if path:
                header.append(f"  {path}", style="dim")
            self.write(header)
            body = c.get("body", "")
            for line in body.splitlines():
                self.write(Text(f"    {line}"))


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

    def update_counts(
        self,
        sessions: list[Session],
        *,
        active_filter: SessionState | None = None,
        active_sort: SortMode = SortMode.CREATED,
    ) -> None:
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

        if active_filter is not None:
            fstyle, _ = STATE_STYLES[active_filter]
            line.append(f" · filter: {active_filter.value}", style=fstyle)

        if active_sort != SortMode.CREATED:
            line.append(f" · sort: {active_sort.value}", style="dim")

        self.update(line)


class DispatchInput(Input):
    """Input for dispatching a URL or identifier to an external command."""

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
        super().__init__(placeholder="URL or identifier...", **kwargs)
        self.add_class("hidden")


# --- Cell renderers ---

def _indicator(s: Session, selected: bool = False) -> Text:
    if selected:
        return Text(" ◆ ", style="bright_cyan")
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
    threshold = int(config.get_setting("stale_threshold"))
    stale = s.git.is_stale(threshold)

    parts: list[str] = []
    if s.git.modified:
        parts.append(f"~{s.git.modified}")
    if s.git.added:
        parts.append(f"+{s.git.added}")
    if s.git.deleted:
        parts.append(f"-{s.git.deleted}")
    if s.git.unpushed:
        parts.append(f"↑{s.git.unpushed}")

    age = s.git.rebase_age_display()
    if not parts and not age:
        return Text("—")

    t = Text()
    if parts:
        t.append(" ".join(parts))
    if age:
        if parts:
            t.append(" ")
        age_str = f"⚠{age}" if stale else age
        t.append(age_str, style="bright_red" if stale else "")
    return t


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

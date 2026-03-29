from __future__ import annotations

import os
from concurrent.futures import ThreadPoolExecutor

from textual import work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.message import Message
from textual.widgets import Footer, Input

from . import actions, cache, tmux
from . import git_status as git_mod
from . import pr_status as pr_mod
from .models import GitStatus, PRStatus, Session
from .widgets import DetailPanel, DispatchInput, SessionTable, StatusBar


class SessionsUpdated(Message):
    """Posted when session/git data has been refreshed."""

    def __init__(self, sessions: list[Session]) -> None:
        super().__init__()
        self.sessions = sessions


class PullRequestsUpdated(Message):
    """Posted when PR data has been refreshed."""

    def __init__(self, pr_data: dict[str, PRStatus | None]) -> None:
        super().__init__()
        self.pr_data = pr_data


class VigilApp(App):
    TITLE = "vigil"
    COMMANDS = set()
    ENABLE_COMMAND_PALETTE = False
    theme = "nord"

    CSS = """
    Screen {
        background: $background;
    }
    #session-table {
        width: 1fr;
        height: 1fr;
        background: $background;
    }
    #detail-panel {
        background: $background;
        scrollbar-size: 0 0;
    }
    Footer {
        background: $panel;
        color: $foreground;
    }
    Footer .footer-key--key {
        background: transparent;
        color: $primary;
    }
    Footer .footer-key--description {
        background: transparent;
        color: $foreground;
    }
    """

    BINDINGS = [
        Binding("q", "quit", "Quit"),
        Binding("j", "cursor_down", "Down", show=False),
        Binding("k", "cursor_up", "Up", show=False),
        Binding("enter", "select_session", "Select", priority=True),
        Binding("o", "open_pr", "Open PR"),
        Binding("m", "merge_pr", "Merge"),
        Binding("a", "approve_pr", "Approve"),
        Binding("x", "cleanup", "Cleanup"),
        Binding("d", "dispatch", "Dispatch"),
        Binding("b", "rebase_push", "Rebase"),
        Binding("r", "refresh", "Refresh"),
        Binding("tab", "toggle_detail", "Detail", priority=True),
        Binding("escape", "cancel", "Cancel", show=False),
    ]

    def __init__(self, *args, **kwargs) -> None:
        super().__init__(*args, ansi_color=True, **kwargs)
        self.sessions: list[Session] = []
        self._git_cache: dict[str, GitStatus] = {}
        self._pr_cache: dict[str, PRStatus | None] = {}
        self._popup_mode = "TMUX" in os.environ
        self._confirm_action: str = ""
        self._confirm_session: Session | None = None
        self._initial_pr_done = False

    def compose(self) -> ComposeResult:
        yield StatusBar(id="status-bar")
        yield SessionTable(id="session-table")
        yield DetailPanel(id="detail-panel")
        yield DispatchInput(id="dispatch-input")
        yield Footer()

    def on_mount(self) -> None:
        # Load cache for instant display
        cached = cache.load()
        if cached:
            self.sessions = cached
            for s in cached:
                self._git_cache[s.name] = s.git
                if s.pr and s.git.branch:
                    self._pr_cache[s.git.branch] = s.pr
            self.post_message(SessionsUpdated(cached))
        self._poll_sessions()
        self.set_interval(3, self._poll_sessions)
        self.set_interval(30, self._poll_pr_status)

    # --- Workers ---

    @work(thread=True, exclusive=True, group="tmux")
    def _poll_sessions(self) -> None:
        """Fetch tmux sessions, post immediately, then fill in git status."""
        try:
            raw_sessions = tmux.list_sessions()
        except Exception:
            return

        current = tmux.get_current_session()
        last = tmux.get_last_session(current)
        bell_flags = tmux.get_bell_flags()

        sessions: list[Session] = []
        for raw in raw_sessions:
            name = raw["name"]
            s = Session(
                name=name,
                pane_path=raw["pane_path"],
                created=raw["created"],
                is_current=(name == current),
                is_last=(name == last),
                has_bell=bell_flags.get(name, False),
            )
            # Apply cached git/PR data for instant display
            if name in self._git_cache:
                s.git = self._git_cache[name]
            if s.git.branch and s.git.branch in self._pr_cache:
                s.pr = self._pr_cache[s.git.branch]
            sessions.append(s)

        # Post immediately so UI appears fast
        self.post_message(SessionsUpdated(sessions))

        # Fetch git status in parallel (git status --porcelain is ~750ms each)
        with ThreadPoolExecutor(max_workers=len(sessions) or 1) as pool:
            git_results = list(pool.map(lambda s: git_mod.fetch(s.pane_path), sessions))
        for s, git in zip(sessions, git_results):
            s.git = git
            self._git_cache[s.name] = git
            if s.git.branch and s.git.branch in self._pr_cache:
                s.pr = self._pr_cache[s.git.branch]

        self.post_message(SessionsUpdated(sessions))

    def _fetch_pr_data_incremental(self) -> None:
        """Fetch PR status one branch at a time, posting after each."""
        seen: dict[str, str] = {}
        for s in self.sessions:
            if s.git.branch and s.git.git_root and s.git.branch not in seen:
                seen[s.git.branch] = s.git.git_root
        for branch, git_root in seen.items():
            pr = pr_mod.fetch(branch, git_root)
            self.post_message(PullRequestsUpdated({branch: pr}))

    @work(thread=True, exclusive=True, group="pr")
    def _poll_pr_status(self) -> None:
        self._fetch_pr_data_incremental()

    @work(thread=True, group="pr-init")
    def _do_initial_pr_poll(self) -> None:
        self._fetch_pr_data_incremental()

    # --- Event handlers ---

    def on_sessions_updated(self, event: SessionsUpdated) -> None:
        self.sessions = event.sessions
        table = self.query_one("#session-table", SessionTable)
        table.update_sessions(self.sessions)
        self.query_one("#status-bar", StatusBar).update_counts(self.sessions)
        # Update detail panel for currently selected session
        selected = table.get_selected()
        detail = self.query_one("#detail-panel", DetailPanel)
        if not detail.has_class("hidden"):
            detail.update_session(selected)
        # Trigger initial PR poll once we have git data (branches populated)
        has_branches = any(s.git.branch for s in self.sessions)
        if not self._initial_pr_done and has_branches:
            self._initial_pr_done = True
            self._do_initial_pr_poll()
        cache.save(self.sessions)

    def on_pull_requests_updated(self, event: PullRequestsUpdated) -> None:
        self._pr_cache.update(event.pr_data)
        for s in self.sessions:
            if s.git.branch in event.pr_data:
                s.pr = event.pr_data[s.git.branch]
        self.query_one("#session-table", SessionTable).update_sessions(self.sessions)
        self.query_one("#status-bar", StatusBar).update_counts(self.sessions)
        cache.save(self.sessions)

    def on_input_submitted(self, event: Input.Submitted) -> None:
        if isinstance(event.input, DispatchInput):
            text = event.value.strip()
            if text:
                self._do_dispatch(text)
            self._hide_dispatch_input()

    # --- Actions ---

    def _get_selected(self) -> Session | None:
        return self.query_one("#session-table", SessionTable).get_selected()

    def action_cursor_down(self) -> None:
        table = self.query_one("#session-table", SessionTable)
        table.action_cursor_down()
        self._update_detail()

    def action_cursor_up(self) -> None:
        table = self.query_one("#session-table", SessionTable)
        table.action_cursor_up()
        self._update_detail()

    def action_select_session(self) -> None:
        session = self._get_selected()
        if not session:
            return
        if self._popup_mode:
            try:
                tmux.switch_client(session.name)
            except Exception:
                self.notify(f"Failed to switch to {session.name}", severity="error")
            self.exit()
        else:
            self.action_toggle_detail()

    def action_toggle_detail(self) -> None:
        detail = self.query_one("#detail-panel", DetailPanel)
        detail.toggle_class("hidden")
        if not detail.has_class("hidden"):
            self._update_detail()

    def _update_detail(self) -> None:
        detail = self.query_one("#detail-panel", DetailPanel)
        if not detail.has_class("hidden"):
            detail.update_session(self._get_selected())

    def action_open_pr(self) -> None:
        session = self._get_selected()
        if session and session.pr and session.pr.url:
            actions.open_pr_in_browser(session.pr.url)
            self.notify(f"Opened PR #{session.pr.number}")

    def action_merge_pr(self) -> None:
        session = self._get_selected()
        if not session or not session.pr or not session.pr.number:
            self.notify("No PR for this session", severity="warning")
            return
        if not self._confirm_action:
            self._confirm_action = "merge"
            self._confirm_session = session
            self.notify(f"Press m again to merge PR #{session.pr.number}", severity="warning")
            return
        if self._confirm_action == "merge" and self._confirm_session is session:
            self._confirm_action = ""
            self._confirm_session = None
            self._do_merge(session)
        else:
            self._confirm_action = ""
            self._confirm_session = None

    def action_approve_pr(self) -> None:
        session = self._get_selected()
        if not session or not session.pr or not session.pr.number:
            self.notify("No PR for this session", severity="warning")
            return
        self._do_approve(session)

    def action_cleanup(self) -> None:
        session = self._get_selected()
        if not session:
            return
        if not self._confirm_action:
            self._confirm_action = "cleanup"
            self._confirm_session = session
            self.notify(f"Press x again to cleanup {session.name}", severity="warning")
            return
        if self._confirm_action == "cleanup" and self._confirm_session is session:
            self._confirm_action = ""
            self._confirm_session = None
            self._do_cleanup(session)
        else:
            self._confirm_action = ""
            self._confirm_session = None

    def action_dispatch(self) -> None:
        dispatch_input = self.query_one("#dispatch-input", DispatchInput)
        dispatch_input.remove_class("hidden")
        dispatch_input.value = ""
        dispatch_input.focus()

    def action_cancel(self) -> None:
        self._confirm_action = ""
        self._confirm_session = None
        self._hide_dispatch_input()

    def _hide_dispatch_input(self) -> None:
        dispatch_input = self.query_one("#dispatch-input", DispatchInput)
        dispatch_input.add_class("hidden")
        self.query_one("#session-table", SessionTable).focus()

    def action_rebase_push(self) -> None:
        session = self._get_selected()
        if not session or not session.git.git_root or not session.git.branch:
            self.notify("No git branch for this session", severity="warning")
            return
        if session.git.branch in ("main", "master"):
            self.notify("Can't rebase main", severity="warning")
            return
        self._do_rebase_push(session)

    def action_quit(self) -> None:
        self.workers.cancel_all()
        self.exit()

    def action_refresh(self) -> None:
        self._poll_sessions()
        self._poll_pr_status()

    # --- Action workers ---

    @work(thread=True)
    def _do_merge(self, session: Session) -> None:
        try:
            actions.merge_pr(session.git.git_root, session.git.branch)
            self.notify(f"Merged PR #{session.pr.number}")
        except Exception as e:
            self.notify(f"Merge failed: {e}", severity="error")
        self._poll_pr_status()

    @work(thread=True)
    def _do_approve(self, session: Session) -> None:
        try:
            actions.approve_pr(session.git.git_root, session.git.branch)
            self.notify(f"Approved PR #{session.pr.number}")
        except Exception as e:
            self.notify(f"Approve failed: {e}", severity="error")
        self._poll_pr_status()

    @work(thread=True)
    def _do_rebase_push(self, session: Session) -> None:
        self.notify(f"Rebasing {session.name}...")
        try:
            actions.rebase_and_push(session.git.git_root)
            self.notify(f"Rebased and pushed {session.name}")
        except RuntimeError as e:
            self.notify(f"{e}", severity="error")
        except Exception as e:
            self.notify(f"Rebase failed: {e}", severity="error")
        self._poll_sessions()

    @work(thread=True)
    def _do_cleanup(self, session: Session) -> None:
        try:
            actions.cleanup_session(session.name, session.pane_path)
            self.notify(f"Cleaned up {session.name}")
        except Exception as e:
            self.notify(f"Cleanup failed: {e}", severity="error")
        self._poll_sessions()

    @work(thread=True)
    def _do_dispatch(self, input_str: str) -> None:
        try:
            actions.dispatch(input_str)
            self.notify(f"Dispatched: {input_str}")
        except Exception as e:
            self.notify(f"Dispatch failed: {e}", severity="error")
        self._poll_sessions()


def _check_dependencies() -> None:
    """Verify required CLI tools are available."""
    import shutil
    missing = [cmd for cmd in ("tmux", "git", "gh") if not shutil.which(cmd)]
    if missing:
        raise SystemExit(f"vigil: missing required tools: {', '.join(missing)}")


def main() -> None:
    _check_dependencies()
    app = VigilApp()
    app.run()


if __name__ == "__main__":
    main()

from __future__ import annotations

import asyncio
import logging
import os
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Literal

from textual import work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.message import Message
from textual.widgets import Footer, Input

from . import actions, cache, config, tmux
from . import git_status as git_mod
from . import pr_status as pr_mod
from .git_status import detect_default_branch
from .models import GitStatus, PRStatus, Session, SessionState, SortMode
from .widgets import DetailPanel, DispatchInput, SessionTable, StatusBar

logger = logging.getLogger(__name__)

_AUTO_FOCUS_STATES = (
    SessionState.ATTENTION,
    SessionState.BLOCKED,
    SessionState.UNRESOLVED,
    SessionState.MERGEABLE,
)
_AUTO_FOCUS_COOLDOWN = 15  # seconds


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


class _ClearSelection(Message):
    """Posted by batch workers to clear selection on the main thread."""


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
        Binding("q", "quit", "Quit", show=False),
        Binding("j", "cursor_down", "Down", show=False),
        Binding("k", "cursor_up", "Up", show=False),
        Binding("enter", "select_session", "Select", priority=True, show=False),
        Binding("o", "open_pr", "Open PR"),
        Binding("m", "merge_pr", "Merge"),
        Binding("a", "approve_pr", "Approve"),
        Binding("x", "cleanup", "Cleanup"),
        Binding("D", "toggle_draft", "Draft"),
        Binding("d", "dispatch", "Dispatch", show=False),
        Binding("b", "rebase_push", "Rebase"),
        Binding("r", "refresh", "Refresh", show=False),
        Binding("tab", "toggle_detail", "Detail", priority=True),
        Binding("f", "cycle_filter", "Filter"),
        Binding("F", "cycle_filter_back", "Filter◀", show=False),
        Binding("p", "cycle_detail_mode", "PR Info"),
        Binding("s", "cycle_sort", "Sort"),
        Binding("S", "cycle_sort_back", "Sort◀", show=False),
        Binding("space", "toggle_select", "Select", priority=True, show=False),
        Binding("escape", "cancel", "Cancel", show=False),
    ]

    def __init__(self, *args, **kwargs) -> None:
        kwargs.setdefault("ansi_color", True)
        super().__init__(*args, **kwargs)
        self.sessions: list[Session] = []
        self._git_cache: dict[str, GitStatus] = {}
        self._pr_cache: dict[str, PRStatus | None] = {}
        self._popup_mode = "TMUX" in os.environ
        self._confirm_action: str = ""
        self._confirm_session: Session | None = None
        self._initial_pr_done = False
        self._shutting_down = threading.Event()
        self._filter_state: SessionState | None = None
        self._prev_states: dict[str, SessionState] = {}
        self._initial_load = True
        self._selected_sessions: set[str] = set()
        self._sort_mode: SortMode = SortMode.CREATED
        self._last_manual_nav: float = 0.0

    def compose(self) -> ComposeResult:
        yield StatusBar(id="status-bar")
        yield SessionTable(id="session-table")
        yield DetailPanel(id="detail-panel")
        yield DispatchInput(id="dispatch-input")
        yield Footer()

    def on_mount(self) -> None:
        git_interval = int(config.get_setting("git_interval"))
        pr_interval = int(config.get_setting("pr_interval"))
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
        self.set_interval(git_interval, self._poll_sessions)
        self.set_interval(pr_interval, self._poll_pr_status)

    # --- Workers ---

    @work(thread=True, exclusive=True, group="tmux")
    def _poll_sessions(self) -> None:
        """Fetch tmux sessions, post immediately, then fill in git status."""
        try:
            raw_sessions = tmux.list_sessions()
        except Exception:
            logger.exception("poll_sessions: list_sessions failed")
            self.notify("Session refresh failed", severity="warning")
            return

        try:
            current = tmux.get_current_session()
        except Exception:
            current = ""
        try:
            last = tmux.get_last_session(current)
        except Exception:
            last = ""
        try:
            bell_flags = tmux.get_bell_flags()
        except Exception:
            bell_flags = {}

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
        if self._shutting_down.is_set():
            return
        _git_workers = int(config.get_setting("git_workers"))
        pool = ThreadPoolExecutor(max_workers=min(len(sessions), _git_workers) or 1)
        try:
            futures = {pool.submit(git_mod.fetch, s.pane_path): s for s in sessions}
            for future in as_completed(futures):
                if self._shutting_down.is_set():
                    for f in futures:
                        f.cancel()
                    return
                try:
                    git = future.result()
                except Exception:
                    continue
                s = futures[future]
                s.git = git
                self._git_cache[s.name] = git
                if s.git.branch and s.git.branch in self._pr_cache:
                    s.pr = self._pr_cache[s.git.branch]
        finally:
            pool.shutdown(wait=False, cancel_futures=True)

        if not self._shutting_down.is_set():
            self.post_message(SessionsUpdated(sessions))

    def _fetch_pr_data_incremental(self) -> None:
        """Fetch PR status one branch at a time, posting after each."""
        seen: dict[str, str] = {}
        for s in self.sessions:
            if s.git.branch and s.git.git_root and s.git.branch not in seen:
                seen[s.git.branch] = s.git.git_root
        for branch, git_root in seen.items():
            if self._shutting_down.is_set():
                return
            pr = pr_mod.fetch(branch, git_root, cancel=self._shutting_down)
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
        table.update_sessions(
            self._sorted(self.sessions), filter_state=self._filter_state,
            selected=self._selected_sessions,
        )
        self.query_one("#status-bar", StatusBar).update_counts(
            self.sessions, active_filter=self._filter_state,
            active_sort=self._sort_mode,
        )
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
        self._check_state_transitions()
        cache.save(self.sessions)

    def on_pull_requests_updated(self, event: PullRequestsUpdated) -> None:
        self._pr_cache.update(event.pr_data)
        for s in self.sessions:
            if s.git.branch in event.pr_data:
                s.pr = event.pr_data[s.git.branch]
        self.query_one("#session-table", SessionTable).update_sessions(
            self._sorted(self.sessions), filter_state=self._filter_state,
            selected=self._selected_sessions,
        )
        self.query_one("#status-bar", StatusBar).update_counts(
            self.sessions, active_filter=self._filter_state,
            active_sort=self._sort_mode,
        )
        self._check_state_transitions()
        cache.save(self.sessions)

    def on__clear_selection(self, event: _ClearSelection) -> None:
        self._selected_sessions.clear()
        self._apply_filter()

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
        self._last_manual_nav = time.monotonic()
        table = self.query_one("#session-table", SessionTable)
        if table.row_count and table.cursor_row >= table.row_count - 1:
            table.move_cursor(row=0)
        else:
            table.action_cursor_down()
        self._update_detail()

    def action_cursor_up(self) -> None:
        self._last_manual_nav = time.monotonic()
        table = self.query_one("#session-table", SessionTable)
        if table.row_count and table.cursor_row <= 0:
            table.move_cursor(row=table.row_count - 1)
        else:
            table.action_cursor_up()
        self._update_detail()

    def action_toggle_select(self) -> None:
        self._last_manual_nav = time.monotonic()
        session = self._get_selected()
        if not session:
            return
        if session.name in self._selected_sessions:
            self._selected_sessions.discard(session.name)
        else:
            self._selected_sessions.add(session.name)
        self._apply_filter()
        self.action_cursor_down()

    def _get_batch_sessions(self) -> list[Session]:
        """Return selected sessions visible under current filter, or empty list."""
        if not self._selected_sessions:
            return []
        visible = (
            [s for s in self.sessions if s.state == self._filter_state]
            if self._filter_state else self.sessions
        )
        return [s for s in visible if s.name in self._selected_sessions]

    def action_select_session(self) -> None:
        session = self._get_selected()
        if not session:
            return
        if self._popup_mode:
            try:
                tmux.switch_client(session.name)
            except Exception:
                self.notify(f"Failed to switch to {session.name}", severity="error")
            self._shutting_down.set()
            self.workers.cancel_all()
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

    def action_cycle_detail_mode(self) -> None:
        detail = self.query_one("#detail-panel", DetailPanel)
        if detail.has_class("hidden"):
            detail.remove_class("hidden")
        detail.cycle_mode()
        self._update_detail()

    def action_open_pr(self) -> None:
        session = self._get_selected()
        if session and session.pr and session.pr.url:
            actions.open_pr_in_browser(session.pr.url)
            if self._popup_mode:
                self._shutting_down.set()
                self.exit()
                return
            self.notify(f"Opened PR #{session.pr.number}")

    def action_merge_pr(self) -> None:
        batch = self._get_batch_sessions()
        if batch:
            mergeable = [s for s in batch if s.pr and s.pr.number]
            if not mergeable:
                self.notify("No PRs in selection", severity="warning")
                return
            if not self._confirm_action:
                self._confirm_action = "merge"
                self._confirm_session = None
                self.notify(f"Press m again to merge {len(mergeable)} PRs", severity="warning")
                return
            if self._confirm_action == "merge":
                self._confirm_action = ""
                self._do_batch_merge(mergeable)
                return

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
        batch = self._get_batch_sessions()
        if batch:
            approvable = [s for s in batch if s.pr and s.pr.number]
            if not approvable:
                self.notify("No PRs in selection", severity="warning")
                return
            self._do_batch_approve(approvable)
            return

        session = self._get_selected()
        if not session or not session.pr or not session.pr.number:
            self.notify("No PR for this session", severity="warning")
            return
        self._do_approve(session)

    def action_cleanup(self) -> None:
        batch = self._get_batch_sessions()
        if batch:
            if not self._confirm_action:
                self._confirm_action = "cleanup"
                self._confirm_session = None
                self.notify(f"Press x again to cleanup {len(batch)} sessions", severity="warning")
                return
            if self._confirm_action == "cleanup":
                self._confirm_action = ""
                self._do_batch_cleanup(batch)
                return

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
        if self._selected_sessions:
            self._selected_sessions.clear()
            self._apply_filter()
        self._hide_dispatch_input()

    def _hide_dispatch_input(self) -> None:
        dispatch_input = self.query_one("#dispatch-input", DispatchInput)
        dispatch_input.add_class("hidden")
        self.query_one("#session-table", SessionTable).focus()

    def action_rebase_push(self) -> None:
        batch = self._get_batch_sessions()
        if batch:
            rebaseable = [
                s for s in batch
                if s.git.git_root and s.git.branch
                and s.git.branch != detect_default_branch(s.git.git_root)
            ]
            if not rebaseable:
                self.notify("No rebaseable branches in selection", severity="warning")
                return
            self._do_batch_rebase(rebaseable)
            return

        session = self._get_selected()
        if not session or not session.git.git_root or not session.git.branch:
            self.notify("No git branch for this session", severity="warning")
            return
        default_branch = detect_default_branch(session.git.git_root)
        if default_branch and session.git.branch == default_branch:
            self.notify(f"Can't rebase {default_branch}", severity="warning")
            return
        self._do_rebase_push(session)

    def action_toggle_draft(self) -> None:
        batch = self._get_batch_sessions()
        if batch:
            with_pr = [s for s in batch if s.pr and s.pr.number]
            if not with_pr:
                self.notify("No PRs in selection", severity="warning")
                return
            self._do_batch_toggle_draft(with_pr)
            return

        session = self._get_selected()
        if not session or not session.pr or not session.pr.number:
            self.notify("No PR for this session", severity="warning")
            return
        self._do_toggle_draft(session)

    async def action_quit(self) -> None:
        self._shutting_down.set()
        self.workers.cancel_all()
        active = [w for w in self.workers if not w.is_finished]
        if active:
            self.notify("Shutting down...")
            for _ in range(30):  # 30 x 0.1s = 3s max wait
                if all(w.is_finished for w in active):
                    break
                await asyncio.sleep(0.1)
        self.exit()

    _NOTIFY_SEVERITY: dict[SessionState, Literal["information", "warning", "error"]] = {
        SessionState.BLOCKED: "error",
        SessionState.UNRESOLVED: "error",
        SessionState.MERGEABLE: "information",
        SessionState.DONE: "information",
    }

    def _check_state_transitions(self) -> None:
        """Detect state changes and fire notifications + auto-cleanup."""
        if self._initial_load:
            # Populate baseline without notifying
            has_git = any(s.git.branch for s in self.sessions)
            if has_git:
                self._initial_load = False
            self._prev_states = {s.name: s.state for s in self.sessions}
            return

        notify = config.get_setting("notifications_enabled") == "true"
        auto_cleanup = config.get_setting("auto_cleanup") == "true"

        for s in self.sessions:
            old = self._prev_states.get(s.name)
            if old is None or old == s.state:
                continue
            if notify:
                severity = self._NOTIFY_SEVERITY.get(s.state, "information")
                self.notify(
                    f"{s.name}: {old.value} → {s.state.value}",
                    severity=severity,
                )
                self._run_notify_hook(s.name, old, s.state)
            if auto_cleanup and s.state == SessionState.DONE and not s.is_current:
                self._do_cleanup(s)

        self._maybe_auto_focus()
        self._prev_states = {s.name: s.state for s in self.sessions}

    def _maybe_auto_focus(self) -> None:
        """Move cursor to highest-priority session that just became actionable."""
        if self._popup_mode:
            return
        if config.get_setting("auto_focus") != "true":
            return
        if time.monotonic() - self._last_manual_nav < _AUTO_FOCUS_COOLDOWN:
            return

        # Find sessions that just transitioned into an auto-focus state, pick highest priority
        best: Session | None = None
        best_priority = len(_AUTO_FOCUS_STATES)
        for s in self.sessions:
            old = self._prev_states.get(s.name)
            if old is None or old == s.state:
                continue
            if s.state in _AUTO_FOCUS_STATES:
                priority = _AUTO_FOCUS_STATES.index(s.state)
                if priority < best_priority:
                    best = s
                    best_priority = priority

        if best is None:
            return

        table = self.query_one("#session-table", SessionTable)
        try:
            row_idx = table._row_keys.index(best.name)
            table.move_cursor(row=row_idx)
            self._update_detail()
        except ValueError:
            pass  # Session not visible (filtered out)

    @work(thread=True)
    def _run_notify_hook(self, session_name: str, old: SessionState, new: SessionState) -> None:
        try:
            config.run_hook("notify", {
                "session": session_name,
                "old_state": old.value,
                "new_state": new.value,
            })
        except config.HookNotConfigured:
            pass
        except Exception as e:
            logger.debug("notify hook failed: %s", e)

    def _sorted(self, sessions: list[Session]) -> list[Session]:
        """Sort sessions by current sort mode."""
        if self._sort_mode == SortMode.STATE:
            state_order = list(SessionState)
            return sorted(sessions, key=lambda s: state_order.index(s.state))
        if self._sort_mode == SortMode.ALPHA:
            return sorted(sessions, key=lambda s: s.name.lower())
        return sessions  # CREATED — already in creation order from tmux

    def _apply_filter(self) -> None:
        """Re-render table and status bar with current filter and sort."""
        table = self.query_one("#session-table", SessionTable)
        table.update_sessions(
            self._sorted(self.sessions), filter_state=self._filter_state,
            selected=self._selected_sessions,
        )
        self.query_one("#status-bar", StatusBar).update_counts(
            self.sessions, active_filter=self._filter_state,
            active_sort=self._sort_mode,
        )

    def action_cycle_filter(self) -> None:
        states = list(SessionState)
        if self._filter_state is None:
            self._filter_state = states[0]
        else:
            idx = states.index(self._filter_state)
            self._filter_state = states[idx + 1] if idx + 1 < len(states) else None
        self._apply_filter()

    def action_cycle_filter_back(self) -> None:
        states = list(SessionState)
        if self._filter_state is None:
            self._filter_state = states[-1]
        else:
            idx = states.index(self._filter_state)
            self._filter_state = states[idx - 1] if idx > 0 else None
        self._apply_filter()

    def action_cycle_sort(self) -> None:
        modes = list(SortMode)
        idx = modes.index(self._sort_mode)
        self._sort_mode = modes[(idx + 1) % len(modes)]
        self._apply_filter()

    def action_cycle_sort_back(self) -> None:
        modes = list(SortMode)
        idx = modes.index(self._sort_mode)
        self._sort_mode = modes[(idx - 1) % len(modes)]
        self._apply_filter()

    def action_refresh(self) -> None:
        self._poll_sessions()
        self._poll_pr_status()

    # --- Action workers ---

    @work(thread=True)
    def _do_merge(self, session: Session) -> None:
        try:
            actions.merge_pr(session.git.git_root, session.git.branch)
            pr_num = session.pr.number if session.pr else "?"
            self.notify(f"Merged PR #{pr_num}")
        except Exception as e:
            logger.error("merge failed for %s: %s", session.name, e)
            self.notify(f"Merge failed: {e}", severity="error")
        self._poll_pr_status()

    @work(thread=True)
    def _do_approve(self, session: Session) -> None:
        try:
            actions.approve_pr(session.git.git_root, session.git.branch)
            pr_num = session.pr.number if session.pr else "?"
            self.notify(f"Approved PR #{pr_num}")
        except Exception as e:
            logger.error("approve failed for %s: %s", session.name, e)
            self.notify(f"Approve failed: {e}", severity="error")
        self._poll_pr_status()

    @work(thread=True)
    def _do_rebase_push(self, session: Session) -> None:
        self.notify(f"Rebasing {session.name}...")
        try:
            actions.rebase_and_push(session.git.git_root)
            self.notify(f"Rebased and pushed {session.name}")
        except RuntimeError as e:
            logger.error("rebase failed for %s: %s", session.name, e)
            self.notify(f"{e}", severity="error")
        except Exception as e:
            logger.error("rebase failed for %s: %s", session.name, e)
            self.notify(f"Rebase failed: {e}", severity="error")
        self._poll_sessions()

    @work(thread=True)
    def _do_cleanup(self, session: Session) -> None:
        try:
            actions.cleanup_session(
                session.name, session.pane_path,
                branch=session.git.branch, git_root=session.git.git_root,
            )
            self.notify(f"Cleaned up {session.name}")
        except Exception as e:
            logger.error("cleanup failed for %s: %s", session.name, e)
            self.notify(f"Cleanup failed: {e}", severity="error")
        self._poll_sessions()

    # --- Batch action workers ---

    @work(thread=True)
    def _do_batch_merge(self, sessions: list[Session]) -> None:
        ok, fail = 0, 0
        for s in sessions:
            try:
                actions.merge_pr(s.git.git_root, s.git.branch)
                ok += 1
            except Exception as e:
                logger.error("batch merge failed for %s: %s", s.name, e)
                fail += 1
        self.post_message(_ClearSelection())
        self.notify(f"Merged {ok} PRs" + (f", {fail} failed" if fail else ""))
        self._poll_pr_status()

    @work(thread=True)
    def _do_batch_approve(self, sessions: list[Session]) -> None:
        ok, fail = 0, 0
        for s in sessions:
            try:
                actions.approve_pr(s.git.git_root, s.git.branch)
                ok += 1
            except Exception as e:
                logger.error("batch approve failed for %s: %s", s.name, e)
                fail += 1
        self.post_message(_ClearSelection())
        self.notify(f"Approved {ok} PRs" + (f", {fail} failed" if fail else ""))
        self._poll_pr_status()

    @work(thread=True)
    def _do_batch_cleanup(self, sessions: list[Session]) -> None:
        ok, fail = 0, 0
        for s in sessions:
            try:
                actions.cleanup_session(
                    s.name, s.pane_path,
                    branch=s.git.branch, git_root=s.git.git_root,
                )
                ok += 1
            except Exception as e:
                logger.error("batch cleanup failed for %s: %s", s.name, e)
                fail += 1
        self.post_message(_ClearSelection())
        self.notify(f"Cleaned up {ok} sessions" + (f", {fail} failed" if fail else ""))
        self._poll_sessions()

    @work(thread=True)
    def _do_batch_rebase(self, sessions: list[Session]) -> None:
        self.notify(f"Rebasing {len(sessions)} branches...")
        ok, fail = 0, 0
        for s in sessions:
            try:
                actions.rebase_and_push(s.git.git_root)
                ok += 1
            except Exception as e:
                logger.error("batch rebase failed for %s: %s", s.name, e)
                fail += 1
        self.post_message(_ClearSelection())
        self.notify(f"Rebased {ok} branches" + (f", {fail} failed" if fail else ""))
        self._poll_sessions()

    @work(thread=True)
    def _do_toggle_draft(self, session: Session) -> None:
        try:
            is_draft = session.pr.is_draft if session.pr else False
            result = actions.toggle_draft(session.git.git_root, session.git.branch, is_draft)
            self.notify(f"PR #{session.pr.number}: {result}" if session.pr else result)
        except Exception as e:
            logger.error("toggle draft failed for %s: %s", session.name, e)
            self.notify(f"Draft toggle failed: {e}", severity="error")
        self._poll_pr_status()

    @work(thread=True)
    def _do_batch_toggle_draft(self, sessions: list[Session]) -> None:
        ok, fail = 0, 0
        for s in sessions:
            try:
                is_draft = s.pr.is_draft if s.pr else False
                actions.toggle_draft(s.git.git_root, s.git.branch, is_draft)
                ok += 1
            except Exception as e:
                logger.error("batch toggle draft failed for %s: %s", s.name, e)
                fail += 1
        self.post_message(_ClearSelection())
        self.notify(f"Toggled draft on {ok} PRs" + (f", {fail} failed" if fail else ""))
        self._poll_pr_status()

    @work(thread=True)
    def _do_dispatch(self, input_str: str) -> None:
        try:
            actions.dispatch(input_str)
            self.notify(f"Dispatched: {input_str}")
        except config.HookNotConfigured:
            self.notify("Dispatch hook not configured", severity="warning")
        except Exception as e:
            logger.error("dispatch failed for %r: %s", input_str, e)
            self.notify(f"Dispatch failed: {e}", severity="error")
        self._poll_sessions()


def _check_dependencies() -> None:
    """Verify required CLI tools are available."""
    import shutil
    missing = [cmd for cmd in ("tmux", "git", "gh") if not shutil.which(cmd)]
    if missing:
        raise SystemExit(f"vigil: missing required tools: {', '.join(missing)}")


def main() -> None:
    import sys
    if "--help" in sys.argv or "-h" in sys.argv:
        print("vigil — TUI mission control for tmux sessions")
        print()
        print("Usage: vigil [--help]")
        print()
        print("Config: ~/.config/vigil/config.toml")
        print("Logs:   ~/.local/share/vigil/vigil.log")
        raise SystemExit(0)
    from . import logging_config
    config.load_config()
    logging_config.configure(config.get_setting("log_level"))
    _check_dependencies()
    app = VigilApp()
    app.run()


if __name__ == "__main__":
    main()

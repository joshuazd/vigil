# Generalize Remaining Workflow-Specific Code

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove remaining hardcoded assumptions so vigil works out-of-the-box for any tmux user, not just the original author's setup.

**Architecture:** Four independent fixes: (1) tmux `window_index` filter uses `base-index`-aware first-window detection, (2) remaining "Shortcut" references replaced with generic text, (3) `--help` flag added to bootstrap script + entry point, (4) README config examples use generic commands.

**Tech Stack:** Python 3.10+, tmux CLI, argparse (stdlib)

---

## File Structure

| File | Change | Responsibility |
|------|--------|---------------|
| `src/vigil/tmux.py:23-27` | Modify | Fix `window_index` filter to use `#{session_base_window}` |
| `src/vigil/widgets.py:219` | Modify | Remove "Shortcut" from docstring |
| `README.md:32,51-53` | Modify | Generic dispatch description, generic hook examples |
| `src/vigil/app.py:435-441` | Modify | Add `--help` support |
| `vigil:19` | Modify | Pass `--help` through without bootstrapping venv |
| `tests/test_tmux.py:23` | Modify | Update mock data for first-window filter |
| `tests/test_app.py` | Modify | Test `--help` exits cleanly |

---

### Task 1: Fix tmux window_index filter for non-default base-index

The `list_sessions()` filter hardcodes `#{==:#{window_index},1}` which only works when `base-index` is 1. Default tmux uses 0. This means vigil shows zero sessions for most users.

**Files:**
- Modify: `src/vigil/tmux.py:23-27`
- Test: `tests/test_tmux.py`

- [ ] **Step 1: Write failing test**

In `tests/test_tmux.py`, the existing `TestListSessions` mock data will still pass because it doesn't validate the filter string. Add a test that checks the tmux command includes `window_first_flag` instead of a hardcoded index:

```python
# In TestListSessions class, add:
@patch("vigil.tmux.subprocess.run")
def test_filter_uses_window_first_flag(self, mock_run):
    mock_run.return_value = MagicMock(returncode=0, stdout="100|sess|/tmp\n")
    list_sessions()
    cmd = mock_run.call_args.args[0]
    filter_arg = cmd[cmd.index("-f") + 1]
    assert "window_first_flag" in filter_arg
    assert "window_index" not in filter_arg
```

- [ ] **Step 2: Run test to verify it fails**

Run: `source ~/.local/share/vigil/venv/bin/activate && pytest tests/test_tmux.py::TestListSessions::test_filter_uses_window_first_flag -v`
Expected: FAIL — current code uses `window_index`

- [ ] **Step 3: Fix the filter**

In `src/vigil/tmux.py`, change line 26 from:

```python
"-f", "#{&&:#{==:#{window_index},1},#{==:#{pane_index},1}}",
```

to:

```python
"-f", "#{&&:#{window_first_flag},#{==:#{pane_index},0}}",
```

`window_first_flag` is 1 for the first window regardless of `base-index`. `pane_index` is always 0-based.

- [ ] **Step 4: Run tests to verify pass**

Run: `source ~/.local/share/vigil/venv/bin/activate && pytest tests/test_tmux.py -v`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add src/vigil/tmux.py tests/test_tmux.py
git commit -m "Fix session list for default tmux base-index

Use window_first_flag instead of hardcoded window_index=1 so vigil
works regardless of the user's base-index setting."
```

---

### Task 2: Remove remaining "Shortcut" references

**Files:**
- Modify: `src/vigil/widgets.py:219`
- Modify: `README.md:32,51-53`

- [ ] **Step 1: Fix widgets.py docstring**

In `src/vigil/widgets.py`, change line 219 from:

```python
"""Input for dispatching a Shortcut URL or PR number."""
```

to:

```python
"""Input for dispatching a URL or identifier to an external command."""
```

- [ ] **Step 2: Fix README keybindings table**

In `README.md`, change line 32 from:

```markdown
| `d` | Dispatch (Shortcut URL or PR number) |
```

to:

```markdown
| `d` | Dispatch (run configured hook with input) |
```

- [ ] **Step 3: Fix README config examples**

In `README.md`, change the hook examples (lines 51-53) from:

```toml
cleanup = "git-worktree-cleanup --session {session} {path}"
dispatch = "dispatch --detached --non-interactive {input}"
```

to:

```toml
cleanup = "tmux kill-session -t {session} && git worktree remove {path}"
dispatch = "my-dispatch-script {input}"
```

- [ ] **Step 4: Run linter**

Run: `source ~/.local/share/vigil/venv/bin/activate && ruff check src/ tests/`
Expected: All checks passed

- [ ] **Step 5: Commit**

```bash
git add src/vigil/widgets.py README.md
git commit -m "Remove Shortcut-specific references from docs and code"
```

---

### Task 3: Add --help flag

Currently `./vigil --help` bootstraps a venv then launches the TUI with no output. Add basic help.

**Files:**
- Modify: `vigil` (bootstrap script)
- Modify: `src/vigil/app.py:435-441`
- Test: `tests/test_app.py`

- [ ] **Step 1: Add --help passthrough in bootstrap script**

In `vigil`, add before the venv check (after `set -euo pipefail`):

```bash
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    echo "vigil — TUI mission control for tmux sessions"
    echo ""
    echo "Usage: vigil [--help]"
    echo ""
    echo "Config: ~/.config/vigil/config.toml"
    echo "Logs:   ~/.local/share/vigil/vigil.log"
    echo "Docs:   https://github.com/$(cd "$(dirname "$(readlink -f "$0")")" && basename "$(git remote get-url origin 2>/dev/null | sed 's/.*github.com[:/]//' | sed 's/.git$//')" 2>/dev/null || echo "vigil")"
    exit 0
fi
```

Actually, that git URL extraction is fragile. Keep it simple:

```bash
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    echo "vigil — TUI mission control for tmux sessions"
    echo ""
    echo "Usage: vigil [--help]"
    echo ""
    echo "Config: ~/.config/vigil/config.toml"
    echo "Logs:   ~/.local/share/vigil/vigil.log"
    exit 0
fi
```

- [ ] **Step 2: Add --help in Python entry point**

In `src/vigil/app.py`, update `main()`:

```python
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
```

- [ ] **Step 3: Write test**

In `tests/test_app.py`, add:

```python
class TestHelpFlag:
    def test_help_exits_cleanly(self):
        import sys
        with patch.object(sys, "argv", ["vigil", "--help"]):
            with pytest.raises(SystemExit) as exc_info:
                from vigil.app import main
                main()
            assert exc_info.value.code == 0
```

- [ ] **Step 4: Run tests**

Run: `source ~/.local/share/vigil/venv/bin/activate && pytest tests/test_app.py -v`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add vigil src/vigil/app.py tests/test_app.py
git commit -m "Add --help flag to bootstrap script and entry point"
```

---

### Task 4: Final verification

- [ ] **Step 1: Run full test suite**

Run: `source ~/.local/share/vigil/venv/bin/activate && pytest tests/ -v --cov=vigil`
Expected: all pass

- [ ] **Step 2: Run lint and types**

Run: `source ~/.local/share/vigil/venv/bin/activate && ruff check src/ tests/ && mypy src/ --ignore-missing-imports`
Expected: no errors

- [ ] **Step 3: Manual smoke test**

Run: `./vigil --help`
Expected: prints help text, exits

Run: `./vigil`
Expected: TUI launches, sessions visible (if tmux is running)

- [ ] **Step 4: Commit and push**

```bash
git push
```

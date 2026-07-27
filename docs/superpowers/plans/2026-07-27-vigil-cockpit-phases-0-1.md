# Vigil Cockpit Phases 0-1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `send-keys` race from the Claude launch path, then stand up a state daemon inside the vigil binary that produces no visible change.

**Architecture:** Phase 0 is three edits in the dotfiles scripts package plus its first test harness: Claude becomes the tmux pane's own process via `respawn-pane` instead of text typed into a shell, and its system prompt moves from a `%q`-quoted command-line blob into a file. Phase 1 extracts vigil's polling into a reusable collector, wraps it in a Unix-socket server behind a `vigil daemon` subcommand, and teaches the TUI to consume snapshots from that socket while keeping its existing self-polling path as a permanent fallback.

**Tech Stack:** bash + bats-core + shellcheck (dotfiles); Go 1.26.1, Bubble Tea 1.3.10, `encoding/json`, `net` (vigil).

**Spec:** `~/vigil/docs/superpowers/specs/2026-07-27-vigil-cockpit-design.md`

## Global Constraints

- Two repositories. Tasks 1-4 are in `~/dotfiles` (package `scripts/scripts`). Tasks 5-9 are in `~/vigil`. Never mix the two in one commit.
- Go module path is `github.com/jzinkduda/vigil`. Go version floor `1.26.1`.
- Add no new Go dependencies. Everything in phase 1 uses the standard library.
- All new bash follows the existing conventions: `set -o errexit -o nounset -o pipefail`, `readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"`, `while [ "${#}" -gt 0 ]` / `case` argument parsing, source guards in `lib/`.
- Never use em dashes in code, comments, commit messages, or docs. Use a plain dash.
- Prefer no comments. Comment only where the code's meaning cannot be inferred from reading it.
- Phase 1 must produce **no visible behavior change**. Task 9 has an explicit equivalence gate.
- Every task ends with a commit. Use conventional-commit prefixes (`feat:`, `fix:`, `test:`, `refactor:`) matching the existing history in both repos.
- Do not use heredoc syntax (`<<EOF`) in Bash tool calls when implementing. Use the Write tool for files and `<<<` for stdin.

---

# Phase 0: Remove the send-keys race (`~/dotfiles`)

### Task 1: Test harness for the scripts package

The scripts package has no test system today. Phase 0 modifies shell functions that issue tmux commands, so it needs a way to assert on those commands. This task delivers that harness and proves it works against an existing pure function.

**Files:**
- Create: `scripts/scripts/tests/helper.bash`
- Create: `scripts/scripts/tests/stubs/tmux`
- Create: `scripts/scripts/tests/tmux_lib.bats`
- Create: `scripts/scripts/Makefile`
- Create: `.github/workflows/test-scripts.yml`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `tests/helper.bash` exports `setup_tmux_stub()` which prepends `tests/stubs` to `PATH` and sets `TMUX_STUB_LOG` to a fresh temp file, and `tmux_calls()` which prints the recorded log to stdout.
  - `make test` in `scripts/scripts` runs `bats tests/`.
  - The stub records one line per invocation: the literal argv joined by `\x1f` (unit separator), so arguments containing spaces stay distinguishable.

- [ ] **Step 1: Write the failing test**

Create `scripts/scripts/tests/tmux_lib.bats`:

```bash
#!/usr/bin/env bats

load helper

setup() {
  setup_tmux_stub
  source "${BATS_TEST_DIRNAME}/../lib/tmux.sh"
}

@test "session_name_from_title strips tmux-unsafe characters" {
  run session_name_from_title "SC" "12345" "Emit metrics: for v1.2 'now'"
  [ "${status}" -eq 0 ]
  [ "${output}" = "SC-12345 Emit metrics for v12 now" ]
}

@test "session_name_from_title truncates at a word boundary" {
  run session_name_from_title "SC" "12345" "Refactor the entire authentication subsystem end to end"
  [ "${status}" -eq 0 ]
  [ "${#output}" -le 50 ]
  [[ "${output}" != *" " ]]
}

@test "tmux stub records invocations" {
  tmux display-message -p "hello world"
  run tmux_calls
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"display-message"* ]]
  [[ "${output}" == *"hello world"* ]]
}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
brew install bats-core
cd ~/dotfiles/scripts/scripts && bats tests/
```

Expected: FAIL. `helper.bash` does not exist, so `load helper` errors before any test body runs.

- [ ] **Step 3: Write the tmux stub**

Create `scripts/scripts/tests/stubs/tmux`:

```bash
#!/usr/bin/env bash
# Test stub. Records argv to $TMUX_STUB_LOG, one invocation per line,
# arguments joined by the unit separator so embedded spaces stay parseable.
set -o nounset

if [ -n "${TMUX_STUB_LOG:-}" ]; then
  printf '%s' "${1:-}" >> "${TMUX_STUB_LOG}"
  shift || true
  for arg in "${@}"; do
    printf '\x1f%s' "${arg}" >> "${TMUX_STUB_LOG}"
  done
  printf '\n' >> "${TMUX_STUB_LOG}"
fi

# Canned responses for the queries lib/tmux.sh makes.
case "${1:-}" in
  display-message)
    printf '%s\n' "${TMUX_STUB_DISPLAY:-/tmp/stub-worktree}"
    ;;
  has-session)
    exit "${TMUX_STUB_HAS_SESSION:-1}"
    ;;
esac

exit 0
```

Make it executable: `chmod +x scripts/scripts/tests/stubs/tmux`

- [ ] **Step 4: Write the helper**

Create `scripts/scripts/tests/helper.bash`:

```bash
setup_tmux_stub() {
  export TMUX_STUB_LOG="${BATS_TEST_TMPDIR}/tmux-calls.log"
  : > "${TMUX_STUB_LOG}"
  export PATH="${BATS_TEST_DIRNAME}/stubs:${PATH}"
  export TMUX="fake-socket,0,0"
}

tmux_calls() {
  cat "${TMUX_STUB_LOG}"
}

# Assert that no recorded invocation starts with the given tmux subcommand.
refute_tmux_subcommand() {
  local subcommand="${1}"
  ! grep -q "^${subcommand}\x1f" "${TMUX_STUB_LOG}"
}

# Print the full argv of the first invocation of the given subcommand,
# with the unit separators replaced by newlines.
tmux_call_args() {
  local subcommand="${1}"
  grep -m1 "^${subcommand}\x1f" "${TMUX_STUB_LOG}" | tr '\x1f' '\n'
}
```

- [ ] **Step 5: Run the tests to confirm they pass**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/
```

Expected: 3 tests, all PASS.

- [ ] **Step 6: Add the make target**

Create `scripts/scripts/Makefile`:

```make
.PHONY: test lint

test:
	bats tests/

lint:
	shellcheck common.sh lib/*.sh dispatch dispatch-from-chrome gh-review \
		shortcut-implement git-worktree-session git-worktree-new
```

- [ ] **Step 7: Add CI**

Create `.github/workflows/test-scripts.yml`, matching the style of the sibling workflows:

```yaml
name: Scripts

on:
  push:
    branches: [ main, master ]
  pull_request:
    branches: [ main, master ]

jobs:
  test-scripts:
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]

    steps:
    - name: Checkout repository
      uses: actions/checkout@v4

    - name: Install bats
      run: |
        if [ "$RUNNER_OS" == "Linux" ]; then
          sudo apt-get update && sudo apt-get install -y bats
        else
          brew install bats-core
        fi

    - name: Run tests
      working-directory: scripts/scripts
      run: bats tests/
```

- [ ] **Step 8: Verify CI config is valid and tests still pass**

```bash
cd ~/dotfiles/scripts/scripts && make test
```

Expected: 3 tests PASS.

- [ ] **Step 9: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/tests scripts/scripts/Makefile .github/workflows/test-scripts.yml
git commit -m "test(scripts): add bats harness with tmux argv-recording stub"
```

---

### Task 2: Launch Claude via respawn-pane instead of send-keys

`create_tmux_session` currently types the Claude command into a freshly created session at `lib/tmux.sh:128`, racing the shell's readiness. Replace that with `respawn-pane -k`, which makes Claude the pane's process directly.

**Files:**
- Modify: `scripts/scripts/lib/tmux.sh:99-143` (`create_tmux_session`)
- Modify: `scripts/scripts/tests/tmux_lib.bats` (add tests)

**Interfaces:**
- Consumes: `setup_tmux_stub`, `tmux_calls`, `refute_tmux_subcommand`, `tmux_call_args` from Task 1's `tests/helper.bash`.
- Produces: `launch_claude_in_pane <session_name> <session_dir> <command>` in `lib/tmux.sh`. Issues exactly one `tmux respawn-pane -k -t "=<session>:claude.1" -c <dir> "<command>; exec \"${SHELL}\""`. Tasks 3 and 4 call it.

- [ ] **Step 1: Write the failing tests**

Append to `scripts/scripts/tests/tmux_lib.bats`:

```bash
@test "launch_claude_in_pane respawns the pane instead of sending keys" {
  launch_claude_in_pane "SC-1 demo" "/tmp/wt" "claude --model opus"
  run refute_tmux_subcommand "send-keys"
  [ "${status}" -eq 0 ]
  run tmux_call_args "respawn-pane"
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"-k"* ]]
  [[ "${output}" == *"=SC-1 demo:claude.1"* ]]
  [[ "${output}" == *"/tmp/wt"* ]]
}

@test "launch_claude_in_pane keeps a shell alive after claude exits" {
  launch_claude_in_pane "SC-1 demo" "/tmp/wt" "claude --model opus"
  run tmux_call_args "respawn-pane"
  [[ "${output}" == *"claude --model opus; exec "* ]]
}

@test "create_tmux_session launches claude without send-keys" {
  create_tmux_session "SC-1 demo" "/tmp/wt" true "" "claude --model opus"
  run refute_tmux_subcommand "send-keys"
  [ "${status}" -eq 0 ]
  run tmux_call_args "respawn-pane"
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"claude --model opus"* ]]
}

@test "create_tmux_session with no claude command does not respawn" {
  create_tmux_session "SC-1 demo" "/tmp/wt" true ""
  run refute_tmux_subcommand "respawn-pane"
  [ "${status}" -eq 0 ]
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats
```

Expected: the first three new tests FAIL. `launch_claude_in_pane` is undefined, and `create_tmux_session` still calls `send-keys`, so `refute_tmux_subcommand` returns non-zero.

- [ ] **Step 3: Add `launch_claude_in_pane`**

Insert into `scripts/scripts/lib/tmux.sh` immediately before `create_tmux_session` (before line 99):

```bash
#######################################
# Replace the claude pane's process with the given command.
# Uses respawn-pane rather than send-keys so there is no shell-readiness race
# and the command never passes through a shell prompt. Appends an exec of the
# login shell so exiting Claude leaves a usable pane instead of collapsing it.
# Arguments:
#   session_name - tmux session name
#   session_dir  - working directory for the pane
#   command      - command to run
#######################################
launch_claude_in_pane() {
  local session_name="${1}"
  local session_dir="${2}"
  local command="${3}"

  tmux respawn-pane -k -t "=${session_name}:claude.1" -c "${session_dir}" \
    "${command}; exec \"\${SHELL}\""
}
```

- [ ] **Step 4: Replace the send-keys call in `create_tmux_session`**

In `scripts/scripts/lib/tmux.sh`, replace lines 127-129:

```bash
  if [ -n "${claude_command}" ]; then
    tmux send-keys -t "=${session_name}:claude.1" "${claude_command}" Enter
  fi
```

with:

```bash
  if [ -n "${claude_command}" ]; then
    launch_claude_in_pane "${session_name}" "${session_dir}" "${claude_command}"
  fi
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
cd ~/dotfiles/scripts/scripts && make test
```

Expected: 7 tests PASS.

- [ ] **Step 6: Confirm the secondary-pane send-keys is untouched**

`setup_secondary_pane` at `lib/tmux.sh:83` also uses `send-keys`, for the `nit` / `tuicr` side pane. That one stays: it is a short single-token command with no prompt payload, and the pane is created empty immediately before. Verify it is still there:

```bash
cd ~/dotfiles/scripts/scripts && grep -n "send-keys" lib/tmux.sh
```

Expected: exactly one hit, at the `setup_secondary_pane` line.

- [ ] **Step 7: Lint**

```bash
cd ~/dotfiles/scripts/scripts && shellcheck lib/tmux.sh
```

Expected: no output.

- [ ] **Step 8: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/tests/tmux_lib.bats
git commit -m "fix(scripts): launch claude via respawn-pane, not send-keys

send-keys typed the launch command into a freshly created session, racing
the shell's readiness. respawn-pane makes claude the pane's own process."
```

---

### Task 3: Move the system prompt out of the command line

`claude_launch_cmd` at `lib/route.sh:500` `printf '%q'`-quotes a multi-line routing hint plus execution-default block straight into the command string. Write it to a file instead and read it back with `$(cat ...)`.

**Files:**
- Modify: `scripts/scripts/lib/route.sh:500-534` (`claude_launch_cmd`)
- Create: `scripts/scripts/tests/route_lib.bats`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `claude_launch_cmd <tier> <reasoning> <rationale> <slash_command> <extra_system_block> [extra_flags...]` gains one new behavior, controlled by the environment variable `CLAUDE_PROMPT_FILE`. When it is set and non-empty, the function writes the assembled system prompt to that path and emits `--append-system-prompt "$(cat <quoted-path>)"`. When it is unset, behavior is byte-for-byte what it is today. Task 4 sets the variable.

- [ ] **Step 1: Write the failing tests**

Create `scripts/scripts/tests/route_lib.bats`:

```bash
#!/usr/bin/env bats

load helper

setup() {
  source "${BATS_TEST_DIRNAME}/../lib/route.sh"
}

@test "claude_launch_cmd inlines the prompt when no prompt file is set" {
  run claude_launch_cmd "opus" "high" "because" "/implement 1" ""
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"--append-system-prompt"* ]]
  [[ "${output}" == *"routing-hint"* ]]
  [[ "${output}" != *'$(cat '* ]]
}

@test "claude_launch_cmd writes the prompt to CLAUDE_PROMPT_FILE and reads it back" {
  export CLAUDE_PROMPT_FILE="${BATS_TEST_TMPDIR}/prompt.txt"
  run claude_launch_cmd "opus" "high" "because" "/implement 1" ""
  [ "${status}" -eq 0 ]
  [[ "${output}" == *'--append-system-prompt "$(cat '* ]]
  [[ "${output}" == *"prompt.txt"* ]]
  [[ "${output}" != *"routing-hint"* ]]
  [ -f "${CLAUDE_PROMPT_FILE}" ]
  grep -q "routing-hint" "${CLAUDE_PROMPT_FILE}"
}

@test "claude_launch_cmd writes the extra system block to the prompt file" {
  export CLAUDE_PROMPT_FILE="${BATS_TEST_TMPDIR}/prompt.txt"
  run claude_launch_cmd "opus" "high" "because" "/implement 1" "<execution-default>
multi
line
</execution-default>"
  [ "${status}" -eq 0 ]
  grep -q "execution-default" "${CLAUDE_PROMPT_FILE}"
  grep -q "^multi$" "${CLAUDE_PROMPT_FILE}"
  grep -q "^line$" "${CLAUDE_PROMPT_FILE}"
}

@test "claude_launch_cmd still quotes the model id and slash command" {
  export CLAUDE_PROMPT_FILE="${BATS_TEST_TMPDIR}/prompt.txt"
  run claude_launch_cmd "opus" "high" "because" "/implement 1" ""
  [[ "${output}" == *"--effort high"* ]]
  [[ "${output}" == *"/implement 1"* ]]
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/route_lib.bats
```

Expected: tests 2 and 3 FAIL. `claude_launch_cmd` ignores `CLAUDE_PROMPT_FILE`, so no file is written and the output still contains `routing-hint`.

- [ ] **Step 3: Implement the prompt-file branch**

In `scripts/scripts/lib/route.sh`, replace lines 522-531:

```bash
  local hint_quoted
  hint_quoted="$(printf '%q' "${hint}")"

  # Quote the model arg: ID may contain [1m] which is a bash glob pattern.
  local cmd="claude --model $(printf '%q' "${model}") --effort ${reasoning}"
  local flag
  for flag in "${extra_flags[@]}"; do
    cmd+=" $(printf '%q' "${flag}")"
  done
  cmd+=" --append-system-prompt ${hint_quoted} -- $(printf '%q' "${slash_command}")"
```

with:

```bash
  # A multi-line prompt quoted into the command string has to survive tmux
  # argument parsing. Writing it to a file keeps it out of the command line.
  local system_prompt_arg
  if [ -n "${CLAUDE_PROMPT_FILE:-}" ]; then
    printf '%s' "${hint}" > "${CLAUDE_PROMPT_FILE}"
    system_prompt_arg="\"\$(cat $(printf '%q' "${CLAUDE_PROMPT_FILE}"))\""
  else
    system_prompt_arg="$(printf '%q' "${hint}")"
  fi

  # Quote the model arg: ID may contain [1m] which is a bash glob pattern.
  local cmd="claude --model $(printf '%q' "${model}") --effort ${reasoning}"
  local flag
  for flag in "${extra_flags[@]}"; do
    cmd+=" $(printf '%q' "${flag}")"
  done
  cmd+=" --append-system-prompt ${system_prompt_arg} -- $(printf '%q' "${slash_command}")"
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/route_lib.bats
```

Expected: 4 tests PASS.

- [ ] **Step 5: Run the whole suite and lint**

```bash
cd ~/dotfiles/scripts/scripts && make test && shellcheck lib/route.sh
```

Expected: 11 tests PASS, no shellcheck output.

- [ ] **Step 6: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/route.sh scripts/scripts/tests/route_lib.bats
git commit -m "feat(scripts): write claude system prompt to a file

A %q-quoted multi-line prompt in the command string has to survive tmux
argument parsing. CLAUDE_PROMPT_FILE moves it to disk."
```

---

### Task 4: Wire shortcut-implement and gh-review to the race-free path

Both scripts build `launch_cmd` after `run_worktree_popup` returns and then `send-keys` it. Point them at `launch_claude_in_pane` and give them a prompt file inside the worktree's private git directory.

**Files:**
- Modify: `scripts/scripts/lib/tmux.sh` (add `worktree_prompt_file`)
- Modify: `scripts/scripts/shortcut-implement:204-208`
- Modify: `scripts/scripts/gh-review:191-195`
- Modify: `scripts/scripts/tests/tmux_lib.bats` (add tests)

**Interfaces:**
- Consumes: `launch_claude_in_pane` (Task 2), the `CLAUDE_PROMPT_FILE` contract (Task 3), `setup_tmux_stub` / `tmux_call_args` (Task 1).
- Produces: `worktree_prompt_file <session_name>` in `lib/tmux.sh`. Prints the absolute path `<worktree-git-dir>/vigil-launch-prompt.txt`, resolving the worktree from the session's pane path. Returns 1 and prints nothing if the session or git dir cannot be resolved.

- [ ] **Step 1: Write the failing tests**

Append to `scripts/scripts/tests/tmux_lib.bats`:

```bash
@test "worktree_prompt_file resolves under the worktree git dir" {
  local wt="${BATS_TEST_TMPDIR}/wt"
  mkdir -p "${wt}"
  git -C "${wt}" init --quiet
  export TMUX_STUB_DISPLAY="${wt}"

  run worktree_prompt_file "SC-1 demo"
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"/vigil-launch-prompt.txt" ]]
  [ -d "$(dirname "${output}")" ]
}

@test "worktree_prompt_file fails when the pane path is not a git dir" {
  export TMUX_STUB_DISPLAY="${BATS_TEST_TMPDIR}/not-a-repo"
  mkdir -p "${TMUX_STUB_DISPLAY}"

  run worktree_prompt_file "SC-1 demo"
  [ "${status}" -ne 0 ]
  [ -z "${output}" ]
}

@test "worktree_prompt_file queries the claude window pane path" {
  local wt="${BATS_TEST_TMPDIR}/wt2"
  mkdir -p "${wt}"
  git -C "${wt}" init --quiet
  export TMUX_STUB_DISPLAY="${wt}"

  worktree_prompt_file "SC-1 demo"
  run tmux_call_args "display-message"
  [[ "${output}" == *"=SC-1 demo:claude"* ]]
  [[ "${output}" == *"pane_current_path"* ]]
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats
```

Expected: all three new tests FAIL with "command not found: worktree_prompt_file".

- [ ] **Step 3: Add `worktree_prompt_file`**

Append to `scripts/scripts/lib/tmux.sh`:

```bash
#######################################
# Print the path to a session's Claude launch-prompt file.
# Lives in the worktree's private git directory so it never shows up in
# git status, cannot be committed, and is removed with the worktree.
# Arguments:
#   session_name - tmux session name
# Outputs:
#   Writes the absolute file path to stdout
# Returns:
#   0 on success, 1 if the session or its git dir cannot be resolved
#######################################
worktree_prompt_file() {
  local session_name="${1}"
  local pane_path git_dir

  pane_path="$(tmux display-message -p -t "=${session_name}:claude" \
    '#{pane_current_path}' 2>/dev/null)" || return 1
  [ -n "${pane_path}" ] || return 1

  git_dir="$(git -C "${pane_path}" rev-parse --absolute-git-dir 2>/dev/null)" || return 1
  [ -n "${git_dir}" ] || return 1

  printf '%s/vigil-launch-prompt.txt' "${git_dir}"
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats
```

Expected: 10 tests PASS.

- [ ] **Step 5: Rewire shortcut-implement**

In `scripts/scripts/shortcut-implement`, replace lines 204-208:

```bash
  local launch_cmd
  launch_cmd="$(claude_launch_cmd \
    "${route_model}" "${route_reasoning}" "${route_rationale}" \
    "/implement ${story_id}" "${execution_default}" "${extra_flags[@]}")"
  tmux send-keys -t "=${session_name}:claude" "${launch_cmd}" Enter
```

with:

```bash
  local launch_cmd session_dir
  session_dir="$(tmux display-message -p -t "=${session_name}:claude" '#{pane_current_path}')"
  CLAUDE_PROMPT_FILE="$(worktree_prompt_file "${session_name}" || true)" \
    launch_cmd="$(claude_launch_cmd \
      "${route_model}" "${route_reasoning}" "${route_rationale}" \
      "/implement ${story_id}" "${execution_default}" "${extra_flags[@]}")"
  launch_claude_in_pane "${session_name}" "${session_dir}" "${launch_cmd}"
```

- [ ] **Step 6: Rewire gh-review**

In `scripts/scripts/gh-review`, replace lines 191-195:

```bash
  local launch_cmd
  launch_cmd="$(claude_launch_cmd \
    "${route_model}" "${route_reasoning}" "${route_rationale}" \
    "/review-pr ${pr_input}" "")"
  tmux send-keys -t "=${session_name}:claude" "${launch_cmd}" Enter
```

with:

```bash
  local launch_cmd session_dir
  session_dir="$(tmux display-message -p -t "=${session_name}:claude" '#{pane_current_path}')"
  CLAUDE_PROMPT_FILE="$(worktree_prompt_file "${session_name}" || true)" \
    launch_cmd="$(claude_launch_cmd \
      "${route_model}" "${route_reasoning}" "${route_rationale}" \
      "/review-pr ${pr_input}" "")"
  launch_claude_in_pane "${session_name}" "${session_dir}" "${launch_cmd}"
```

- [ ] **Step 7: Confirm no send-keys of a launch command remains**

```bash
cd ~/dotfiles/scripts/scripts && grep -n "send-keys" *.sh lib/*.sh shortcut-implement gh-review git-worktree-session
```

Expected: exactly one hit, the `setup_secondary_pane` line in `lib/tmux.sh`.

- [ ] **Step 8: Full suite and lint**

```bash
cd ~/dotfiles/scripts/scripts && make test && make lint
```

Expected: 14 tests PASS, no shellcheck output.

- [ ] **Step 9: Manual end-to-end check**

This is the gate for phase 0. Automated tests use a tmux stub, so a real run is required.

```bash
cd <a real repo with a Shortcut story> && ~/scripts/dispatch sc-<some-real-story>
```

Confirm: a session is created, Claude starts in the `claude` window with the right model and effort, the prompt file exists at `<worktree>/.git/../vigil-launch-prompt.txt` (find it with `git -C <worktree> rev-parse --absolute-git-dir`), and `git -C <worktree> status --short` does **not** list it. Then quit Claude and confirm the pane drops to a shell rather than closing.

- [ ] **Step 10: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/shortcut-implement \
  scripts/scripts/gh-review scripts/scripts/tests/tmux_lib.bats
git commit -m "fix(scripts): launch claude race-free from implement and review

Both scripts now respawn the claude pane instead of typing the launch
command into it, and pass the system prompt via a file in the worktree's
private git dir."
```

---

# Phase 1: The invisible daemon (`~/vigil`)

### Task 5: Extract polling into a reusable collector

`model.go` owns the polling logic inside Bubble Tea commands (`fetchTmuxCmd` at :755, `fetchGitCmd` at :795, `fetchPRsCmd` at :838). The daemon needs the same logic without Bubble Tea. Extract it.

**Files:**
- Create: `internal/collect/collect.go`
- Create: `internal/collect/collect_test.go`

**Interfaces:**
- Consumes: `fetch.ListSessions`, `fetch.BellFlags`, `fetch.FetchGitStatus`, `fetch.FetchPRStatus`, `fetch.Commander` (all existing); `session.Session`, `session.GitStatus`, `session.PRStatus`; `config.Config.GetSettingInt`.
- Produces:

```go
package collect

// Collector gathers full session state without any UI dependency.
type Collector struct {
    Cmd        fetch.Commander
    GitWorkers int
}

// New returns a Collector configured from cfg.
func New(cfg *config.Config, cmd fetch.Commander) *Collector

// Snapshot returns every tmux session with git and PR state populated.
// Sessions whose git status cannot be read are still returned, with a
// zero GitStatus. A nil error means the tmux enumeration succeeded; per
// session failures are not errors.
func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error)
```

Tasks 7 and 9 both call `Snapshot`.

- [ ] **Step 1: Write the failing test**

Create `internal/collect/collect_test.go`:

```go
package collect

import (
	"context"
	"testing"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

func TestSnapshotPopulatesSessionsWithGitState(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha\n1700000001|beta|/tmp/beta", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}",
		"alpha|1\nbeta|0", nil)
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if sessions[0].Name != "alpha" {
		t.Errorf("got name %q, want alpha", sessions[0].Name)
	}
	if !sessions[0].HasBell {
		t.Error("alpha should have a bell flag")
	}
	if sessions[1].HasBell {
		t.Error("beta should not have a bell flag")
	}
	if sessions[0].PanePath != "/tmp/alpha" {
		t.Errorf("got pane path %q, want /tmp/alpha", sessions[0].PanePath)
	}
}

func TestSnapshotReturnsErrorWhenTmuxFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", context.DeadlineExceeded)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("want error when tmux enumeration fails")
	}
}

func TestSnapshotWithNoSessionsReturnsEmpty(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}", "", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)

	c := New(&config.Config{}, cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(sessions))
	}
}

func TestNewDefaultsGitWorkers(t *testing.T) {
	c := New(&config.Config{}, fetch.NewMockCommander())
	if c.GitWorkers != 8 {
		t.Errorf("got %d git workers, want 8", c.GitWorkers)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/vigil && go test ./internal/collect/
```

Expected: FAIL, the package does not exist.

- [ ] **Step 3: Implement the collector**

Create `internal/collect/collect.go`:

```go
package collect

import (
	"context"
	"sync"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

const defaultGitWorkers = 8

type Collector struct {
	Cmd        fetch.Commander
	GitWorkers int
}

func New(cfg *config.Config, cmd fetch.Commander) *Collector {
	workers := cfg.GetSettingInt("git_workers")
	if workers <= 0 {
		workers = defaultGitWorkers
	}
	return &Collector{Cmd: cmd, GitWorkers: workers}
}

func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error) {
	raw, err := fetch.ListSessions(ctx, c.Cmd)
	if err != nil {
		return nil, err
	}

	bells := fetch.BellFlags(ctx, c.Cmd)

	sessions := make([]*session.Session, len(raw))
	for i, r := range raw {
		sessions[i] = &session.Session{
			Name:     r.Name,
			PanePath: r.PanePath,
			Created:  r.Created,
			HasBell:  bells[r.Name],
		}
	}

	c.fillGit(ctx, sessions)
	c.fillPRs(ctx, sessions)
	return sessions, nil
}

func (c *Collector) fillGit(ctx context.Context, sessions []*session.Session) {
	sem := make(chan struct{}, c.GitWorkers)
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.Git = fetch.FetchGitStatus(ctx, c.Cmd, s.PanePath)
		}(s)
	}
	wg.Wait()
}

func (c *Collector) fillPRs(ctx context.Context, sessions []*session.Session) {
	sem := make(chan struct{}, c.GitWorkers)
	var wg sync.WaitGroup
	for _, s := range sessions {
		if s.Git.Branch == "" || s.Git.GitRoot == "" {
			continue
		}
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.PR = fetch.FetchPRStatus(ctx, c.Cmd, s.Git.Branch, s.Git.GitRoot)
		}(s)
	}
	wg.Wait()
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/vigil && go test ./internal/collect/ -v
```

Expected: 4 tests PASS.

- [ ] **Step 5: Check for data races**

The collector writes `s.Git` and `s.PR` from goroutines. Confirm the ownership is clean:

```bash
cd ~/vigil && go test -race ./internal/collect/
```

Expected: PASS with no race reports.

- [ ] **Step 6: Lint and commit**

```bash
cd ~/vigil && make lint && git add internal/collect && \
  git commit -m "feat(collect): add UI-independent session state collector"
```

---

### Task 6: The wire protocol

**Files:**
- Create: `internal/protocol/protocol.go`
- Create: `internal/protocol/protocol_test.go`

**Interfaces:**
- Consumes: `session.Session`.
- Produces:

```go
package protocol

const Version = 1

// SocketPath returns the daemon socket path. Prefers $XDG_RUNTIME_DIR,
// falling back to ~/.local/state/vigil.
func SocketPath() string

type Snapshot struct {
    Version   int                `json:"version"`
    Timestamp int64              `json:"timestamp"`
    Sessions  []*session.Session `json:"sessions"`
}

// Encode writes one newline-delimited JSON snapshot to w.
func Encode(w io.Writer, snap *Snapshot) error

// Decoder reads newline-delimited snapshots from r.
type Decoder struct{ ... }
func NewDecoder(r io.Reader) *Decoder

// Next returns the next snapshot, or io.EOF when the stream ends.
// Returns ErrVersionMismatch for a snapshot this build cannot read.
func (d *Decoder) Next() (*Snapshot, error)

var ErrVersionMismatch = errors.New("protocol version mismatch")
```

Tasks 7 and 9 both use these.

- [ ] **Step 1: Write the failing test**

Create `internal/protocol/protocol_test.go`:

```go
package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	snap := &Snapshot{
		Version:   Version,
		Timestamp: 1700000000,
		Sessions: []*session.Session{
			{Name: "alpha", PanePath: "/tmp/alpha", HasBell: true,
				Git: session.GitStatus{Branch: "main", Modified: 2}},
		},
	}

	var buf bytes.Buffer
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Sessions[0].Name != "alpha" {
		t.Errorf("got name %q, want alpha", got.Sessions[0].Name)
	}
	if !got.Sessions[0].HasBell {
		t.Error("bell flag lost in round trip")
	}
	if got.Sessions[0].Git.Modified != 2 {
		t.Errorf("got %d modified, want 2", got.Sessions[0].Git.Modified)
	}
}

func TestEncodeIsNewlineDelimited(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := Encode(&buf, &Snapshot{Version: Version}); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if n := strings.Count(buf.String(), "\n"); n != 3 {
		t.Errorf("got %d newlines, want 3", n)
	}
}

func TestDecoderReadsSuccessiveSnapshots(t *testing.T) {
	var buf bytes.Buffer
	for _, ts := range []int64{1, 2, 3} {
		if err := Encode(&buf, &Snapshot{Version: Version, Timestamp: ts}); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}

	d := NewDecoder(&buf)
	for _, want := range []int64{1, 2, 3} {
		got, err := d.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.Timestamp != want {
			t.Errorf("got timestamp %d, want %d", got.Timestamp, want)
		}
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestDecoderRejectsVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, &Snapshot{Version: Version + 1}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := NewDecoder(&buf).Next(); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("got %v, want ErrVersionMismatch", err)
	}
}

func TestDecoderSurvivesLargeSnapshot(t *testing.T) {
	snap := &Snapshot{Version: Version}
	for i := 0; i < 200; i++ {
		snap.Sessions = append(snap.Sessions, &session.Session{
			Name: strings.Repeat("x", 50),
			PR:   &session.PRStatus{Body: strings.Repeat("y", 5000)},
		})
	}
	var buf bytes.Buffer
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(got.Sessions) != 200 {
		t.Errorf("got %d sessions, want 200", len(got.Sessions))
	}
}

func TestSocketPathIsAbsolute(t *testing.T) {
	if p := SocketPath(); !strings.HasPrefix(p, "/") {
		t.Errorf("got %q, want an absolute path", p)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/vigil && go test ./internal/protocol/
```

Expected: FAIL, the package does not exist.

- [ ] **Step 3: Implement the protocol**

Create `internal/protocol/protocol.go`:

```go
package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jzinkduda/vigil/internal/session"
)

const Version = 1

// maxLine bounds a single snapshot. PR bodies and review comments make
// snapshots large, so this is well above bufio's 64KB default.
const maxLine = 8 << 20

var ErrVersionMismatch = errors.New("protocol version mismatch")

func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "vigil", "vigild.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "vigil", "vigild.sock")
}

type Snapshot struct {
	Version   int                `json:"version"`
	Timestamp int64              `json:"timestamp"`
	Sessions  []*session.Session `json:"sessions"`
}

func Encode(w io.Writer, snap *Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

type Decoder struct {
	scanner *bufio.Scanner
}

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxLine)
	return &Decoder{scanner: s}
}

func (d *Decoder) Next() (*Snapshot, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var snap Snapshot
	if err := json.Unmarshal(d.scanner.Bytes(), &snap); err != nil {
		return nil, err
	}
	if snap.Version != Version {
		return nil, ErrVersionMismatch
	}
	return &snap, nil
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/vigil && go test ./internal/protocol/ -v
```

Expected: 6 tests PASS.

- [ ] **Step 5: Note the macOS socket path limit**

macOS caps `sun_path` at 104 bytes. `~/.local/state/vigil/vigild.sock` is well under that for normal home directories, so no mitigation is needed, but confirm the computed path length:

```bash
printf '%s' "${XDG_RUNTIME_DIR:-$HOME/.local/state}/vigil/vigild.sock" | wc -c
```

Expected: comfortably below 104. If your environment reports otherwise, stop and raise it rather than working around it silently.

- [ ] **Step 6: Lint and commit**

```bash
cd ~/vigil && make lint && git add internal/protocol && \
  git commit -m "feat(protocol): add newline-delimited JSON snapshot protocol"
```

---

### Task 7: The daemon

**Files:**
- Create: `internal/daemon/daemon.go`
- Create: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `collect.Collector` and `collect.New` (Task 5); `protocol.Snapshot`, `protocol.Encode`, `protocol.SocketPath`, `protocol.Version` (Task 6); `config.Config`; `cache.Save`, `cache.CachePath`.
- Produces:

```go
package daemon

type Server struct {
    Collector *collect.Collector
    Interval  time.Duration
    SocketPath string
    CachePath  string
}

// New returns a Server configured from cfg. Interval comes from the
// git_interval setting, SocketPath from protocol.SocketPath().
func New(cfg *config.Config, cmd fetch.Commander) *Server

// Run listens on SocketPath and serves snapshots until ctx is cancelled.
// Removes a stale socket file if no daemon is listening on it. Returns
// ErrAlreadyRunning if one is.
func (s *Server) Run(ctx context.Context) error

var ErrAlreadyRunning = errors.New("daemon already running")
```

Task 8 calls `Run`.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/daemon_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	dir := t.TempDir()
	return &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   50 * time.Millisecond,
		SocketPath: filepath.Join(dir, "test.sock"),
		CachePath:  filepath.Join(dir, "cache.json"),
	}
}

func TestServerSendsSnapshotOnConnect(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	snap, err := protocol.NewDecoder(conn).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if snap.Version != protocol.Version {
		t.Errorf("got version %d, want %d", snap.Version, protocol.Version)
	}
}

func TestServerBroadcastsToMultipleClients(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	for i := 0; i < 3; i++ {
		conn, err := net.Dial("unix", s.SocketPath)
		if err != nil {
			t.Fatalf("Dial %d: %v", i, err)
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		if _, err := protocol.NewDecoder(conn).Next(); err != nil {
			t.Fatalf("client %d Next: %v", i, err)
		}
	}
}

func TestServerKeepsPushingOnInterval(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	d := protocol.NewDecoder(conn)
	for i := 0; i < 3; i++ {
		if _, err := d.Next(); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
}

func TestServerRefusesWhenAlreadyRunning(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	second := testServer(t)
	second.SocketPath = s.SocketPath
	if err := second.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("got %v, want ErrAlreadyRunning", err)
	}
}

func TestServerReplacesStaleSocket(t *testing.T) {
	s := testServer(t)
	if err := writeStaleSocketFile(s.SocketPath); err != nil {
		t.Fatalf("writeStaleSocketFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitForSocket(t, s.SocketPath)

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		t.Fatalf("Dial after stale cleanup: %v", err)
	}
	conn.Close()
}

func TestServerRemovesSocketOnShutdown(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	waitForSocket(t, s.SocketPath)

	cancel()
	<-done

	if _, err := net.Dial("unix", s.SocketPath); err == nil {
		t.Error("socket still accepting connections after shutdown")
	}
}
```

Add these helpers to the same file:

```go
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if conn, err := net.Dial("unix", path); err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became available", path)
}

func writeStaleSocketFile(path string) error {
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Close the listener but leave the file behind, which is what a
	// SIGKILLed daemon leaves on disk.
	return l.(*net.UnixListener).Close()
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/vigil && go test ./internal/daemon/
```

Expected: FAIL, the package does not exist.

- [ ] **Step 3: Implement the daemon**

Create `internal/daemon/daemon.go`:

```go
package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/cache"
	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

var ErrAlreadyRunning = errors.New("daemon already running")

const defaultInterval = 3 * time.Second

type Server struct {
	Collector  *collect.Collector
	Interval   time.Duration
	SocketPath string
	CachePath  string

	mu      sync.Mutex
	clients map[net.Conn]struct{}
	latest  *protocol.Snapshot
}

func New(cfg *config.Config, cmd fetch.Commander) *Server {
	interval := cfg.GetSettingDuration("git_interval")
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Server{
		Collector:  collect.New(cfg, cmd),
		Interval:   interval,
		SocketPath: protocol.SocketPath(),
		CachePath:  cache.CachePath(),
	}
}

func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return err
	}
	if err := s.clearStaleSocket(); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		listener.Close()
		os.Remove(s.SocketPath)
	}()

	s.clients = make(map[net.Conn]struct{})

	go s.accept(ctx, listener)

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			s.closeClients()
			return nil
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

// clearStaleSocket removes a socket file left behind by a dead daemon.
// A successful dial means a live daemon owns it.
func (s *Server) clearStaleSocket() error {
	if _, err := os.Stat(s.SocketPath); err != nil {
		return nil
	}
	conn, err := net.DialTimeout("unix", s.SocketPath, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return ErrAlreadyRunning
	}
	return os.Remove(s.SocketPath)
}

func (s *Server) accept(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			conn.Close()
			return
		}
		s.mu.Lock()
		s.clients[conn] = struct{}{}
		latest := s.latest
		s.mu.Unlock()

		if latest != nil {
			s.send(conn, latest)
		}
	}
}

func (s *Server) poll(ctx context.Context) {
	sessions, err := s.Collector.Snapshot(ctx)
	if err != nil {
		return
	}
	snap := &protocol.Snapshot{
		Version:   protocol.Version,
		Timestamp: time.Now().Unix(),
		Sessions:  sessions,
	}

	s.mu.Lock()
	s.latest = snap
	conns := make([]net.Conn, 0, len(s.clients))
	for c := range s.clients {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		s.send(c, snap)
	}

	if s.CachePath != "" {
		_ = cache.Save(s.CachePath, sessions)
	}
}

func (s *Server) send(conn net.Conn, snap *protocol.Snapshot) {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.Encode(conn, snap); err != nil {
		s.drop(conn)
	}
}

func (s *Server) drop(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
	conn.Close()
}

func (s *Server) closeClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.Close()
		delete(s.clients, c)
	}
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/vigil && go test ./internal/daemon/ -v
```

Expected: 6 tests PASS.

- [ ] **Step 5: Run with the race detector**

The daemon shares `clients` and `latest` across the accept goroutine and the poll loop. This is the highest-risk concurrency in the plan.

```bash
cd ~/vigil && go test -race -count=5 ./internal/daemon/
```

Expected: PASS with no race reports. `-count=5` because socket timing bugs are intermittent.

- [ ] **Step 6: Lint and commit**

```bash
cd ~/vigil && make lint && git add internal/daemon && \
  git commit -m "feat(daemon): serve session snapshots over a unix socket"
```

---

### Task 8: Subcommand dispatch in main.go

`main.go:18` matches `os.Args[1]` against two strings inline. Adding `daemon` needs real dispatch.

**Files:**
- Modify: `main.go:17-30`
- Create: `main_test.go`

**Interfaces:**
- Consumes: `daemon.New`, `daemon.Server.Run` (Task 7).
- Produces: `parseArgs(args []string) (command string, err error)`. Returns `"tui"` for no arguments, `"daemon"`, `"help"`, or `"version"`. Returns an error naming the unknown argument otherwise. Task 9 does not depend on this.

- [ ] **Step 1: Write the failing test**

Create `main_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args runs the tui", nil, "tui"},
		{"daemon subcommand", []string{"daemon"}, "daemon"},
		{"long help", []string{"--help"}, "help"},
		{"short help", []string{"-h"}, "help"},
		{"long version", []string{"--version"}, "version"},
		{"short version", []string{"-v"}, "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	got, err := parseArgs([]string{"--bogus"})
	if err == nil {
		t.Fatalf("want an error, got command %q", got)
	}
	if !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("error %q should name the offending argument", err)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/vigil && go test -run TestParseArgs ./...
```

Expected: FAIL, `parseArgs` is undefined.

- [ ] **Step 3: Implement dispatch**

Replace `main.go` lines 17-56 (the whole `main` function) with:

```go
func parseArgs(args []string) (string, error) {
	if len(args) == 0 {
		return "tui", nil
	}
	switch args[0] {
	case "daemon":
		return "daemon", nil
	case "--help", "-h":
		return "help", nil
	case "--version", "-v":
		return "version", nil
	default:
		return "", fmt.Errorf("unknown argument: %s", args[0])
	}
}

func main() {
	command, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
		printUsage(os.Stderr)
		os.Exit(2)
	}

	switch command {
	case "help":
		printUsage(os.Stdout)
		return
	case "version":
		fmt.Println("vigil " + version)
		return
	}

	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %s not found in PATH\n", dep)
			os.Exit(1)
		}
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v (using defaults)\n", err)
	}
	cmd := &fetch.ExecCommander{}

	switch command {
	case "daemon":
		if err := runDaemon(cfg, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
			os.Exit(1)
		}
	default:
		if err := runTUI(cfg, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
			os.Exit(1)
		}
	}
}

func runDaemon(cfg *config.Config, cmd fetch.Commander) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.New(cfg, cmd).Run(ctx)
}

func runTUI(cfg *config.Config, cmd fetch.Commander) error {
	m := model.New(cfg, cmd)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "vigil - TUI mission control for tmux sessions")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  vigil            Run the dashboard")
	fmt.Fprintln(w, "  vigil daemon     Run the state daemon in the foreground")
	fmt.Fprintln(w, "  vigil --help")
	fmt.Fprintln(w, "  vigil --version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Config: ~/.config/vigil/config.toml")
}
```

Update the import block to add `context`, `io`, `os/signal`, `syscall`, and `github.com/jzinkduda/vigil/internal/daemon`.

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd ~/vigil && go test -run TestParseArgs ./... -v
```

Expected: 7 subtests PASS.

- [ ] **Step 5: Verify the daemon actually runs and exits cleanly**

```bash
cd ~/vigil && make build && ./vigil --help && ./vigil --version
./vigil daemon &
sleep 2
ls -l "${XDG_RUNTIME_DIR:-$HOME/.local/state}/vigil/vigild.sock"
kill %1
sleep 1
ls "${XDG_RUNTIME_DIR:-$HOME/.local/state}/vigil/vigild.sock" 2>&1
```

Expected: help and version print; the socket exists while running; after `kill` the socket file is gone ("No such file or directory").

- [ ] **Step 6: Confirm the TUI is unchanged**

```bash
cd ~/vigil && ./vigil
```

Expected: the dashboard behaves exactly as before. Quit with `q`.

- [ ] **Step 7: Lint and commit**

```bash
cd ~/vigil && make lint && git add main.go main_test.go && \
  git commit -m "feat(cli): add subcommand dispatch and a daemon subcommand"
```

---

### Task 9: TUI consumes the daemon, falls back to self-polling

The TUI keeps its current polling code as the fallback. When a daemon is reachable, snapshots arrive over the socket instead.

**Files:**
- Create: `internal/model/client.go`
- Create: `internal/model/client_test.go`
- Modify: `internal/model/messages.go` (add two message types)
- Modify: `internal/model/model.go:145-170` (`Init`), `:171-232` (`Update`)

**Critical context, read before starting.** Three facts about the existing code drive this task's shape:

1. `IsCurrent` and `IsLast` are properties of a tmux *client*, not of the world. `session.Session` marks both `json:"-"`, so the daemon cannot and does not send them. The client must resolve them itself, and that means shelling out to `fetch.CurrentSession` / `fetch.LastSession`, which cannot happen inside `Update`. They are resolved inside the listen command, which already runs off the UI goroutine.

2. **Do not route `SnapshotMsg` through `handleTmuxUpdated` (model.go:595).** That handler merges tmux metadata into existing sessions by keeping the *old* session object and copying only four fields across, which discards `s.Git` and `s.PR`. Daemon snapshots arrive with git and PR already populated, so reusing that handler would silently throw away everything the daemon collected. `SnapshotMsg` needs its own handler.

3. There are no `m.tmuxInterval` / `m.gitInterval` / `m.prInterval` fields. Intervals are read at use site: `m.cfg.GetSettingDuration("git_interval")`, `m.cfg.GetSettingDuration("pr_interval")`, and a hardcoded `1*time.Second` for the tmux tick (model.go:163-167).

**Interfaces:**
- Consumes: `protocol.Snapshot`, `protocol.NewDecoder`, `protocol.SocketPath`, `protocol.Decoder` (Task 6); existing `fetch.CurrentSession`, `fetch.LastSession`, `session.SortSessions`, `m.checkStateTransitions`, `m.refreshDetailCmd`, `m.visibleSessions`, `m.addNotification`.
- Produces:
  - `SnapshotMsg struct { Sessions []*session.Session }` and `DaemonLostMsg struct{}` in `messages.go`.
  - `dialDaemon(path string) (net.Conn, error)` in `client.go`.
  - `listenDaemonCmd(decoder *protocol.Decoder, ctx context.Context, cmd fetch.Commander, fallbackCurrent string) tea.Cmd` in `client.go`. Reads one snapshot, resolves the per-client flags, and emits `SnapshotMsg`, or `DaemonLostMsg` when the stream ends.
  - `(m Model) handleSnapshot(msg SnapshotMsg) (tea.Model, tea.Cmd)` in `model.go`.
  - `Model.daemonConn net.Conn` and `Model.daemonDecoder *protocol.Decoder`.

- [ ] **Step 1: Write the failing test**

Create `internal/model/client_test.go`:

```go
package model

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
)

func serveOneSnapshot(t *testing.T, snap *protocol.Snapshot) net.Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if snap != nil {
			_ = protocol.Encode(conn, snap)
			time.Sleep(300 * time.Millisecond)
		}
	}()

	conn, err := dialDaemon(path)
	if err != nil {
		t.Fatalf("dialDaemon: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDialDaemonFailsWhenAbsent(t *testing.T) {
	if _, err := dialDaemon(filepath.Join(t.TempDir(), "nope.sock")); err == nil {
		t.Fatal("want an error dialing a nonexistent socket")
	}
}

func TestListenDaemonEmitsSnapshotMsg(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version: protocol.Version,
		Sessions: []*session.Session{
			{Name: "alpha", Git: session.GitStatus{Branch: "main", Modified: 3}},
		},
	})

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg", msg)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if snap.Sessions[0].Git.Modified != 3 {
		t.Errorf("got %d modified, want 3: git state must survive the client",
			snap.Sessions[0].Git.Modified)
	}
}

func TestListenDaemonResolvesPerClientFlags(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version: protocol.Version,
		Sessions: []*session.Session{
			{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"},
		},
	})

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cmd.OnArgs("tmux display-message -p #{client_last_session}", "gamma", nil)

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	snap := msg.(SnapshotMsg)

	byName := map[string]*session.Session{}
	for _, s := range snap.Sessions {
		byName[s.Name] = s
	}
	if !byName["beta"].IsCurrent {
		t.Error("beta should be marked current")
	}
	if byName["alpha"].IsCurrent {
		t.Error("alpha should not be marked current")
	}
	if !byName["gamma"].IsLast {
		t.Error("gamma should be marked last")
	}
}

func TestListenDaemonClearsLastWhenSessionGone(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version:  protocol.Version,
		Sessions: []*session.Session{{Name: "alpha"}},
	})

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.OnArgs("tmux display-message -p #{client_last_session}", "vanished", nil)

	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	snap := msg.(SnapshotMsg)
	if snap.Sessions[0].IsLast {
		t.Error("no session should be marked last when the last session is gone")
	}
}

func TestListenDaemonFallsBackToKnownCurrent(t *testing.T) {
	conn := serveOneSnapshot(t, &protocol.Snapshot{
		Version:  protocol.Version,
		Sessions: []*session.Session{{Name: "alpha"}},
	})

	// MockCommander returns "" for unregistered commands, standing in for
	// running outside tmux where display-message yields nothing.
	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "alpha")()
	snap := msg.(SnapshotMsg)
	if !snap.Sessions[0].IsCurrent {
		t.Error("should fall back to the current session detected at startup")
	}
}

func TestListenDaemonEmitsDaemonLostOnClose(t *testing.T) {
	conn := serveOneSnapshot(t, nil)

	cmd := fetch.NewMockCommander()
	msg := listenDaemonCmd(protocol.NewDecoder(conn), context.Background(), cmd, "")()
	if _, ok := msg.(DaemonLostMsg); !ok {
		t.Fatalf("got %T, want DaemonLostMsg", msg)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
cd ~/vigil && go test ./internal/model/
```

Expected: FAIL, `dialDaemon` / `listenDaemonCmd` / `SnapshotMsg` / `DaemonLostMsg` are undefined.

- [ ] **Step 3: Add the message types**

Append to `internal/model/messages.go`:

```go
// SnapshotMsg carries a full session snapshot received from the daemon,
// with per-client flags already resolved.
type SnapshotMsg struct {
	Sessions []*session.Session
}

// DaemonLostMsg reports that the daemon stream ended, so the TUI should
// resume self-polling.
type DaemonLostMsg struct{}
```

- [ ] **Step 4: Implement the client**

Create `internal/model/client.go`:

```go
package model

import (
	"context"
	"net"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

func dialDaemon(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 300*time.Millisecond)
}

// listenDaemonCmd reads one snapshot per invocation; Update re-issues it on
// every SnapshotMsg. Which session is current or last belongs to this tmux
// client rather than to the daemon, so those are resolved here, off the UI
// goroutine, where the tmux queries are allowed to block.
func listenDaemonCmd(
	decoder *protocol.Decoder,
	ctx context.Context,
	cmd fetch.Commander,
	fallbackCurrent string,
) tea.Cmd {
	return func() tea.Msg {
		snap, err := decoder.Next()
		if err != nil {
			return DaemonLostMsg{}
		}

		current := fetch.CurrentSession(ctx, cmd)
		if current == "" {
			current = fallbackCurrent
		}
		last := fetch.LastSession(ctx, cmd)

		names := make(map[string]bool, len(snap.Sessions))
		for _, s := range snap.Sessions {
			names[s.Name] = true
		}
		if !names[last] {
			last = ""
		}
		for _, s := range snap.Sessions {
			s.IsCurrent = s.Name == current
			s.IsLast = s.Name == last
		}

		return SnapshotMsg{Sessions: snap.Sessions}
	}
}
```

- [ ] **Step 5: Run the client tests to confirm they pass**

```bash
cd ~/vigil && go test ./internal/model/ -run 'TestDialDaemon|TestListenDaemon' -v
```

Expected: 6 tests PASS.

- [ ] **Step 6: Commit the client before touching the Model**

```bash
cd ~/vigil && make lint && \
  git add internal/model/client.go internal/model/client_test.go internal/model/messages.go && \
  git commit -m "feat(model): add daemon snapshot client"
```

- [ ] **Step 7: Add the Model fields and dial in New**

Add to the `Model` struct in `internal/model/model.go`, after the `cmd fetch.Commander` field:

```go
	// Daemon connection (nil when self-polling)
	daemonConn    net.Conn
	daemonDecoder *protocol.Decoder
```

`Init` has a value receiver, so assignments made there do not persist to the running model. Dial in `New` (model.go:86) instead. Add immediately before `New` returns its Model:

```go
	if conn, err := dialDaemon(protocol.SocketPath()); err == nil {
		m.daemonConn = conn
		m.daemonDecoder = protocol.NewDecoder(conn)
	}
```

Add `net` and `internal/protocol` to the import block.

- [ ] **Step 8: Branch Init on the daemon**

In `internal/model/model.go`, `Init` (line 145) currently loads the cache and then appends five polling commands. Insert the daemon branch between those two blocks, immediately after the cache-load `if` and before `cmds = append(cmds, m.fetchTmuxCmd(), ...)`:

```go
	if m.daemonDecoder != nil {
		cmds = append(cmds,
			listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName))
		return tea.Batch(cmds...)
	}
```

The cache load stays above it so the daemon path still gets an instant first paint. Leave the existing polling block below untouched; it is now the fallback.

- [ ] **Step 9: Add the snapshot handler**

Add to `internal/model/model.go`, next to `handleTmuxUpdated`:

```go
// handleSnapshot applies a complete daemon snapshot. Unlike
// handleTmuxUpdated, it does not merge into existing sessions: the snapshot
// already carries git and PR state, and merging would discard it.
func (m Model) handleSnapshot(msg SnapshotMsg) (tea.Model, tea.Cmd) {
	m.sessions = msg.Sessions

	// Keep the caches warm so a later fall back to self-polling starts
	// with data rather than blank columns.
	for _, s := range m.sessions {
		m.gitCache[s.Name] = s.Git
		if s.Git.Branch != "" && s.PR != nil {
			m.prCache[s.Git.Branch] = s.PR
		}
	}

	session.SortSessions(m.sessions, m.sortMode)

	if !m.cursorPlaced && m.popupMode && m.currentSessionName != "" {
		for i, s := range m.visibleSessions() {
			if s.Name == m.currentSessionName {
				m.cursor = i
				m.cursorPlaced = true
				break
			}
		}
	}

	cmds := m.checkStateTransitions()
	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}
	cmds = append(cmds,
		listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName))

	return m, tea.Batch(cmds...)
}

func (m Model) handleDaemonLost() (tea.Model, tea.Cmd) {
	if m.daemonConn != nil {
		m.daemonConn.Close()
		m.daemonConn = nil
		m.daemonDecoder = nil
	}
	m.addNotification("daemon lost, polling directly", "warning")
	return m, tea.Batch(
		m.fetchTmuxCmd(),
		m.fetchGitCmd(),
		tmuxTickCmd(1*time.Second),
		gitTickCmd(m.cfg.GetSettingDuration("git_interval")),
		prTickCmd(m.cfg.GetSettingDuration("pr_interval")),
	)
}
```

- [ ] **Step 10: Dispatch the new messages in Update**

Add two cases to the `Update` switch (model.go:171), alongside the existing `case TmuxUpdatedMsg:`:

```go
	case SnapshotMsg:
		return m.handleSnapshot(msg)

	case DaemonLostMsg:
		return m.handleDaemonLost()
```

- [ ] **Step 11: Mirror the initial-load flags**

`Model` has `initialLoad` and `initialPRDone` flags that the self-polling handlers clear, and the view uses them to decide whether to show a loading state. The daemon path must clear them too or the TUI will render as permanently loading. Find what sets them and what reads them:

```bash
cd ~/vigil && grep -n "initialLoad\|initialPRDone" internal/model/*.go internal/view/*.go
```

Add whatever assignments `handleGitUpdated` and `handlePRUpdated` make to those fields into `handleSnapshot`, since a snapshot delivers both git and PR data at once. Re-run the checks in step 12 after.

- [ ] **Step 12: Run the full suite with the race detector**

```bash
cd ~/vigil && go test -race ./...
```

Expected: all packages PASS.

- [ ] **Step 13: The phase 1 equivalence gate**

Phase 1 must be invisible. Verify by comparing both paths against real data.

```bash
cd ~/vigil && make build

# Daemon path
./vigil daemon &
sleep 5
./vigil    # note the rows, states, git and PR columns; quit with q

# Fallback path
kill %1
sleep 1
./vigil    # same session list, same columns
```

Confirm: identical session names, order, git columns, PR columns, and bell highlighting. On the fallback path there must be **no** "daemon lost" notification, because the TUI never connected. Then test the mid-session transition:

```bash
./vigil daemon &
sleep 3
./vigil     # leave it running
# in another terminal:
pkill -f 'vigil daemon'
```

Confirm: the TUI shows "daemon lost, polling directly" once, keeps its data, and continues updating.

- [ ] **Step 14: Lint and commit**

```bash
cd ~/vigil && make lint && \
  git add internal/model/model.go internal/model/client.go && \
  git commit -m "feat(model): consume daemon snapshots, self-poll as fallback"
```

- [ ] **Step 15: Update project docs**

Add to `CLAUDE.md` under Architecture:

```markdown
- `internal/collect/` - UI-independent session state collection (used by daemon and TUI fallback)
- `internal/protocol/` - newline-delimited JSON snapshot protocol over a unix socket
- `internal/daemon/` - `vigil daemon`: polls once, broadcasts snapshots to all clients
```

And under Key Conventions:

```markdown
- The TUI prefers daemon snapshots and falls back to self-polling; both paths are supported permanently
```

Add to `README.md` under Usage:

```markdown
# Run the state daemon (one poller shared by all clients)
vigil daemon
```

Commit:

```bash
cd ~/vigil && git add CLAUDE.md README.md && \
  git commit -m "docs: document the collect/protocol/daemon packages"
```

---

## Definition of done

- `cd ~/dotfiles/scripts/scripts && make test && make lint` passes.
- `cd ~/vigil && go test -race ./... && make lint` passes.
- `grep -rn "send-keys" ~/dotfiles/scripts/scripts` returns exactly one hit, in `setup_secondary_pane`.
- A real `dispatch sc-<id>` run launches Claude with the correct model and effort, leaves a prompt file inside the worktree's git dir, and that file does not appear in `git status`.
- `vigil` renders identically with the daemon running and with it stopped.
- Killing the daemon under a running TUI produces one warning and uninterrupted updates.

## Not in this plan

Phases 2 through 6 of the spec: panel render mode and its toggle binding, panel-by-default for new sessions, dispatch through the daemon, the work queue, and the deletions. Phase 2 gets its own plan after this one has been lived on.

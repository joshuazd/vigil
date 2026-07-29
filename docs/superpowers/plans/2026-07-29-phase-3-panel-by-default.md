# Phase 3: Panel By Default Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** New tmux sessions come up with a vigil panel already in their `claude` window, disabled by a `panel_auto` setting in vigil's own config.

**Architecture:** Two repositories. In `~/vigil`, a testable `run()` seam in `main.go`, a `vigil config get <key>` subcommand that answers before the dependency check, and a grace period that stops a panel owning transition effects between spawning a daemon and reconnecting to it. In `~/dotfiles`, the panel split moves out of the `vigil-panel` script into a shared `add_vigil_panel` function in `lib/tmux.sh` that `create_tmux_session` also calls.

**Tech Stack:** Go 1.x with Bubble Tea (`~/vigil`), Bash with bats-core (`~/dotfiles`).

**Spec:** `docs/superpowers/specs/2026-07-29-phase-3-panel-by-default-design.md`

## Global Constraints

- `~/vigil` tests run as `make test`, which is `go test -race ./...`. The `-race` flag is not optional: the daemon's design is a concurrency claim.
- `~/vigil` lints with `make lint` (`golangci-lint`). Both must be clean before any commit.
- `~/dotfiles` tests run as `bats tests/` from `scripts/scripts/`.
- No global mutable state in `~/vigil`. Config and caches are passed explicitly.
- Prefer no code comments. Comment only where the meaning cannot be inferred from reading the code.
- Never use the em dash. Use a plain dash.
- Every subprocess in `~/vigil` goes through `fetch.Commander`. The only permitted direct `exec` sites are `internal/fetch/cmd.go` and the daemon spawn in `internal/model/client.go`.
- Bash: no heredocs. Use the Write tool for files and `cmd <<< "text"` for stdin.
- `cp` and `rm` are aliased to `-i` on this machine. Use `\rm` and `\cp`, or `git checkout --`.
- Commit after each task. Do not squash tasks together.

## Task Order and Why

Tasks 1 to 3 are `~/vigil` and land first: task 7 shells out to `vigil config get`, so that subcommand has to exist and be installed before the bash side can be tested against a real binary. Tasks 4 to 7 are `~/dotfiles`. Task 4 is pure test-harness work with no production change, and tasks 6 and 7 are unassertable without it. Task 8 is manual verification.

Tasks 3 and 4 are independent of everything else and can be done in any order relative to their neighbours.

## File Structure

**`~/vigil`**

| File | Change | Responsibility |
|---|---|---|
| `main.go` | Modify | Gains `run(args, stdout, stderr) int`; `main` shrinks to a call. `parseArgs` returns remaining args. New `config` command. |
| `main_test.go` | Create | Covers `run` dispatch, exit codes, and that `config get` precedes the dependency check. |
| `internal/config/config.go` | Modify | New `panel_auto` setting; new exported `IsSetting`. |
| `internal/config/config_test.go` | Modify | Covers `panel_auto` default and `IsSetting`. |
| `internal/model/model.go` | Modify | New `effectsDisownedUntil` field, `spawnGrace` var, `effectsDisowned()` helper, one new condition in `checkStateTransitions`. |
| `internal/model/spawn_grace_test.go` | Create | Covers the grace window, its expiry, and that a repeated failed probe does not extend it. |

**`~/dotfiles/scripts/scripts`**

| File | Change | Responsibility |
|---|---|---|
| `tests/stubs/tmux` | Modify | `display-message` answers keyed on the requested format. |
| `tests/stubs/vigil` | Create | Answers `config get panel_auto`. |
| `tests/helper.bash` | Modify | New `tmux_call_index` for ordering assertions. |
| `lib/tmux.sh` | Modify | Gains `panel_geometry` and `add_vigil_panel`; `setup_secondary_pane` measures the pane; `create_tmux_session` gains the panel step. |
| `vigil-panel` | Modify | Reduces to a toggle over the shared function. |
| `tests/tmux_lib.bats` | Modify | Coverage for the new functions and the changed ones. |
| `tests/vigil_panel.bats` | Modify | Toggle-only coverage; the split assertions move to `tmux_lib.bats`. |

---

### Task 1: The `run()` seam in `main.go`

Pure refactor. No behaviour change. It exists so Task 2's ordering claim is an assertion rather than a comment.

**Files:**
- Modify: `main.go:22-87`
- Test: `main_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `func parseArgs(args []string) (string, []string, error)` - returns the command name, the arguments after the command word, and an error. `func run(args []string, stdout, stderr io.Writer) int` - returns the process exit code.

- [ ] **Step 1: Write the failing test**

Create `main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseArgsReturnsTheRemainingArguments(t *testing.T) {
	cmd, rest, err := parseArgs([]string{"daemon"})
	if err != nil {
		t.Fatalf("parseArgs returned %v, want nil", err)
	}
	if cmd != "daemon" {
		t.Errorf("got command %q, want daemon", cmd)
	}
	if len(rest) != 0 {
		t.Errorf("got rest %v, want empty", rest)
	}
}

func TestParseArgsRejectsAnUnknownArgument(t *testing.T) {
	if _, _, err := parseArgs([]string{"nonsense"}); err == nil {
		t.Fatal("parseArgs accepted an unknown argument, want an error")
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "vigil ") {
		t.Errorf("got stdout %q, want it to start with \"vigil \"", stdout.String())
	}
}

func TestRunPrintsHelpToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("got stdout %q, want it to contain \"Usage:\"", stdout.String())
	}
}

func TestRunRejectsAnUnknownArgumentWithExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"nonsense"}, &stdout, &stderr); code != 2 {
		t.Fatalf("got exit code %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown argument") {
		t.Errorf("got stderr %q, want it to mention the unknown argument", stderr.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/vigil && go test ./ -run 'TestParseArgs|TestRun' -v`

Expected: compile failure. `parseArgs` returns two values, not three, and `run` is undefined.

- [ ] **Step 3: Rewrite `main.go`'s entry point**

Replace `parseArgs` and `main` (`main.go:22-87`) with:

```go
func parseArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "tui", nil, nil
	}
	switch args[0] {
	case "daemon":
		return "daemon", args[1:], nil
	case "--panel":
		return "panel", args[1:], nil
	case "--help", "-h":
		return "help", args[1:], nil
	case "--version", "-v":
		return "version", args[1:], nil
	default:
		return "", nil, fmt.Errorf("unknown argument: %s", args[0])
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	command, _, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "vigil: %v\n", err)
		printUsage(stderr)
		return 2
	}

	switch command {
	case "help":
		printUsage(stdout)
		return 0
	case "version":
		fmt.Fprintln(stdout, "vigil "+version)
		return 0
	}

	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			fmt.Fprintf(stderr, "vigil: %s not found in PATH\n", dep)
			return 1
		}
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "vigil: %v (using defaults)\n", err)
	}
	cmd := &fetch.ExecCommander{}

	switch command {
	case "daemon":
		err = runDaemon(cfg, cmd)
	case "panel":
		err = runPanel(cfg, cmd)
	default:
		err = runTUI(cfg, cmd)
	}
	if err != nil {
		fmt.Fprintf(stderr, "vigil: %v\n", err)
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ~/vigil && go test ./ -run 'TestParseArgs|TestRun' -v`

Expected: all five PASS.

- [ ] **Step 5: Run the full suite and the linter**

Run: `cd ~/vigil && make test && make lint`

Expected: both clean. If `golangci-lint` reports the `_` for `rest` as unused, leave it - Task 2 consumes it.

- [ ] **Step 6: Commit**

```bash
cd ~/vigil
git add main.go main_test.go
git commit -m "refactor(main): extract a testable run() seam

parseArgs returns the arguments after the command word and main shrinks to
one call, so dispatch order and exit codes become assertions instead of
comments. No behaviour change."
```

---

### Task 2: `panel_auto` and `vigil config get`

**Files:**
- Modify: `internal/config/config.go:31-42` (settings table), and append `IsSetting`
- Modify: `main.go` (new `config` case, new `runConfigGet`, `printUsage`)
- Test: `internal/config/config_test.go`, `main_test.go`

**Interfaces:**
- Consumes: `parseArgs` and `run` from Task 1.
- Produces: `func IsSetting(name string) bool` in `internal/config`. The `panel_auto` setting key, env var `VIGIL_PANEL_AUTO`, default `"true"`. CLI contract: `vigil config get <key>` prints the value and exits 0; an unknown key prints nothing to stdout and exits 1; wrong usage exits 2.

- [ ] **Step 1: Write the failing config tests**

Append to `internal/config/config_test.go`:

```go
func TestPanelAutoDefaultsToTrue(t *testing.T) {
	cfg := &Config{}
	if got := cfg.GetSetting("panel_auto"); got != "true" {
		t.Errorf("got %q, want true", got)
	}
}

func TestPanelAutoReadsTheConfigFile(t *testing.T) {
	cfg := &Config{Settings: map[string]any{"panel_auto": "false"}}
	if got := cfg.GetSetting("panel_auto"); got != "false" {
		t.Errorf("got %q, want false", got)
	}
}

func TestPanelAutoEnvVarWins(t *testing.T) {
	t.Setenv("VIGIL_PANEL_AUTO", "false")
	cfg := &Config{Settings: map[string]any{"panel_auto": "true"}}
	if got := cfg.GetSetting("panel_auto"); got != "false" {
		t.Errorf("got %q, want false", got)
	}
}

// IsSetting is what lets a caller tell "the setting is off" from "that is not
// a setting". GetSetting cannot: it returns "" for both an unknown key and
// capture_window, which is legitimately empty by default.
func TestIsSettingDistinguishesAnEmptyDefaultFromAnUnknownKey(t *testing.T) {
	if !IsSetting("capture_window") {
		t.Error("IsSetting said capture_window is not a setting")
	}
	if IsSetting("no_such_setting") {
		t.Error("IsSetting accepted an unknown key")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd ~/vigil && go test ./internal/config/ -run 'TestPanelAuto|TestIsSetting' -v`

Expected: the three `TestPanelAuto` tests fail with `got "" , want true` or similar; `TestIsSetting` fails to compile because `IsSetting` is undefined.

- [ ] **Step 3: Add the setting and `IsSetting`**

In `internal/config/config.go`, add to `settingDefaults` after the `auto_focus` line:

```go
	"panel_auto":            {"VIGIL_PANEL_AUTO", "true"},
```

And after `GetSettingDuration`, add:

```go
// IsSetting reports whether name is a known setting. Callers need this to
// tell an unknown key from a setting whose value is legitimately empty.
func IsSetting(name string) bool {
	_, ok := settingDefaults[name]
	return ok
}
```

- [ ] **Step 4: Run the config tests to verify they pass**

Run: `cd ~/vigil && go test ./internal/config/ -run 'TestPanelAuto|TestIsSetting' -v`

Expected: all four PASS.

- [ ] **Step 5: Write the failing CLI tests**

Append to `main_test.go`:

```go
// The dependency check must not run before config get. A bash caller that
// receives "gh not found" instead of a value would silently disable the panel
// on any machine mid-setup, which looks identical to panel_auto = false.
func TestConfigGetAnswersWithoutTheDependencies(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "get", "panel_auto"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d and stderr %q, want 0", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "true" {
		t.Errorf("got stdout %q, want true", stdout.String())
	}
}

func TestConfigGetRejectsAnUnknownKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "get", "no_such_setting"}, &stdout, &stderr); code != 1 {
		t.Fatalf("got exit code %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Errorf("got stdout %q, want nothing", stdout.String())
	}
}

func TestConfigRejectsAMissingSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config"}, &stdout, &stderr); code != 2 {
		t.Fatalf("got exit code %d, want 2", code)
	}
}

func TestConfigGetHonoursTheEnvironment(t *testing.T) {
	t.Setenv("VIGIL_PANEL_AUTO", "false")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "get", "panel_auto"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != "false" {
		t.Errorf("got stdout %q, want false", stdout.String())
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `cd ~/vigil && go test ./ -run TestConfig -v`

Expected: FAIL with exit code 2 and "unknown argument: config".

- [ ] **Step 7: Add the subcommand**

In `main.go`'s `parseArgs`, add before `default`:

```go
	case "config":
		return "config", args[1:], nil
```

In `run`, change `command, _, err := parseArgs(args)` to `command, rest, err := parseArgs(args)` and add a case to the first switch, alongside `help` and `version`:

```go
	case "config":
		return runConfigGet(rest, stdout, stderr)
```

Add the function:

```go
// runConfigGet answers before the dependency check on purpose: reading a
// config value has no business requiring gh to be installed.
func runConfigGet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "get" {
		fmt.Fprintln(stderr, "vigil: usage: vigil config get <key>")
		return 2
	}
	key := args[1]
	if !config.IsSetting(key) {
		fmt.Fprintf(stderr, "vigil: unknown setting: %s\n", key)
		return 1
	}
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "vigil: %v (using defaults)\n", err)
	}
	fmt.Fprintln(stdout, cfg.GetSetting(key))
	return 0
}
```

Add to `printUsage`, after the `--panel` line:

```go
	_, _ = fmt.Fprintln(w, "  vigil config get <key>   Print a config value")
```

- [ ] **Step 8: Run the CLI tests to verify they pass**

Run: `cd ~/vigil && go test ./ -run TestConfig -v`

Expected: all four PASS.

- [ ] **Step 9: Run the full suite and the linter**

Run: `cd ~/vigil && make test && make lint`

Expected: both clean.

- [ ] **Step 10: Commit**

```bash
cd ~/vigil
git add main.go main_test.go internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add panel_auto and a vigil config get subcommand

panel_auto defaults to true and gates the panel that create_tmux_session
adds to new sessions. config get dispatches alongside help and version, so
it answers before the tmux/git/gh check: a bash caller receiving \"gh not
found\" instead of a value would silently disable the panel on a machine
mid-setup, and that is indistinguishable from panel_auto = false.

IsSetting exists because GetSetting cannot tell an unknown key from a
setting that is legitimately empty, which capture_window is by default."
```

---

### Task 3: The daemon ownership grace period

**Files:**
- Modify: `internal/model/model.go` (Model struct, a new var, `spawnDaemonOnce`, `checkStateTransitions:1325`)
- Test: `internal/model/spawn_grace_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `var spawnGrace time.Duration` (package-level, shortenable by tests). `Model.effectsDisownedUntil time.Time`. `func (m *Model) effectsDisowned() bool`.

Background: `checkStateTransitions(local bool)` decides ownership on `local` alone. A panel that has just spawned a daemon is self-polling and about to stop being the owner, so both it and the fresh daemon run effects for the same event. Toasts are added earlier in the same loop, before the `local` check, and must stay unaffected.

- [ ] **Step 1: Write the failing tests**

Create `internal/model/spawn_grace_test.go`:

```go
package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/session"
)

// panelThatSpawnedADaemon is a self-polling panel in the state newModel
// leaves it in when it found no daemon and started one: no connection, and
// the grace period running.
func panelThatSpawnedADaemon(effects *countingEffects) Model {
	m := transitionModel(effects)
	m.panelMode = true
	m.effectsDisownedUntil = time.Now().Add(spawnGrace)
	return m
}

func TestAPanelInsideItsSpawnGraceRunsNoEffects(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs during the grace period, want 0", got)
	}
}

// The toast is per-client and must survive: only hooks and cleanups are
// owned by one process.
func TestAPanelInsideItsSpawnGraceStillToasts(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	m.checkStateTransitions(true)

	if len(m.notifications) == 0 {
		t.Error("got no notifications during the grace period, want one")
	}
}

func TestAPanelOwnsEffectsOnceTheGraceExpires(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)
	m.effectsDisownedUntil = time.Now().Add(-time.Millisecond)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Errorf("got %d effect runs after the grace expired, want 1", got)
	}
}

// This is the test that pins the arming rule. handleProbeResult calls
// spawnDaemonOnce again on every failed probe, and a failed probe never
// establishes a connection, so none of those repeats may re-arm the deadline.
//
// Not "forever": spawnCooldown is 15s and spawnGrace is 5s, so an
// unconditional re-arm would suppress about 5s in every 15, recurring while
// the daemon kept failing. Bounded, but still the zero-hooks direction. What
// prevents it is the connection gate, not the arithmetic.
func TestARepeatedFailedProbeDoesNotExtendTheGrace(t *testing.T) {
	original := daemonSpawner
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = original })

	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)
	deadline := m.effectsDisownedUntil

	// Age lastSpawn past the cooldown so the respawn actually happens; with
	// the cooldown still in force spawnDaemonOnce returns early and the test
	// would pass against either implementation.
	m.lastSpawn = time.Now().Add(-spawnCooldown - time.Second)
	m.spawnDaemonOnce()

	if !m.effectsDisownedUntil.Equal(deadline) {
		t.Errorf("grace deadline moved from %v to %v on a respawn, want it unchanged",
			deadline, m.effectsDisownedUntil)
	}
}

func TestADashboardOwnsEffectsImmediately(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Errorf("got %d effect runs on a dashboard, want 1", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd ~/vigil && go test ./internal/model/ -run 'Grace|Spawn|Dashboard' -race -v`

Expected: compile failure - `spawnGrace` and `effectsDisownedUntil` are undefined. `TestADashboardOwnsEffectsImmediately` will pass once it compiles; it is the control.

- [ ] **Step 3: Add the field and the var**

In `internal/model/model.go`, add next to the `inFlightEffects` field:

```go
	// effectsDisownedUntil suppresses this client's transition effects while a
	// daemon it just spawned is coming up. Both would otherwise own the same
	// event: newModel spawns and starts self-polling immediately, but the
	// reconnect probe only lands a probe interval later.
	//
	// Armed on any successful spawn, provided the client has had a live daemon
	// connection since the last arm (daemonSeenSinceArm). handleProbeResult
	// respawns on every failed probe, and a failed probe never establishes a
	// connection, so a daemon that never starts arms exactly once.
	effectsDisownedUntil time.Time
```

> **Correction, applied after implementation.** The brief below and the spec both
> originally said the deadline is set on the *first* spawn only, and justified it with the
> claim that re-arming would suppress effects "forever". Both are wrong and the shipped
> code does neither. Arm-once misses daemon restart: a panel whose daemon crashes
> mid-session respawns one from a failed probe, never arms, and both processes own the
> event - two cross-process `CleanupSession` calls on one worktree for a `Done`, because
> `inFlightEffects` is per-process. And "forever" is false arithmetic: `spawnCooldown` is
> 15s against a 5s `spawnGrace`, so an unconditional re-arm would suppress about 5s in
> every 15. The rule that shipped is the connection gate described in the comment above;
> see the spec's "The daemon ownership window" section.

Next to `spawnCooldown` (`model.go:31-33`):

```go
// spawnGrace is how long a panel that just spawned a daemon waits before it
// will run transition effects itself. A healthy reconnect lands at
// daemonProbeInterval plus the dial, well inside this.
//
// A var, not a const, so tests shorten it rather than sleeping.
var spawnGrace = 5 * time.Second
```

- [ ] **Step 4: Arm it and consult it**

In `spawnDaemonOnce` (`model.go:252-260`), after the successful-spawn branch:

```go
func (m *Model) spawnDaemonOnce() {
	if time.Since(m.lastSpawn) < spawnCooldown {
		return
	}
	m.lastSpawn = time.Now()
	if err := daemonSpawner(); err != nil {
		m.addNotification("could not start daemon: "+err.Error(), "warning")
		return
	}
	if m.effectsDisownedUntil.IsZero() || m.daemonSeenSinceArm {
		m.effectsDisownedUntil = time.Now().Add(spawnGrace)
		m.daemonSeenSinceArm = false
	}
}
```

`daemonSeenSinceArm` is set at the two places a working connection is established -
`newModel`'s dial and `handleProbeResult`'s live-conn branch - and nowhere else. See the
correction note under Step 3: this is the shipped rule, not the arm-once one the rest of
this brief was written against.

Add the helper below it:

```go
func (m *Model) effectsDisowned() bool {
	return time.Now().Before(m.effectsDisownedUntil)
}
```

In `checkStateTransitions`, change `model.go:1325` from:

```go
		if !local {
			continue
		}
```

to:

```go
		if !local || m.effectsDisowned() {
			continue
		}
```

The notification added earlier in the same loop body is deliberately left above this check: toasts are per-client and ungated.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/vigil && go test ./internal/model/ -run 'Grace|Spawn|Dashboard' -race -v`

Expected: all six PASS.

- [ ] **Step 6: Verify the tests would fail against the wrong implementation**

Temporarily change the arming block in `spawnDaemonOnce` to re-arm unconditionally:

```go
	m.effectsDisownedUntil = time.Now().Add(spawnGrace)
```

Run: `cd ~/vigil && go test ./internal/model/ -run Grace -race`

Expected: `TestARepeatedFailedProbeDoesNotExtendTheGrace` FAILS. If it passes, the test is vacuous and must be fixed before continuing - most likely the `lastSpawn` ageing line was dropped, so the cooldown short-circuited the respawn.

Restore with `git checkout -- internal/model/model.go` **only if nothing else in that file is uncommitted**. If it is, revert the three lines by hand. A `git checkout --` that discards an uncommitted fix is a documented way work has been lost in this repo.

- [ ] **Step 7: Run the full suite and the linter**

Run: `cd ~/vigil && make test && make lint`

Expected: both clean. Pay attention to any pre-existing transition test that now sees zero effects - none should, because only `panelMode` models that spawned a daemon arm the deadline, and `newTestModel` leaves it zero.

- [ ] **Step 8: Commit**

```bash
cd ~/vigil
git add internal/model/model.go internal/model/spawn_grace_test.go
git commit -m "fix(model): stop owning effects while a spawned daemon comes up

newModel spawns a daemon and starts self-polling in the same breath, but the
reconnect probe only lands a probe interval later. In that window the panel
and the fresh daemon both own transition effects for the same event, which
the blockers handoff measured at notify=4 for two transitions.

Phase 3 makes this routine: every cold-start dispatch will hit it.

The deadline is armed on any successful spawn where the client has had a live
daemon connection since the last arm. handleProbeResult respawns on each
failed probe and a failed probe never connects, so a daemon that never starts
arms exactly once and cannot chain suppressions."
```

The commit that actually landed (`d09cb60`) carries the arm-once wording, because the rule
was corrected in the two follow-up commits `6f06d73` and `01fe026` rather than by amending
it. Read the three together.

---

### Task 4: Test-harness gaps in `~/dotfiles`

No production change. Tasks 6 and 7 are unassertable without this.

**Files:**
- Modify: `tests/stubs/tmux`
- Modify: `tests/helper.bash`
- Test: `tests/tmux_lib.bats`

**Interfaces:**
- Consumes: nothing.
- Produces: the tmux stub keys `display-message` on the requested format, honouring `TMUX_STUB_DISPLAY` for `#{client_height}`/`#{client_width}` and `TMUX_STUB_PANE_WIDTH` for `#{pane_width}`. New helper `tmux_call_index <subcommand> <pattern>` prints the 1-based log line number of the first matching invocation.

- [ ] **Step 1: Write the failing tests**

Append to `tests/tmux_lib.bats`:

```bash
@test "the stub answers pane_width separately from the client size" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_PANE_WIDTH="160"
  run tmux display-message -p '#{pane_width}'
  [ "${output}" = "160" ]
  run tmux display-message -p '#{client_height} #{client_width}'
  [ "${output}" = "40 200" ]
}

@test "an explicitly empty client size stays empty" {
  # The no-client case. A :- default would silently substitute dimensions and
  # the fallback branch could never be reached from a test.
  export TMUX_STUB_DISPLAY=""
  run tmux display-message -p '#{client_height} #{client_width}'
  [ "${output}" = "" ]
}

@test "tmux_call_index reports call order" {
  tmux split-window -t first
  tmux respawn-pane -t second
  first_index="$(tmux_call_index "split-window" "first")"
  second_index="$(tmux_call_index "respawn-pane" "second")"
  [ "${first_index}" -lt "${second_index}" ]
}

@test "tmux_call_index is empty for a call that never happened" {
  run tmux_call_index "kill-pane" "anything"
  [ "${output}" = "" ]
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "stub answers|explicitly empty|call_index"`

Expected: the first two fail because every `display-message` returns the same value; the last two fail with "command not found: tmux_call_index".

- [ ] **Step 3: Key the stub on the requested format**

In `tests/stubs/tmux`, replace the `display-message)` case with:

```bash
  display-message)
    # Keyed on the requested format. One canned answer for every
    # display-message would feed client dimensions into a pane-width query,
    # so a mutant asking the wrong question would still get the right answer.
    #
    # The client branch uses ${VAR-default}, not ${VAR:-default}: an
    # explicitly empty value is the no-client case and must survive.
    case "${*}" in
      *client_height*|*client_width*)
        printf '%s\n' "${TMUX_STUB_DISPLAY-40 200}"
        ;;
      *pane_width*)
        printf '%s\n' "${TMUX_STUB_PANE_WIDTH:-200}"
        ;;
      *)
        printf '%s\n' "${TMUX_STUB_DISPLAY:-/tmp/stub-worktree}"
        ;;
    esac
    ;;
```

- [ ] **Step 4: Add the ordering helper**

Append to `tests/helper.bash`:

```bash
# Print the 1-based log line number of the first invocation matching both the
# subcommand and the pattern. Every other helper throws position away, and
# ordering between two calls of the same subcommand cannot be asserted
# without it.
tmux_call_index() {
  local subcommand="${1}"
  local pattern="${2}"
  grep -n -m1 -e "^${subcommand}${TMUX_STUB_SEP}.*${pattern}" "${TMUX_STUB_LOG}" \
    | cut -d: -f1
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "stub answers|explicitly empty|call_index"`

Expected: all four PASS.

- [ ] **Step 6: Run the whole bats suite**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`

Expected: everything passes. The `worktree_prompt_file` tests use `TMUX_STUB_DISPLAY` for a path and fall through to the default branch, so they are unaffected.

- [ ] **Step 7: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/tests/stubs/tmux scripts/scripts/tests/helper.bash scripts/scripts/tests/tmux_lib.bats
git commit -m "test(tmux): key the stub on the query and expose call order

panel_geometry asks for client dimensions and setup_secondary_pane is about
to ask for pane width. One canned display-message answer cannot serve both,
and a mutant asking the wrong question would receive the right answer.

tmux_call_index exists because ordering between two split-window calls is
otherwise unassertable."
```

---

### Task 5: Extract `add_vigil_panel` into `lib/tmux.sh`

**Files:**
- Modify: `lib/tmux.sh` (append the two functions after `claude_pane_target`)
- Modify: `vigil-panel` (reduce to a toggle)
- Test: `tests/tmux_lib.bats`, `tests/vigil_panel.bats`

**Interfaces:**
- Consumes: `tmux_call_index` from Task 4.
- Produces: `panel_geometry` prints `<split-flag> <size>`, e.g. `-hb 40`. `add_vigil_panel <window-target>` splits a panel into that window, marks the pane, and returns non-zero if the split failed.

- [ ] **Step 1: Write the failing tests for the shared function**

Append to `tests/tmux_lib.bats`:

```bash
@test "panel_geometry falls back to a left column with no client" {
  # A session created detached has no client to measure. The arithmetic in
  # the auto branch is an error on an empty string under errexit, so this is
  # a crash, not a wrong answer.
  export TMUX_STUB_DISPLAY=""
  run panel_geometry
  [ "${status}" -eq 0 ]
  [ "${output}" = "-hb 40" ]
}

@test "panel_geometry measures a portrait client" {
  export TMUX_STUB_DISPLAY="40 60"
  run panel_geometry
  [ "${output}" = "-vb 10" ]
}

@test "panel_geometry measures a landscape client" {
  export TMUX_STUB_DISPLAY="40 200"
  run panel_geometry
  [ "${output}" = "-hb 40" ]
}

@test "add_vigil_panel splits the window it is given" {
  export TMUX_STUB_DISPLAY="40 200"
  run add_vigil_panel "=SC-1 demo:claude"
  [ "${status}" -eq 0 ]
  run tmux_call_args "split-window"
  printf '%s\n' "${output}" | assert_arg_after "-t" "=SC-1 demo:claude"
  printf '%s\n' "${output}" | assert_arg_after "-l" "40"
  [[ "${output}" == *"vigil --panel"* ]]
}

@test "add_vigil_panel marks the pane it created" {
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_SPLIT_PANE="%7"
  add_vigil_panel "=SC-1 demo:claude"

  run tmux_call_args_matching "set-option" "@vigil_panel"
  printf '%s\n' "${output}" | grep -Fxq -- "-p"
  printf '%s\n' "${output}" | grep -Fxq -- "%7"
  [ "$(printf '%s\n' "${output}" | tail -n1)" = "1" ]

  run tmux_call_args_matching "set-option" "remain-on-exit"
  printf '%s\n' "${output}" | grep -Fxq -- "-p"
  printf '%s\n' "${output}" | grep -Fxq -- "%7"
  [ "$(printf '%s\n' "${output}" | tail -n1)" = "off" ]
}

@test "add_vigil_panel reports a failed split instead of marking nothing" {
  # errexit is disabled for the whole function when it is called on the left
  # of ||, which every caller does. Without an explicit check the failure
  # falls through and set-option runs against an empty target.
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_SPLIT_FAILS=1
  run add_vigil_panel "=SC-1 demo:claude"
  [ "${status}" -ne 0 ]
  run refute_tmux_subcommand "set-option"
  [ "${status}" -eq 0 ]
}
```

- [ ] **Step 2: Teach the stub to fail a split**

In `tests/stubs/tmux`, replace the `split-window)` case with:

```bash
  split-window)
    if [ -n "${TMUX_STUB_SPLIT_FAILS:-}" ]; then
      # Log it before bailing: a test asserting nothing followed the failure
      # still needs to see that the split was attempted.
      if [ -n "${TMUX_STUB_LOG:-}" ]; then
        printf 'split-window\n' >> "${TMUX_STUB_LOG}"
      fi
      exit 1
    fi
    # Only -P asks for the new pane id back.
    for arg in "${@}"; do
      if [ "${arg}" = "-P" ]; then
        printf '%s\n' "${TMUX_STUB_SPLIT_PANE:-%9}"
        break
      fi
    done
    ;;
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "panel_geometry|add_vigil_panel"`

Expected: all six fail with "command not found".

- [ ] **Step 4: Add the functions to `lib/tmux.sh`**

Insert after `claude_pane_target` (`lib/tmux.sh:91`):

```bash
readonly VIGIL_PANEL_FLAG='@vigil_panel'

#######################################
# Print the split flag and size for the current client.
# Portrait (a vertical monitor) gets a wide strip across the top; anything
# else gets a narrow column on the left.
#
# Geometry is configured with tmux user options rather than vigil's config,
# because placement is tmux's concern and these functions are its only reader:
#   @vigil_panel_orientation  auto (default) | top | left
#   @vigil_panel_size         rows for top (default 10), columns for left (40)
# Outputs:
#   e.g. "-hb 40"
#######################################
panel_geometry() {
  local orientation size height width
  orientation="$(tmux show-options -gqv "@vigil_panel_orientation")"
  size="$(tmux show-options -gqv "@vigil_panel_size")"
  read -r height width <<< "$(tmux display-message -p '#{client_height} #{client_width}')"

  if [ -z "${orientation}" ] || [ "${orientation}" = "auto" ]; then
    if [ -z "${height:-}" ] || [ -z "${width:-}" ]; then
      # No client attached, which is every session created detached. The
      # arithmetic below is a fatal error on an empty string, not a wrong
      # answer, so this branch has to come first.
      orientation="left"
    elif [ "$((height * 2))" -gt "${width}" ]; then
      orientation="top"
    else
      orientation="left"
    fi
  fi

  case "${orientation}" in
    top) printf '%s %s\n' '-vb' "${size:-10}" ;;
    *)   printf '%s %s\n' '-hb' "${size:-40}" ;;
  esac
}

#######################################
# Split a vigil panel into the given window and mark the new pane.
# Arguments:
#   window_target — e.g. "=SC-1 demo:claude"
# Returns:
#   0 on success, 1 if the split failed
#######################################
add_vigil_panel() {
  local window_target="${1}"
  local split size pane
  read -r split size <<< "$(panel_geometry)"

  # Checked explicitly rather than left to errexit: callers invoke this on the
  # left of ||, which disables errexit for the whole function, and a fallen
  # through failure would run set-option against an empty pane id.
  pane="$(tmux split-window -t "${window_target}" "${split}" -l "${size}" \
    -d -P -F '#{pane_id}' "${VIGIL_BIN:-vigil} --panel")" || return 1
  [ -n "${pane}" ] || return 1

  tmux set-option -p -t "${pane}" "${VIGIL_PANEL_FLAG}" 1
  # So a dead panel closes its pane instead of leaving a corpse in the layout.
  tmux set-option -p -t "${pane}" remain-on-exit off
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "panel_geometry|add_vigil_panel"`

Expected: all six PASS.

- [ ] **Step 6: Reduce `vigil-panel` to a toggle**

Replace the whole of `vigil-panel` with:

```bash
#!/usr/bin/env bash
#
# vigil-panel - toggle a vigil session panel in the current tmux window.
#
# tmux decides where the panel goes; vigil renders to fit whatever pane it
# lands in. Bound to prefix p / prefix C-p.
#
# The split itself lives in lib/tmux.sh as add_vigil_panel, because
# create_tmux_session panels new sessions with the same code.

set -o errexit
set -o nounset
set -o pipefail

_script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${_script_dir}/lib/tmux.sh"
unset _script_dir

#######################################
# Print the pane id of this window's panel, if it has one.
# Panes are found by their @vigil_panel marker rather than by position:
# splitting with -b inserts before the existing pane, so every index in the
# window shifts when a panel opens.
#######################################
panel_pane() {
  tmux list-panes -F "#{pane_id} #{${VIGIL_PANEL_FLAG}}" \
    | awk '$2 == "1" { print $1; exit }'
}

main() {
  local existing
  existing="$(panel_pane)"
  if [ -n "${existing}" ]; then
    tmux kill-pane -t "${existing}"
    return 0
  fi
  add_vigil_panel "$(tmux display-message -p '#{window_id}')"
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "${@}"
fi
```

- [ ] **Step 7: Move the split assertions out of `vigil_panel.bats`**

In `tests/vigil_panel.bats`, delete these tests, which now duplicate the `tmux_lib.bats` coverage added in Step 1: "a portrait client gets a strip across the top", "a landscape client gets a column on the left", "the panel runs vigil in panel mode", "the new pane is marked and set to close on exit".

Keep and do not change: "the boundary case counts as landscape", "an orientation option overrides the measurement", "a size option overrides the default", "the split leaves focus where it was", "toggling again kills the existing panel", "a window with panes but no panel still splits". These still exercise the toggle end to end through the real script.

Add the stub's window-id answer so the toggle has a target. In `tests/stubs/tmux`, inside the `display-message)` case, add a branch before the catch-all:

```bash
      *window_id*)
        printf '%s\n' "${TMUX_STUB_WINDOW_ID:-@3}"
        ;;
```

- [ ] **Step 8: Run the whole bats suite**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`

Expected: everything passes. If "the split leaves focus where it was" fails, `-d` was dropped from `add_vigil_panel`'s split.

- [ ] **Step 9: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/vigil-panel scripts/scripts/tests/
git commit -m "refactor(tmux): move the panel split into lib as add_vigil_panel

create_tmux_session is about to create panels too, and the pane markers and
the geometry rule are exactly the things that drift when copied. vigil-panel
keeps only the toggle.

add_vigil_panel takes a window target because it has to panel a session that
is not current, and checks split-window itself because callers invoke it on
the left of ||, which disables errexit for the whole function.

panel_geometry gains a no-client branch: a detached session has no dimensions
to measure and the auto arithmetic is fatal on an empty string."
```

---

### Task 6: `setup_secondary_pane` measures the pane it splits

**Files:**
- Modify: `lib/tmux.sh:121-137`
- Test: `tests/tmux_lib.bats`

**Interfaces:**
- Consumes: the format-keyed stub from Task 4.
- Produces: no signature change. `setup_secondary_pane` queries `#{pane_width}` on the claude pane.

- [ ] **Step 1: Write the failing tests**

Append to `tests/tmux_lib.bats`:

```bash
@test "setup_secondary_pane measures the pane it is about to split" {
  # window_width does not shrink when a 40-column panel appears, but the pane
  # being split does. Measuring the window picks -h for a pane that is really
  # 160 wide.
  export TMUX_STUB_PANE_WIDTH="160"
  setup_secondary_pane "SC-1 demo" "nit"
  run tmux_call_args_matching "display-message" "pane_width"
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"pane_width"* ]]
  run refute_tmux_subcommand_matching "display-message" "window_width"
  [ "${status}" -eq 0 ]
}

@test "a narrow claude pane splits vertically" {
  export TMUX_STUB_PANE_WIDTH="160"
  setup_secondary_pane "SC-1 demo" "nit"
  run tmux_call_args_matching "split-window" "nit"
  [[ "${output}" == *"-v"* ]]
}

@test "a wide claude pane still splits horizontally" {
  export TMUX_STUB_PANE_WIDTH="200"
  setup_secondary_pane "SC-1 demo" "nit"
  run tmux_call_args "split-window"
  [[ "${output}" == *"-h"* ]]
}
```

Add the negative matcher to `tests/helper.bash`:

```bash
# Assert that no invocation of the subcommand also matched the pattern.
# refute_tmux_subcommand is too coarse when a subcommand is used for several
# different queries in one run.
refute_tmux_subcommand_matching() {
  local subcommand="${1}"
  local pattern="${2}"
  ! grep -q -e "^${subcommand}${TMUX_STUB_SEP}.*${pattern}" "${TMUX_STUB_LOG}"
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "measures the pane|narrow claude|wide claude"`

Expected: the first two fail - the code still queries `window_width`, and the stub answers it from `TMUX_STUB_DISPLAY` which defaults to `40 200`, so the split comes out `-h`.

- [ ] **Step 3: Change the query**

In `lib/tmux.sh`, in `setup_secondary_pane`, replace:

```bash
  width="$(tmux display-message -t "=${session}:claude" -p '#{window_width}')"
```

with:

```bash
  width="$(tmux display-message -t "${claude_pane}" -p '#{pane_width}')"
```

Update the function's doc comment: replace "Uses vertical split when terminal is wide enough, horizontal otherwise." with "Splits horizontally when the claude pane itself is at least 200 columns, vertically otherwise. The pane, not the window: a vigil panel takes 40 columns off the pane without changing window_width."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "measures the pane|narrow claude|wide claude"`

Expected: all three PASS.

- [ ] **Step 5: Run the whole bats suite**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`

Expected: everything passes.

- [ ] **Step 6: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/tests/
git commit -m "fix(tmux): split the nit pane on the pane's width, not the window's

window_width does not shrink when a vigil panel takes 40 columns, but the
claude pane it splits does, so the 200-column threshold fired at an effective
160. Only meaningful together with the panel landing before this call."
```

---

### Task 7: `create_tmux_session` creates the panel

**Files:**
- Modify: `lib/tmux.sh:151-198`
- Create: `tests/stubs/vigil`
- Test: `tests/tmux_lib.bats`

**Interfaces:**
- Consumes: `add_vigil_panel` (Task 5), `tmux_call_index` (Task 4), `vigil config get panel_auto` (Task 2).
- Produces: no signature change to `create_tmux_session`.

- [ ] **Step 1: Create the vigil stub**

Create `tests/stubs/vigil`:

```bash
#!/usr/bin/env bash
# Test stub for the vigil binary. Only the config reads create_tmux_session
# makes are answered; anything else is an error, so a new call site cannot
# silently succeed against a canned value.
set -o nounset

if [ "${1:-}" = "config" ] && [ "${2:-}" = "get" ]; then
  case "${3:-}" in
    panel_auto)
      printf '%s\n' "${VIGIL_STUB_PANEL_AUTO-true}"
      exit 0
      ;;
  esac
fi
exit 1
```

Make it executable: `chmod +x tests/stubs/vigil`

- [ ] **Step 2: Write the failing tests**

Append to `tests/tmux_lib.bats`:

```bash
@test "a new session gets a panel in its claude window" {
  export TMUX_STUB_HAS_SESSION=1
  export TMUX_STUB_DISPLAY="40 200"
  create_tmux_session "SC-1 demo" "/tmp/wt" true "" ""
  run tmux_call_args_matching "split-window" "vigil --panel"
  [ "${status}" -eq 0 ]
  printf '%s\n' "${output}" | assert_arg_after "-t" "=SC-1 demo:claude"
}

@test "panel_auto false leaves the session unpanelled" {
  export TMUX_STUB_HAS_SESSION=1
  export TMUX_STUB_DISPLAY="40 200"
  export VIGIL_STUB_PANEL_AUTO="false"
  create_tmux_session "SC-1 demo" "/tmp/wt" true "" ""
  run refute_tmux_subcommand_matching "split-window" "vigil --panel"
  [ "${status}" -eq 0 ]
}

@test "a missing vigil leaves the session unpanelled and working" {
  export TMUX_STUB_HAS_SESSION=1
  export TMUX_STUB_DISPLAY="40 200"
  # Drop the stub directory from PATH so vigil is genuinely absent.
  export PATH="${PATH#"${BATS_TEST_DIRNAME}/stubs:"}"
  export PATH="${BATS_TEST_TMPDIR}/tmuxonly:${PATH}"
  mkdir -p "${BATS_TEST_TMPDIR}/tmuxonly"
  ln -sf "${BATS_TEST_DIRNAME}/stubs/tmux" "${BATS_TEST_TMPDIR}/tmuxonly/tmux"

  run create_tmux_session "SC-1 demo" "/tmp/wt" true "" "claude --model opus"
  [ "${status}" -eq 0 ]
  run refute_tmux_subcommand_matching "split-window" "vigil --panel"
  [ "${status}" -eq 0 ]
  run assert_tmux_subcommand "respawn-pane"
  [ "${status}" -eq 0 ]
}

@test "a failed panel does not abort session creation" {
  export TMUX_STUB_HAS_SESSION=1
  export TMUX_STUB_DISPLAY="40 200"
  export TMUX_STUB_SPLIT_FAILS=1
  run create_tmux_session "SC-1 demo" "/tmp/wt" true "" "claude --model opus"
  [ "${status}" -eq 0 ]
  run assert_tmux_subcommand "respawn-pane"
  [ "${status}" -eq 0 ]
}

@test "the panel is created before the nit split" {
  # Order is the point: setup_secondary_pane measures the claude pane, so the
  # panel's 40 columns must already be gone when it looks. Panel second would
  # leave it reading full width, which is the bug the pane_width change
  # exists to remove.
  export TMUX_STUB_HAS_SESSION=1
  export TMUX_STUB_DISPLAY="40 200"
  create_tmux_session "SC-1 demo" "/tmp/wt" true "nit" "claude --model opus"
  panel_at="$(tmux_call_index "split-window" "vigil --panel")"
  nit_at="$(tmux_call_index "split-window" "nit")"
  claude_at="$(tmux_call_index "respawn-pane" "claude --model opus")"
  [ -n "${panel_at}" ]
  [ "${panel_at}" -lt "${nit_at}" ]
  [ "${nit_at}" -lt "${claude_at}" ]
}

@test "an existing session is not panelled again" {
  export TMUX_STUB_HAS_SESSION=0
  run create_tmux_session "SC-1 demo" "/tmp/wt" true "" ""
  [ "${status}" -eq 3 ]
  run refute_tmux_subcommand "split-window"
  [ "${status}" -eq 0 ]
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "panel in its claude|panel_auto false|missing vigil|failed panel|before the nit|not panelled again"`

Expected: four fail because no panel is created at all. "panel_auto false", "a missing vigil" and "an existing session" pass already - they are the controls, and each must keep passing.

- [ ] **Step 4: Add the panel step**

In `lib/tmux.sh`, in `create_tmux_session`, insert between the `new-window` line (`:178`) and the `pane_command` block (`:179`):

```bash
  if [ "$(vigil config get panel_auto 2> /dev/null)" = "true" ]; then
    # Before setup_secondary_pane, so that split measures a pane the panel
    # has already narrowed. Fail-soft: a panel that cannot be created must
    # never take the session with it.
    add_vigil_panel "=${session_name}:claude" || warn "vigil panel failed"
  fi
```

> **Correction, applied at final review.** Two things this gate gets wrong, plus one this
> brief does not mention at all. `2> /dev/null` silences a present-but-broken vigil as
> thoroughly as an absent one, so the shipped version guards with `command -v` and warns in
> the first case only; and the gate must read `${VIGIL_BIN:-vigil}`, because that is the
> binary `add_vigil_panel` launches. Separately, `new-session -d` sizes its windows to
> `default-size` (80x24) with no client to measure, so the 40-column panel took half an
> 80-column window and tmux scaled it to ~175 columns on attach. `new-session` now takes
> `-x/-y` from `client_dimensions`. See the spec's Decisions section.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "panel in its claude|panel_auto false|missing vigil|failed panel|before the nit|not panelled again"`

Expected: all six PASS.

- [ ] **Step 6: Verify the ordering test is not vacuous**

Move the panel block to after the `pane_command` block, run only the ordering test:

Run: `cd ~/dotfiles/scripts/scripts && bats tests/tmux_lib.bats -f "before the nit"`

Expected: FAIL. If it passes, `tmux_call_index` is returning empty for one of the three lookups and the `-lt` comparisons are not running - check the patterns match the logged argv.

Move the block back. Verify with `git diff` that `lib/tmux.sh` matches Step 4 before continuing.

- [ ] **Step 7: Run the whole bats suite**

Run: `cd ~/dotfiles/scripts/scripts && bats tests/`

Expected: everything passes. The pre-existing `create_tmux_session` tests now also create a panel, which is why Step 1's stub defaults `panel_auto` to `true` rather than making every old test opt in.

- [ ] **Step 8: Commit**

```bash
cd ~/dotfiles
git add scripts/scripts/lib/tmux.sh scripts/scripts/tests/
git commit -m "feat(tmux): panel new sessions by default

create_tmux_session adds a vigil panel to the claude window, gated on
panel_auto in vigil's config. Existing sessions are untouched and prefix p
still works on them.

Before setup_secondary_pane on purpose: that split measures the claude pane,
so the panel's columns have to be gone already. Fail-soft throughout - a
missing vigil, a false setting and a failed split all leave a working
session."
```

---

### Task 8: Install and verify on the real machine

No code. The blockers handoff's lesson governs: prime deliberately, then transition, because a test that produces no event proves nothing about a system that only acts on events, and "nothing happened" looks identical to "the feature is broken".

**Files:** none.

- [ ] **Step 1: Install the new binary**

```bash
cd ~/vigil && make install
```

`make install` writes via a temp file and renames, never in place. Overwriting a running image's inode invalidates its code signature and macOS then SIGKILLs every later exec of that path.

- [ ] **Step 2: Restart the running daemon**

```bash
pkill -f 'vigil daemon' || true
```

This is not optional. Task 3 changes when a client owns transition effects, but a daemon started from the old image is still the old code. Verifying against it measures nothing.

- [ ] **Step 3: Confirm the subcommand works against the real binary**

```bash
vigil config get panel_auto
vigil config get no_such_setting; echo "exit=${?}"
```

Expected: `true`, then nothing on stdout and `exit=1`.

- [ ] **Step 4: Verify a cold-start dispatch fires the notify hook once**

Kill every daemon first, then dispatch a story and let the new session's panel start the daemon. Let the session settle into a known state before causing a transition - a bell flag left set from a previous run makes the detector prime at `Attention`, so no transition occurs and the result is a false zero.

Count `notify` invocations for the first real transition.

Expected: **1**. Two means the grace period is not being armed; zero means either no transition happened or the daemon is still the old image, so check Step 2 before concluding anything.

- [ ] **Step 5: Verify `panel_auto = false`**

Set `panel_auto = false` under `[settings]` in `~/.config/vigil/config.toml`, create a session, confirm no panel and that everything else is unchanged. Then remove the line.

- [ ] **Step 6: Eyeball the nit split**

Create a session at your normal terminal width and look at whether the nit pane split horizontally or vertically. This is the one change here you will feel every day. If the new orientation is wrong for your setup, the fix is the 200 constant in `setup_secondary_pane`, not a revert.

- [ ] **Step 7: Verify the toggle still toggles**

`prefix p` on a session that already has a panel must kill it, and again must bring it back. This is the path Task 5 refactored.

- [ ] **Step 8: Record the results**

Write a handoff at `docs/superpowers/2026-07-29-phase-3-handoff.md` covering what landed, what was verified and what was not, and anything the run turned up. Write it before deleting any working notes, and re-check it against HEAD if a commit lands after it does. The phase 2 handoff exists only because someone reconstructed it before the session ended.

```bash
cd ~/vigil && git add docs/superpowers/ && git commit -m "docs: record the phase 3 verification results"
```

---

## Self-Review

**Spec coverage.** Every section maps to a task: `add_vigil_panel` and `panel_geometry` to Task 5; `vigil-panel` as a thin toggle to Task 5 Steps 6-7; the `create_tmux_session` panel step to Task 7; `setup_secondary_pane` to Task 6; ordering to Task 7 Steps 4 and 6; `vigil config get` to Task 2; the ownership window to Task 3; the harness gaps to Task 4; the failure-handling table to Task 7's four negative tests plus Task 5's failed-split test; real-machine verification to Task 8.

Two spec rows are covered indirectly and are called out here rather than left implicit. "Daemon spawn fails" is covered by `TestAPanelOwnsEffectsOnceTheGraceExpires` rather than by a test that fails a spawn, which is the same property reached more cheaply. "Worktree removed under a panel" is pre-existing behaviour with no change and therefore no test.

**Interface consistency.** `add_vigil_panel` takes one window-target argument in Tasks 5, 7 and in `vigil-panel`. `panel_geometry` prints `<flag> <size>` and is read with the same `read -r split size` in both callers. `tmux_call_index` takes `(subcommand, pattern)`, matching `tmux_call_args_matching`. `run(args, stdout, stderr) int` and `parseArgs(args) (string, []string, error)` are used identically in Tasks 1 and 2. `spawnGrace`, `effectsDisownedUntil` and `effectsDisowned()` are named the same in the field, the helper and all six tests.

**Known sharp edges carried from the handoff.** Task 3 Step 6 and Task 7 Step 6 both mutate production code to prove a test is not vacuous. A `git checkout --` that discards an uncommitted fix is a documented way work has been lost in this repo; both steps say to commit first and to check `git diff` after restoring. The repo's ctags `post-checkout` hook also drops `NNNNN.tags` files that `.gitignore` does not cover, so a `<system-reminder>` about modified files after a checkout is that hook, not an injection.

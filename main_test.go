package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/model"
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
		{"panel flag", []string{"--panel"}, "panel"},
		{"dispatch subcommand", []string{"dispatch", "sc-1"}, "dispatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := parseArgs(tc.args)
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
	got, _, err := parseArgs([]string{"--bogus"})
	if err == nil {
		t.Fatalf("want an error, got command %q", got)
	}
	if !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("error %q should name the offending argument", err)
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

// Every test that reaches config.Load needs its own HOME. ConfigPath resolves
// under os.UserHomeDir, so without this the suite reads the developer's real
// ~/.config/vigil/config.toml - and a developer who sets panel_auto = "false"
// there, which is the documented way to turn the panel off, turns the repo's
// own suite red.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// The dependency check must not run before config get. A bash caller that
// receives "gh not found" instead of a value would silently disable the panel
// on any machine mid-setup, which looks identical to panel_auto = false.
func TestConfigGetAnswersWithoutTheDependencies(t *testing.T) {
	isolateConfig(t)
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
	isolateConfig(t)
	t.Setenv("VIGIL_PANEL_AUTO", "false")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "get", "panel_auto"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != "false" {
		t.Errorf("got stdout %q, want false", stdout.String())
	}
}

func TestDispatchParsesAsItsOwnCommand(t *testing.T) {
	command, rest, err := parseArgs([]string{"dispatch", "--cwd", "/tmp", "sc-1"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if command != "dispatch" {
		t.Errorf("got %q, want dispatch", command)
	}
	if len(rest) != 3 || rest[2] != "sc-1" {
		t.Errorf("got rest %q", rest)
	}
}

// The hook the design specifies is DISPATCH_INLINE=1 dispatch
// --non-interactive {flags} {input}. A hook that still passes --detached
// skips the teleport that is the whole point of a dispatch, and one still
// keyed on DISPATCH_IN_POPUP runs tmux display-popup -E from a daemon with no
// client, which cannot draw. Neither says anything on the way past, so vigil
// does - and names the hook with {flags}, not the pre-queue one, so a user
// who migrates off this warning is not immediately told to migrate again.
func TestAnUnmigratedDispatchHookIsWarnedAbout(t *testing.T) {
	for _, hook := range []string{
		"dispatch --detached --non-interactive {input}",
		"DISPATCH_IN_POPUP=1 dispatch --non-interactive {input}",
	} {
		cfg := &config.Config{Hooks: map[string]any{"dispatch": hook}}
		var stderr bytes.Buffer
		warnAboutAnUnmigratedDispatchHook(cfg, &stderr)
		if !strings.Contains(stderr.String(), "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}") {
			t.Errorf("hook %q got %q, want the migrated hook named", hook, stderr.String())
		}
	}
}

// TestAMigratedDispatchHookIsNotWarnedAbout pins the fully-migrated hook -
// DISPATCH_INLINE, no --detached, and {flags} - against both warnings this
// function can emit: neither the phase-4 stale-flag check nor the {flags}
// check added in this task has anything to say about it.
func TestAMigratedDispatchHookIsNotWarnedAbout(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]any{
		"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}",
	}}
	var stderr bytes.Buffer
	warnAboutAnUnmigratedDispatchHook(cfg, &stderr)
	if stderr.String() != "" {
		t.Errorf("got %q, want silence", stderr.String())
	}
}

func TestWarnsWhenTheDispatchHookHasNoFlagsPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{Hooks: map[string]any{
		"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {input}",
	}}

	warnAboutAnUnmigratedDispatchHook(cfg, &buf)

	if !strings.Contains(buf.String(), "{flags}") {
		t.Errorf("stderr = %q, want a warning naming {flags}", buf.String())
	}
}

// The warning is only useful if run actually emits it, which the unit tests
// above cannot show. Fake binaries satisfy the dependency check so the run
// reaches the config; `dispatch` with no input then fails its own usage check
// before anything is submitted or spawned.
func TestRunEmitsTheHookWarningBeforeItDoesAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "vigil"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "vigil", "config.toml"),
		[]byte("[hooks]\ndispatch = \"dispatch --detached --non-interactive {input}\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bin := t.TempDir()
	for _, dep := range []string{"tmux", "git", "gh"} {
		if err := os.WriteFile(filepath.Join(bin, dep), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	t.Setenv("PATH", bin)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"dispatch"}, &stdout, &stderr); code != 1 {
		t.Fatalf("got exit %d and stderr %q, want 1 from the usage error", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--detached") {
		t.Errorf("got %q, want the unmigrated hook warning", stderr.String())
	}
}

// Unlike `config get`, dispatch runs after the dependency check: it needs all
// three binaries, and a queued job is worse than an early error.
func TestDispatchRunsAfterTheDependencyCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"dispatch", "sc-1"}, &stdout, &stderr); code != 1 {
		t.Errorf("got exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found in PATH") {
		t.Errorf("got %q, want a dependency error", stderr.String())
	}
}

// fakeFinalModel stands in for tea.Program's final model without dragging in
// a real Model, so these tests exercise restartIfRequested's own logic rather
// than model.Model's.
type fakeFinalModel struct {
	restart bool
}

func (f fakeFinalModel) Init() tea.Cmd                       { return nil }
func (f fakeFinalModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return f, nil }
func (f fakeFinalModel) View() string                        { return "" }
func (f fakeFinalModel) RestartRequested() bool              { return f.restart }

func TestRestartIfRequestedExecsTheSamePathAndArgv(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })

	var gotPath string
	var gotArgv []string
	var gotEnv []string
	execSelf = func(path string, argv []string, envv []string) error {
		gotPath = path
		gotArgv = argv
		gotEnv = envv
		return nil
	}

	if err := restartIfRequested(fakeFinalModel{restart: true}); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if gotPath != exe {
		t.Fatalf("exec path = %q, want %q", gotPath, exe)
	}
	if len(gotArgv) == 0 || gotArgv[0] != exe {
		t.Fatalf("argv = %v, want argv[0] to be the executable", gotArgv)
	}
	if !reflect.DeepEqual(gotArgv[1:], os.Args[1:]) {
		t.Fatalf("argv[1:] = %v, want %v", gotArgv[1:], os.Args[1:])
	}
	if len(gotEnv) != len(os.Environ()) {
		t.Fatalf("envv has %d entries, want %d (os.Environ() passed through)", len(gotEnv), len(os.Environ()))
	}
}

func TestRestartIfRequestedDoesNothingWithoutTheFlag(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })
	execSelf = func(string, []string, []string) error {
		t.Fatal("exec'd without a restart request")
		return nil
	}
	if err := restartIfRequested(fakeFinalModel{restart: false}); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}
}

func TestRestartIfRequestedIgnoresANonModel(t *testing.T) {
	original := execSelf
	t.Cleanup(func() { execSelf = original })
	execSelf = func(string, []string, []string) error {
		t.Fatal("exec'd for a model of the wrong type")
		return nil
	}
	if err := restartIfRequested(nil); err != nil {
		t.Fatalf("restartIfRequested: %v", err)
	}
}

// TestTheRealModelSatisfiesRestartRequester is a compile-time assertion: if
// model.Model ever stops satisfying restartRequester, this file fails to
// compile rather than letting restartIfRequested silently stop firing in
// production while every test above keeps passing against the fake.
func TestTheRealModelSatisfiesRestartRequester(t *testing.T) {
	var _ restartRequester = model.Model{}
}

// TestShortIsNotAStartupDependency pins the deliberate asymmetry: vigil must
// keep working for anyone without Shortcut installed. A missing short leaves
// the story half of the queue empty, which is a degraded feature; adding it to
// this list would make it a refusal to start.
func TestShortIsNotAStartupDependency(t *testing.T) {
	for _, dep := range startupDependencies {
		if dep == "short" {
			t.Fatal("short must not be a startup dependency: vigil has to run without Shortcut")
		}
	}
}

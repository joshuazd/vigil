package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/config"
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
// --non-interactive {input}. A hook that still passes --detached skips the
// teleport that is the whole point of a dispatch, and one still keyed on
// DISPATCH_IN_POPUP runs tmux display-popup -E from a daemon with no client,
// which cannot draw. Neither says anything on the way past, so vigil does.
func TestAnUnmigratedDispatchHookIsWarnedAbout(t *testing.T) {
	for _, hook := range []string{
		"dispatch --detached --non-interactive {input}",
		"DISPATCH_IN_POPUP=1 dispatch --non-interactive {input}",
	} {
		cfg := &config.Config{Hooks: map[string]any{"dispatch": hook}}
		var stderr bytes.Buffer
		warnAboutAnUnmigratedDispatchHook(cfg, &stderr)
		if !strings.Contains(stderr.String(), "DISPATCH_INLINE=1 dispatch --non-interactive {input}") {
			t.Errorf("hook %q got %q, want the migrated hook named", hook, stderr.String())
		}
	}
}

func TestAMigratedDispatchHookIsNotWarnedAbout(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]any{
		"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {input}",
	}}
	var stderr bytes.Buffer
	warnAboutAnUnmigratedDispatchHook(cfg, &stderr)
	if stderr.String() != "" {
		t.Errorf("got %q, want silence", stderr.String())
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

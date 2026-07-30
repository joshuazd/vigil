package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
)

func TestMissingFileReturnsEmptyConfig(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Settings) > 0 {
		t.Error("expected empty settings")
	}
}

func TestValidTOMLParsed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(p, []byte("[hooks]\nmerge = \"custom merge\"\n"), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GetHook("merge") != "custom merge" {
		t.Errorf("got %q, want %q", cfg.GetHook("merge"), "custom merge")
	}
}

func TestInvalidTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(p, []byte("not valid [[[ toml"), 0o644)
	cfg, err := Load(p)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
	if len(cfg.Settings) > 0 {
		t.Error("expected empty settings for invalid TOML")
	}
}

// --- GetHook tests ---

func TestHookReturnsConfigured(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "my-merge {branch}"}}
	if cfg.GetHook("merge") != "my-merge {branch}" {
		t.Errorf("got %q", cfg.GetHook("merge"))
	}
}

func TestHookReturnsDefaultForMerge(t *testing.T) {
	cfg := &Config{}
	if cfg.GetHook("merge") != "gh pr merge {branch} --squash --delete-branch" {
		t.Errorf("got %q", cfg.GetHook("merge"))
	}
}

func TestHookReturnsDefaultForApprove(t *testing.T) {
	cfg := &Config{}
	if cfg.GetHook("approve") != "gh pr review {branch} --approve" {
		t.Errorf("got %q", cfg.GetHook("approve"))
	}
}

func TestHookReturnsEmptyForUnconfigured(t *testing.T) {
	cfg := &Config{}
	if cfg.GetHook("cleanup") != "" {
		t.Errorf("got %q, want empty", cfg.GetHook("cleanup"))
	}
	if cfg.GetHook("dispatch") != "" {
		t.Errorf("got %q, want empty", cfg.GetHook("dispatch"))
	}
}

func TestHookEmptyStringDisables(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": ""}}
	if cfg.GetHook("merge") != "" {
		t.Errorf("got %q, want empty", cfg.GetHook("merge"))
	}
}

// --- ExpandHook tests ---

func TestExpandBasicSubstitution(t *testing.T) {
	result, err := ExpandHook("echo {branch}", map[string]string{"branch": "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "echo 'feat'" {
		t.Errorf("got %q", result)
	}
}

func TestExpandShellEscapesSpaces(t *testing.T) {
	result, err := ExpandHook("echo {msg}", map[string]string{"msg": "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "echo 'hello world'" {
		t.Errorf("got %q", result)
	}
}

func TestExpandShellEscapesSemicolons(t *testing.T) {
	result, err := ExpandHook("echo {msg}", map[string]string{"msg": "a; rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	// Semicolon must be inside quotes
	if result != "echo 'a; rm -rf /'" {
		t.Errorf("got %q", result)
	}
}

func TestExpandUnknownPlaceholderErrors(t *testing.T) {
	_, err := ExpandHook("echo {missing}", map[string]string{"branch": "feat"})
	if err == nil {
		t.Error("expected error for unknown placeholder")
	}
}

// --- GetSetting tests ---

func TestGetSettingDefault(t *testing.T) {
	cfg := &Config{}
	if cfg.GetSetting("git_interval") != "3" {
		t.Errorf("got %q, want 3", cfg.GetSetting("git_interval"))
	}
}

func TestGetSettingDefaultTmuxInterval(t *testing.T) {
	cfg := &Config{}
	if cfg.GetSetting("tmux_interval") != "1" {
		t.Errorf("got %q, want 1", cfg.GetSetting("tmux_interval"))
	}
}

func TestGetSettingEnvOverridesTmuxInterval(t *testing.T) {
	cfg := &Config{}
	t.Setenv("VIGIL_TMUX_INTERVAL", "2")
	if cfg.GetSetting("tmux_interval") != "2" {
		t.Errorf("got %q, want 2", cfg.GetSetting("tmux_interval"))
	}
}

func TestGetSettingFromTOML(t *testing.T) {
	cfg := &Config{Settings: map[string]any{"git_interval": int64(5)}}
	if cfg.GetSetting("git_interval") != "5" {
		t.Errorf("got %q, want 5", cfg.GetSetting("git_interval"))
	}
}

func TestGetSettingEnvOverridesToml(t *testing.T) {
	cfg := &Config{Settings: map[string]any{"git_interval": int64(5)}}
	t.Setenv("VIGIL_GIT_INTERVAL", "10")
	if cfg.GetSetting("git_interval") != "10" {
		t.Errorf("got %q, want 10", cfg.GetSetting("git_interval"))
	}
}

func TestGetSettingEnvOverridesDefault(t *testing.T) {
	cfg := &Config{}
	t.Setenv("VIGIL_LOG_LEVEL", "DEBUG")
	if cfg.GetSetting("log_level") != "DEBUG" {
		t.Errorf("got %q, want DEBUG", cfg.GetSetting("log_level"))
	}
}

// --- RunHook tests ---

func TestRunHookGoesThroughCommander(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "echo {branch}"}}
	cmd := fetch.NewMockCommander()

	_, _ = cfg.RunHook(context.Background(), cmd, "merge", map[string]string{"branch": "feat"}, "", 0)

	if len(cmd.Calls) != 1 {
		t.Fatalf("expected 1 recorded call, got %d", len(cmd.Calls))
	}
	call := cmd.Calls[0]
	if call.Name != "sh" {
		t.Errorf("got Name %q, want %q", call.Name, "sh")
	}
	if len(call.Args) < 2 || call.Args[0] != "-c" {
		t.Fatalf("expected Args[0] == -c, got %v", call.Args)
	}
	if !strings.Contains(call.Args[1], "echo 'feat'") {
		t.Errorf("expected script to contain expanded template, got %q", call.Args[1])
	}
}

func TestRunHookRedirectsStderrBeforeBody(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "echo {branch}"}}
	cmd := fetch.NewMockCommander()

	_, _ = cfg.RunHook(context.Background(), cmd, "merge", map[string]string{"branch": "feat"}, "", 0)

	script := cmd.Calls[0].Args[1]
	prefixIdx := strings.Index(script, "exec 2>&1;")
	bodyIdx := strings.Index(script, "echo 'feat'")
	if prefixIdx == -1 {
		t.Fatalf("expected script to contain 'exec 2>&1;', got %q", script)
	}
	if bodyIdx == -1 || bodyIdx < prefixIdx {
		t.Errorf("expected 'exec 2>&1;' to precede the hook body, got %q", script)
	}
}

func TestRunHookPassesCwdAsDir(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "echo hi"}}
	cmd := fetch.NewMockCommander()

	_, _ = cfg.RunHook(context.Background(), cmd, "merge", map[string]string{}, "/some/dir", 0)

	if len(cmd.Calls) != 1 {
		t.Fatalf("expected 1 recorded call, got %d", len(cmd.Calls))
	}
	if cmd.Calls[0].Dir != "/some/dir" {
		t.Errorf("got Dir %q, want %q", cmd.Calls[0].Dir, "/some/dir")
	}
}

func TestRunHookNoTemplateSkipsCommander(t *testing.T) {
	cfg := &Config{}
	cmd := fetch.NewMockCommander()

	_, err := cfg.RunHook(context.Background(), cmd, "cleanup", map[string]string{}, "", 0)

	var notConfigured *HookNotConfigured
	if !errors.As(err, &notConfigured) {
		t.Fatalf("got %T (%v), want *HookNotConfigured", err, err)
	}
	if len(cmd.Calls) != 0 {
		t.Errorf("expected 0 recorded calls, got %d", len(cmd.Calls))
	}
}

// TestRunHookCapturesStderr pins the property `exec 2>&1` exists for, rather
// than the presence of that string in the script. Uses the real commander
// because a mock cannot prove a shell redirect works.
func TestRunHookCapturesStderr(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"notify": "echo to-stderr >&2"}}

	out, err := cfg.RunHook(context.Background(), &fetch.ExecCommander{}, "notify", map[string]string{}, "", 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if !strings.Contains(out, "to-stderr") {
		t.Fatalf("got %q, want the hook's stderr captured in the output", out)
	}
}

// TestRunHookCapturesStderrWhenTheHookFails is the MergePR recovery contract
// end to end: gh writes "merged" to stderr and exits 1, and MergePR reads that
// to tell a real failure from a successful merge whose branch cleanup failed.
func TestRunHookCapturesStderrWhenTheHookFails(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "echo pull request merged >&2; exit 1"}}

	out, err := cfg.RunHook(context.Background(), &fetch.ExecCommander{}, "merge", map[string]string{}, "", 5*time.Second)
	if err == nil {
		t.Fatal("want an error from a hook that exits 1")
	}
	if !strings.Contains(out, "merged") {
		t.Fatalf("got out %q, want the stderr text MergePR recovers on", out)
	}
	if !strings.Contains(err.Error(), "merged") {
		t.Fatalf("got err %v, want the output embedded in the error too", err)
	}
}

// TestRunHookStderrOrderingSurvivesACompoundHook covers why the redirect is
// `exec 2>&1;` and not a trailing " 2>&1": a trailing redirect would attach to
// the last clause only, so an earlier clause's stderr would be lost.
func TestRunHookStderrOrderingSurvivesACompoundHook(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"merge": "echo first >&2 && echo second"}}

	out, err := cfg.RunHook(context.Background(), &fetch.ExecCommander{}, "merge", map[string]string{}, "", 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if !strings.Contains(out, "first") {
		t.Fatalf("got %q, want the first clause's stderr - a trailing 2>&1 would drop it", out)
	}
	if !strings.Contains(out, "second") {
		t.Fatalf("got %q, want the second clause's stdout too", out)
	}
}

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

func TestRunHookStreamExpandsAndStreams(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": "printf '>>> fetching %s\\n>>> done\\n' {input}",
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		map[string]string{"input": "sc-12345"}, "", nil, 5*time.Second,
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	want := []string{">>> fetching sc-12345", ">>> done"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// stderr is load-bearing: warn and error in lib/output.sh write there, and a
// failure reason arriving on stderr must reach the status line.
func TestRunHookStreamMergesStderr(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": "printf '!!! broke\\n' >&2",
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, 5*time.Second, func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	if len(got) != 1 || got[0] != "!!! broke" {
		t.Errorf("got %q, want [\"!!! broke\"]", got)
	}
}

func TestRunHookStreamPassesEnv(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"dispatch": `printf '%s\n' "$VIGIL_CLIENT"`,
	}}
	var got []string
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", []string{"VIGIL_CLIENT=/dev/ttys009"}, 5*time.Second,
		func(line string) { got = append(got, line) })
	if err != nil {
		t.Fatalf("RunHookStream: %v", err)
	}
	if len(got) != 1 || got[0] != "/dev/ttys009" {
		t.Errorf("got %q, want the client", got)
	}
}

func TestRunHookStreamUnconfigured(t *testing.T) {
	cfg := &Config{}
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, time.Second, func(string) {})
	if !errors.As(err, new(*HookNotConfigured)) {
		t.Errorf("got %v, want HookNotConfigured", err)
	}
}

func TestRunHookStreamHonoursTheTimeout(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{"dispatch": "sleep 5"}}
	err := cfg.RunHookStream(context.Background(), &fetch.ExecCommander{}, "dispatch",
		nil, "", nil, 50*time.Millisecond, func(string) {})
	if err == nil {
		t.Fatal("got nil, want a timeout error")
	}
}

func TestDispatchTimeoutDefaultsTo300s(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &Config{}
	if got := cfg.GetSettingDuration("dispatch_timeout"); got != 300*time.Second {
		t.Errorf("got %v, want 5m0s", got)
	}
	if !IsSetting("dispatch_timeout") {
		t.Error("dispatch_timeout is not a known setting")
	}
}

// RunHook and RunHookStream share hookArgv. This pins that the sharing did not
// change RunHook's contract: its output is trimmed and stderr is merged.
func TestRunHookStillTrimsAndMergesAfterTheRefactor(t *testing.T) {
	cfg := &Config{Hooks: map[string]any{
		"notify": "printf 'out\\n'; printf 'err\\n' >&2",
	}}
	out, err := cfg.RunHook(context.Background(), &fetch.ExecCommander{}, "notify",
		nil, "", 5*time.Second)
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if out != "out\nerr" {
		t.Errorf("got %q, want \"out\\nerr\"", out)
	}
}

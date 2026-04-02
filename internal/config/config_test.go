package config

import (
	"os"
	"path/filepath"
	"testing"
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

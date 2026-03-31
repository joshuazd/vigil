package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// HookNotConfigured is returned when a hook has no configuration and no default.
type HookNotConfigured struct {
	Name string
}

func (e *HookNotConfigured) Error() string {
	return fmt.Sprintf("%s hook not configured", e.Name)
}

// settingDef maps a setting name to its env var and default value.
type settingDef struct {
	EnvVar  string
	Default string
}

var settingDefaults = map[string]settingDef{
	"git_interval":          {"VIGIL_GIT_INTERVAL", "3"},
	"pr_interval":           {"VIGIL_PR_INTERVAL", "30"},
	"cache_ttl":             {"VIGIL_CACHE_TTL", "30"},
	"log_level":             {"VIGIL_LOG_LEVEL", "INFO"},
	"git_workers":           {"VIGIL_GIT_WORKERS", "8"},
	"capture_window":        {"VIGIL_CAPTURE_WINDOW", ""},
	"stale_threshold":       {"VIGIL_STALE_THRESHOLD", "86400"},
	"notifications_enabled": {"VIGIL_NOTIFICATIONS", "true"},
	"auto_cleanup":          {"VIGIL_AUTO_CLEANUP", "false"},
	"auto_focus":            {"VIGIL_AUTO_FOCUS", "true"},
}

var hookDefaults = map[string]string{
	"merge":   "gh pr merge {branch} --squash --delete-branch",
	"approve": "gh pr review {branch} --approve",
	"notify":  `tmux display-message -d 5000 "vigil: {session} → {new_state}"`,
}

// Config holds the loaded TOML configuration.
type Config struct {
	Settings map[string]any `toml:"settings"`
	Hooks    map[string]any `toml:"hooks"`
}

// ConfigPath returns the default config file path.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vigil", "config.toml")
}

// Load reads and parses the config file. Returns an empty Config on error.
func Load(path string) *Config {
	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return &Config{}
	}
	return cfg
}

// GetSetting returns a setting value. Priority: env var > TOML > default.
func (c *Config) GetSetting(name string) string {
	def, ok := settingDefaults[name]
	if !ok {
		return ""
	}
	if v, ok := os.LookupEnv(def.EnvVar); ok {
		return v
	}
	if c.Settings != nil {
		if v, ok := c.Settings[name]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return def.Default
}

// GetSettingInt returns a setting as an int, falling back to 0.
func (c *Config) GetSettingInt(name string) int {
	n, _ := strconv.Atoi(c.GetSetting(name))
	return n
}

// GetSettingBool returns a setting as a bool.
func (c *Config) GetSettingBool(name string) bool {
	return c.GetSetting(name) == "true"
}

// GetSettingDuration returns a setting as a time.Duration in seconds.
func (c *Config) GetSettingDuration(name string) time.Duration {
	return time.Duration(c.GetSettingInt(name)) * time.Second
}

// GetHook returns the hook command template, or "" if disabled/unconfigured.
func (c *Config) GetHook(name string) string {
	if c.Hooks != nil {
		if v, ok := c.Hooks[name]; ok {
			s := fmt.Sprintf("%v", v)
			if s == "" {
				return "" // empty string = disabled
			}
			return s
		}
	}
	if d, ok := hookDefaults[name]; ok {
		return d
	}
	return ""
}

// ExpandHook replaces {placeholders} with shell-escaped values.
func ExpandHook(template string, vars map[string]string) (string, error) {
	result := template
	// Find all {placeholder} patterns and replace
	for {
		start := strings.Index(result, "{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end == -1 {
			break
		}
		end += start
		key := result[start+1 : end]
		val, ok := vars[key]
		if !ok {
			return "", fmt.Errorf("unknown placeholder in hook template: {%s}", key)
		}
		result = result[:start] + shellQuote(val) + result[end+1:]
	}
	return result, nil
}

// RunHook expands and executes a hook command. Returns stdout.
func (c *Config) RunHook(name string, vars map[string]string, cwd string, timeout time.Duration) (string, error) {
	template := c.GetHook(name)
	if template == "" {
		return "", &HookNotConfigured{Name: name}
	}
	cmdStr, err := ExpandHook(template, vars)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("hook %s failed: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// shellQuote wraps a string in single quotes for safe shell usage.
func shellQuote(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jzinkduda/vigil/internal/fetch"
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
	"tmux_interval":         {"VIGIL_TMUX_INTERVAL", "1"},
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
	"panel_auto":            {"VIGIL_PANEL_AUTO", "true"},
	"dispatch_timeout":      {"VIGIL_DISPATCH_TIMEOUT", "300"},
	"queue_enabled":         {"VIGIL_QUEUE_ENABLED", "true"},
	"queue_pr_query":        {"VIGIL_QUEUE_PR_QUERY", "review-requested:@me -is:draft"},
	"queue_pr_age_days":     {"VIGIL_QUEUE_PR_AGE_DAYS", "14"},
	"queue_story_query":     {"VIGIL_QUEUE_STORY_QUERY", "owner:%self% !is:done !is:archived"},
	"queue_interval":        {"VIGIL_QUEUE_INTERVAL", "60"},
	"queue_limit":           {"VIGIL_QUEUE_LIMIT", "20"},
}

var hookDefaults = map[string]string{
	"merge":   "gh pr merge {branch} --squash --delete-branch",
	"approve": "gh pr review {branch} --approve",
	// notify's quoting looks wrong and is not. ExpandHook substitutes each
	// placeholder as one shell-quoted word, so a placeholder inside a larger
	// double-quoted string lands as '...' within "..." - and a session name
	// containing a double quote (dotfiles' session_name_from_title produces
	// them) closes that string early, splitting the message into two arguments
	// that tmux display-message refuses. Closing the literal before each
	// placeholder and reopening after lets the shell concatenate the pieces
	// into the single argument tmux wants. Do not rewrite this as
	// "vigil: {session} → {new_state}"; that form has never worked.
	"notify": `tmux display-message -d 5000 "vigil: "{session}" → "{new_state}`,
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

// Load reads and parses the config file. Returns an empty Config and an error
// if the file exists but cannot be parsed.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return &Config{}, fmt.Errorf("config parse error: %w", err)
	}
	return cfg, nil
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

// IsSetting reports whether name is a known setting. Callers need this to
// tell an unknown key from a setting whose value is legitimately empty.
func IsSetting(name string) bool {
	_, ok := settingDefaults[name]
	return ok
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

// rawPlaceholders are substituted without shell quoting. Every other
// placeholder carries a value from tmux, git, gh or the user, and quoting is
// what stops it reaching sh as syntax. {flags} carries one of exactly two
// constants chosen by vigil - "" or "--detached" - and quoting it would pass
// a stray empty argument to the hook.
var rawPlaceholders = map[string]bool{"flags": true}

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
		sub := val
		if !rawPlaceholders[key] {
			sub = shellQuote(val)
		}
		result = result[:start] + sub + result[end+1:]
	}
	return result, nil
}

// hookArgv builds the argv both hook runners use. Shared so the two cannot
// drift on quoting or on the stderr merge, which MergePR depends on: it
// searches hook output for "merged", and gh writes that to stderr. `exec 2>&1;`
// redirects the whole script regardless of its structure, unlike appending
// " 2>&1" to the command, which would only redirect its last clause.
func (c *Config) hookArgv(name string, vars map[string]string) ([]string, error) {
	template := c.GetHook(name)
	if template == "" {
		return nil, &HookNotConfigured{Name: name}
	}
	cmdStr, err := ExpandHook(template, vars)
	if err != nil {
		return nil, err
	}
	return []string{"sh", "-c", "exec 2>&1; " + cmdStr}, nil
}

// RunHook expands and executes a hook command. Returns stdout.
func (c *Config) RunHook(ctx context.Context, cmd fetch.Commander, name string, vars map[string]string, cwd string, timeout time.Duration) (string, error) {
	argv, err := c.hookArgv(name, vars)
	if err != nil {
		return "", err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, err := cmd.Run(ctx, cwd, argv[0], argv[1:]...)
	if err != nil {
		return strings.TrimSpace(out), fmt.Errorf("hook %s failed: %w (output: %s)", name, err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// RunHookStream runs a hook and delivers its output a line at a time. Used by
// the daemon's job runner, where a dispatch takes long enough that its last
// line is the only progress a user gets.
//
// onLine is called from RunStream's scanner goroutine, not this one.
func (c *Config) RunHookStream(
	ctx context.Context,
	sc fetch.StreamCommander,
	name string,
	vars map[string]string,
	cwd string,
	env []string,
	timeout time.Duration,
	onLine func(string),
) error {
	argv, err := c.hookArgv(name, vars)
	if err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err = sc.RunStream(ctx, cwd, env, argv[0], argv[1:], onLine)
	// The deadline is authoritative; the child's exit status is not. A killed
	// hook reports "signal: killed", which is indistinguishable from any other
	// signal, so a caller that branched on the exit error could never tell a
	// timeout from a crash - and reported the job's last stale progress line
	// as the reason. This asks the context that imposed the deadline whether
	// it actually expired.
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("hook %s: %w", name, context.DeadlineExceeded)
	}
	return err
}

// shellQuote wraps a string in single quotes for safe shell usage.
func shellQuote(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

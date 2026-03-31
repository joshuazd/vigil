package fetch

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// RawSession is an intermediate struct from tmux list-panes output.
type RawSession struct {
	Name     string
	PanePath string
	Created  int64
}

// ListSessions returns tmux sessions sorted by creation time, deduplicated by name.
func ListSessions(ctx context.Context, cmd Commander) ([]RawSession, error) {
	out, err := cmd.Run(ctx, "", "tmux", "list-panes", "-a",
		"-F", "#{session_created}|#{session_name}|#{pane_current_path}")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)

	seen := make(map[string]bool)
	var sessions []RawSession
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		name := parts[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		created, _ := strconv.ParseInt(parts[0], 10, 64)
		sessions = append(sessions, RawSession{
			Name:     name,
			PanePath: parts[2],
			Created:  created,
		})
	}
	return sessions, nil
}

// CurrentSession returns the name of the current tmux session.
// Uses TMUX_PANE for fast lookup when available, falls back to display-message.
func CurrentSession(ctx context.Context, cmd Commander) string {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		out, err := cmd.Run(ctx, "", "tmux", "display-message", "-t", pane, "-p", "#{session_name}")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}
	out, err := cmd.Run(ctx, "", "tmux", "display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// LastSession returns the most recently active session other than current.
func LastSession(ctx context.Context, cmd Commander, current string) string {
	out, err := cmd.Run(ctx, "", "tmux", "list-sessions",
		"-F", "#{session_activity}|#{session_name}")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 && parts[1] != current {
			return parts[1]
		}
	}
	return ""
}

// BellFlags returns a map of session names that have bell flags active.
func BellFlags(ctx context.Context, cmd Commander) map[string]bool {
	out, err := cmd.Run(ctx, "", "tmux", "list-windows", "-a",
		"-F", "#{session_name}|#{window_bell_flag}")
	if err != nil {
		return nil
	}
	bells := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 && parts[1] == "1" {
			bells[parts[0]] = true
		}
	}
	return bells
}

// CapturePane captures the last N lines from a session's pane.
func CapturePane(ctx context.Context, cmd Commander, sessionName string, lines int, window string) string {
	linesStr := fmt.Sprintf("-%d", lines)
	if window != "" {
		out, err := cmd.Run(ctx, "", "tmux", "capture-pane",
			"-t", fmt.Sprintf("=%s:%s", sessionName, window),
			"-p", "-e", "-S", linesStr)
		if err == nil {
			return strings.TrimRight(out, " \t\n\r")
		}
	}
	// Get first window index
	winOut, err := cmd.Run(ctx, "", "tmux", "list-windows",
		"-t", fmt.Sprintf("=%s", sessionName),
		"-F", "#{window_index}")
	if err != nil {
		return ""
	}
	firstIdx := "0"
	if lines := strings.Split(strings.TrimSpace(winOut), "\n"); len(lines) > 0 && lines[0] != "" {
		firstIdx = lines[0]
	}
	out, err := cmd.Run(ctx, "", "tmux", "capture-pane",
		"-t", fmt.Sprintf("=%s:%s", sessionName, firstIdx),
		"-p", "-e", "-S", linesStr)
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, " \t\n\r")
}

// SwitchClient switches the tmux client to the given session.
func SwitchClient(ctx context.Context, cmd Commander, sessionName string) error {
	_, err := cmd.Run(ctx, "", "tmux", "switch-client", "-t", fmt.Sprintf("=%s", sessionName))
	return err
}

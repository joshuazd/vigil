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

// Pane preference for representing a session's working directory. Lower wins.
//
// A session has several panes and only one of them is where the work is. This
// used to be decided by sorting the raw lines and keeping the first, which made
// the choice alphabetical by path: a panel pane in ~/portal beat a work pane in
// ~/sc-198799 because "portal" sorts first. Every git read then came from the
// wrong directory, so PR lookups keyed on the wrong branch and gh correctly
// answered "no PR", with no error anywhere to show for it.
const (
	panePreferClaude = iota // marked @vigil_claude: placed there for exactly this
	panePreferActive        // no marker, but the user is looking at it
	panePreferOther
	panePreferPanel // a vigil panel is never the session's work
)

func panePreference(isClaude, isPanel, isActive bool) int {
	switch {
	case isClaude:
		return panePreferClaude
	case isPanel:
		return panePreferPanel
	case isActive:
		return panePreferActive
	default:
		return panePreferOther
	}
}

// ListSessions returns tmux sessions sorted by creation time, deduplicated by
// name, each carrying the path of the pane that best represents its work.
func ListSessions(ctx context.Context, cmd Commander) ([]RawSession, error) {
	out, err := cmd.Run(ctx, "", "tmux", "list-panes", "-a",
		"-F", "#{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)

	index := make(map[string]int)
	prefs := make(map[string]int)
	var sessions []RawSession
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Not a fixed field count: a pane whose path contains a pipe would
		// otherwise swallow the flags that follow it, and the flags are what
		// this function now depends on.
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		name := parts[1]
		// Flags are the last three fields; the path is everything between the
		// name and them, rejoined, so a pipe in a path cannot shift them.
		flagStart := len(parts) - 3
		var path string
		var isActive, isClaude, isPanel bool
		if flagStart > 2 {
			path = strings.Join(parts[2:flagStart], "|")
			isActive = parts[flagStart] == "1"
			isClaude = parts[flagStart+1] == "1"
			isPanel = parts[flagStart+2] == "1"
		} else {
			path = strings.Join(parts[2:], "|")
		}

		pref := panePreference(isClaude, isPanel, isActive)
		if i, seen := index[name]; seen {
			if pref < prefs[name] {
				sessions[i].PanePath = path
				prefs[name] = pref
			}
			continue
		}
		created, _ := strconv.ParseInt(parts[0], 10, 64)
		index[name] = len(sessions)
		prefs[name] = pref
		sessions = append(sessions, RawSession{
			Name:     name,
			PanePath: path,
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

// LastSession returns the tmux "last" session (switch-client -l target).
func LastSession(ctx context.Context, cmd Commander) string {
	out, err := cmd.Run(ctx, "", "tmux", "display-message", "-p", "#{client_last_session}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// MostRecentSession returns the most recently active session other than exclude.
// Used as a fallback when we need a guaranteed-live session to switch to.
func MostRecentSession(ctx context.Context, cmd Commander, exclude string) string {
	out, err := cmd.Run(ctx, "", "tmux", "list-sessions",
		"-F", "#{session_activity}|#{session_name}")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 && parts[1] != exclude {
			return parts[1]
		}
	}
	return ""
}

// AttachedSessions reports which sessions have a client attached.
// session_attached is a client count, not a boolean, so anything other than
// "0" counts as attached - that is also the fail-closed reading for a
// malformed value. Returns an error rather than an empty map when tmux
// cannot be reached, so a caller deciding whether to destroy something can
// fail closed too. Splits on the last "|" because a session name can itself
// contain "|"; session_attached is always digits, so that split is unambiguous.
// Trims per line rather than over the whole output: a session name can start
// with whitespace, which strings.TrimSpace(out) would eat from the first line.
func AttachedSessions(ctx context.Context, cmd Commander) (map[string]bool, error) {
	out, err := cmd.Run(ctx, "", "tmux", "list-sessions",
		"-F", "#{session_name}|#{session_attached}")
	if err != nil {
		return nil, err
	}
	attached := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.LastIndex(line, "|")
		if i < 0 {
			continue
		}
		name, value := line[:i], strings.TrimSpace(line[i+1:])
		attached[name] = value != "0"
	}
	return attached, nil
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

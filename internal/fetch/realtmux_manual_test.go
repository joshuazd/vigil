package fetch

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestListSessionsAgainstRealTmux is a manual check, skipped unless
// VIGIL_REAL_TMUX=1. The bug this guards was invisible to every mocked test in
// this package because the mock returns whatever the test author believed tmux
// returns. It only showed up against a real server with a real panel pane
// sitting in a different directory from the work.
//
// It asserts nothing about specific sessions - it cannot, since it runs against
// whatever the developer has open. It reports what pane each session resolved
// to and fails only if a session resolved to a pane that is marked as a vigil
// panel, which is never correct.
func TestListSessionsAgainstRealTmux(t *testing.T) {
	if os.Getenv("VIGIL_REAL_TMUX") != "1" {
		t.Skip("set VIGIL_REAL_TMUX=1 to run against the developer's real tmux server")
	}

	cmd := &ExecCommander{}
	ctx := context.Background()

	sessions, err := ListSessions(ctx, cmd)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Skip("no tmux sessions to check")
	}

	panelPaths, err := realPanelPaths(ctx, cmd)
	if err != nil {
		t.Fatalf("collecting panel paths: %v", err)
	}

	for _, s := range sessions {
		marked := panelPaths[s.Name][s.PanePath]
		t.Logf("%-44s -> %s", s.Name, s.PanePath)
		if marked {
			t.Errorf("session %q resolved to its vigil panel pane (%s); a panel is never the session's work",
				s.Name, s.PanePath)
		}
	}
}

// realPanelPaths maps session name -> set of paths that belong to a pane marked
// @vigil_panel. A path is only reported when every pane at that path in the
// session is a panel, so a panel sharing a directory with real work does not
// produce a false failure.
func realPanelPaths(ctx context.Context, cmd Commander) (map[string]map[string]bool, error) {
	out, err := cmd.Run(ctx, "", "tmux", "list-panes", "-a",
		"-F", "#{session_name}|#{pane_current_path}|#{@vigil_panel}")
	if err != nil {
		return nil, err
	}
	panels := make(map[string]map[string]bool)
	nonPanels := make(map[string]map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		name, path, isPanel := parts[0], parts[1], parts[2] == "1"
		target := nonPanels
		if isPanel {
			target = panels
		}
		if target[name] == nil {
			target[name] = make(map[string]bool)
		}
		target[name][path] = true
	}
	for name, paths := range panels {
		for path := range paths {
			if nonPanels[name][path] {
				delete(paths, path)
			}
		}
	}
	return panels, nil
}

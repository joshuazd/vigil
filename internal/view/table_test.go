package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func tableFixture() []*session.Session {
	return []*session.Session{
		{
			Name:    "SC-190583 Emit Datadog metrics for investigation",
			Git:     session.GitStatus{Branch: "feature/metrics", Modified: 3, Unpushed: 1},
			PR:      &session.PRStatus{Number: 4521, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"},
			HasBell: true,
		},
		{
			Name: "SC-2 short one",
			Git:  session.GitStatus{Branch: "feature/short"},
		},
	}
}

// TestTableNeverExceedsItsWidth is the panel's hard requirement.
func TestTableNeverExceedsItsWidth(t *testing.T) {
	for _, width := range []int{12, 20, 26, 30, 40, 43, 50, 62, 80, 104, 200} {
		out := RenderTable(tableFixture(), 0, map[string]bool{}, 86400, width, 2, "")
		for i, line := range strings.Split(out, "\n") {
			if got := visibleLen(line); got > width {
				t.Errorf("width %d line %d: %d visible columns\n%q", width, i, got, line)
			}
		}
	}
}

// TestTableKeepsNameColumnPinnedAtFullWidth pins the TUI's appearance: the
// name column stays 52 wide, so the git column starts at the same offset at
// 104 columns as at 200. Stretching the name to fill the pane would fling git
// and PR out to the right edge of a wide terminal.
//
// The cursor is put on row 1 so row 0, the one being measured, carries no
// cursor styling to unpick. Row 0 is the fixture session that has git data.
func TestTableKeepsNameColumnPinnedAtFullWidth(t *testing.T) {
	const wantOffset = 63 // 3 + 1 + 2 + 1 + 2 + 1 + 52 + 1
	for _, width := range []int{104, 200} {
		out := RenderTable(tableFixture(), 1, map[string]bool{}, 86400, width, 2, "")
		row := StripANSI(strings.Split(out, "\n")[0])
		if gitAt := strings.Index(row, "~3"); gitAt != wantOffset {
			t.Errorf("width %d: git column starts at %d, want %d", width, gitAt, wantOffset)
		}
	}
}

func TestTableDropsGitInAPanelWidthRow(t *testing.T) {
	out := RenderTable(tableFixture(), 1, map[string]bool{}, 86400, 40, 2, "")
	if strings.Contains(StripANSI(out), "~3") {
		t.Error("git data rendered at width 40, where the git column is dropped")
	}
	if !strings.Contains(StripANSI(out), "#4521") {
		t.Error("the PR number was dropped at width 40, where it should survive")
	}
}

func TestTableRendersCursorRowWithinWidth(t *testing.T) {
	for _, width := range []int{20, 40, 104} {
		out := RenderTable(tableFixture(), 0, map[string]bool{}, 86400, width, 2, "")
		cursorRow := strings.Split(out, "\n")[0]
		if got := visibleLen(cursorRow); got > width {
			t.Errorf("width %d: cursor row is %d visible columns", width, got)
		}
	}
}

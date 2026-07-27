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

// wideCellFixture is tableFixture's counterpart for exercising truncation
// itself: tableFixture's cells are all short enough to fit even the
// narrowest surviving column untruncated, so a renderRow bug that drops a
// TruncateVisible call is invisible against it. Both of this fixture's
// sessions have a name, a git cell, and a PR cell wide enough to overflow
// the compact/full column widths, so truncation is load-bearing rather than
// a no-op. Two sessions, not one: with cursor hardcoded to 0 in the tests
// that use this fixture, row 0 exercises renderRow's isCursor branch and
// row 1 exercises its non-cursor branch, so a truncation bug confined to
// either branch has wide content to fail against.
func wideCellFixture() []*session.Session {
	staleAge := 172800 // 2 days: renders a stale marker and exceeds colGit untruncated
	staleAge2 := 90000 // just over the 86400 staleness threshold, 1 day display
	return []*session.Session{
		{
			Name: "SC-999999 an extremely long session name that overflows every name tier",
			Git: session.GitStatus{
				Branch:        "feature/wide",
				Modified:      30,
				Added:         120,
				Deleted:       70,
				Unpushed:      50,
				RebaseAgeSecs: &staleAge,
			},
			PR: &session.PRStatus{
				Number: 4521, State: "OPEN", Checks: "fail",
				ReviewDecision: "CHANGES_REQUESTED", UnresolvedComments: 3, HasConflicts: true,
			},
		},
		{
			Name: "SC-1010 second overflowing session for the non-cursor branch",
			Git: session.GitStatus{
				Branch:        "feature/also-wide",
				Modified:      45,
				Added:         200,
				Deleted:       15,
				Unpushed:      9,
				RebaseAgeSecs: &staleAge2,
			},
			PR: &session.PRStatus{
				Number: 7777, State: "OPEN", Checks: "pending",
				UnresolvedComments: 12, HasConflicts: true,
			},
		},
	}
}

// tableTestFixtures pairs a name with the sessions it renders, for tests
// that must exercise both a fixture whose cells fit untruncated and one
// whose cells do not. A func, not a package var, so each test gets its own
// unshared session slices.
func tableTestFixtures() []struct {
	name     string
	sessions []*session.Session
} {
	return []struct {
		name     string
		sessions []*session.Session
	}{
		{"tableFixture", tableFixture()},
		{"wideCellFixture", wideCellFixture()},
	}
}

// TestTableNeverExceedsItsWidth is the panel's hard requirement. A per-line
// width check alone cannot tell "the row fit" from "the row wrapped into two
// lines that each fit" - lipgloss Width() word-wraps oversized content rather
// than leaving it overflowing, so a wrapped row passes a per-line check by
// construction. RenderTable pads to height lines whenever there is at least
// one session, so asserting the line count catches wrapping that the
// per-line check alone would miss.
func TestTableNeverExceedsItsWidth(t *testing.T) {
	const height = 2
	for _, fx := range tableTestFixtures() {
		for _, width := range []int{12, 20, 26, 30, 40, 43, 50, 62, 80, 104, 200} {
			out := RenderTable(fx.sessions, 0, map[string]bool{}, 86400, width, height, "")
			lines := strings.Split(out, "\n")
			if len(lines) != height {
				t.Errorf("%s width %d: got %d lines, want %d (a wrapped row inflates this)", fx.name, width, len(lines), height)
			}
			for i, line := range lines {
				if got := visibleLen(line); got > width {
					t.Errorf("%s width %d line %d: %d visible columns\n%q", fx.name, width, i, got, line)
				}
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

// TestTableRendersCursorRowWithinWidth's stated subject is the cursor row,
// which is rendered through lipgloss's Width(), a wrap - not a truncate. A
// bug confined to the isCursor branch of renderRow (e.g. a dropped
// TruncateVisible call) would wrap the cursor row into extra sub-lines that
// each individually pass a per-line width check, so the line-count assertion
// is required to catch it: N sessions in, N lines out.
func TestTableRendersCursorRowWithinWidth(t *testing.T) {
	const height = 2
	for _, fx := range tableTestFixtures() {
		for _, width := range []int{20, 40, 104} {
			out := RenderTable(fx.sessions, 0, map[string]bool{}, 86400, width, height, "")
			lines := strings.Split(out, "\n")
			if len(lines) != height {
				t.Errorf("%s width %d: got %d lines, want %d (the cursor row wrapped)", fx.name, width, len(lines), height)
			}
			cursorRow := lines[0]
			if got := visibleLen(cursorRow); got > width {
				t.Errorf("%s width %d: cursor row is %d visible columns", fx.name, width, got)
			}
		}
	}
}

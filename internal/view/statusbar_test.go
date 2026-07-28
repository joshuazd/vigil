package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func TestStatusBarShowsHealth(t *testing.T) {
	out := RenderStatusBar(nil, nil, session.SortCreated, 80, "daemon stale 9s")
	if !strings.Contains(out, "daemon stale 9s") {
		t.Errorf("health missing from %q", out)
	}
}

// TestStatusBarOmitsEmptyHealth covers both shapes an unguarded empty health
// segment would take: a doubled separator when another segment follows it, and
// a dangling one when nothing does. A session fixture is required for the first
// - with no sessions there is no following segment, and the doubled separator
// can never appear whether the guard exists or not.
func TestStatusBarOmitsEmptyHealth(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
	}
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 80, ""))
	if strings.Contains(out, "·  ·") {
		t.Errorf("empty health left a doubled separator in %q", out)
	}

	bare := strings.TrimRight(StripANSI(RenderStatusBar(nil, nil, session.SortCreated, 80, "")), " ")
	if strings.HasSuffix(bare, "·") {
		t.Errorf("empty health left a dangling separator in %q", bare)
	}
}

// TestStatusBarNeverExceedsItsWidth is the panel's requirement: lipgloss
// Width pads but does not truncate, so a status bar wider than the pane wraps
// and pushes every table row down by one.
func TestStatusBarNeverExceedsItsWidth(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
		{Name: "SC-2 two", Git: session.GitStatus{Branch: "b"}},
		{Name: "SC-3 three", Git: session.GitStatus{Branch: "c"}},
	}
	for _, width := range []int{20, 30, 40, 60, 80, 120} {
		out := RenderStatusBar(sessions, nil, session.SortAlpha, width, "no daemon")
		if strings.Contains(out, "\n") {
			t.Fatalf("width %d wrapped: %q", width, out)
		}
		if got := visibleLen(out); got != width {
			t.Errorf("width %d: rendered %d visible columns", width, got)
		}
	}
}

// TestStatusBarKeepsHealthOverStateCounts pins the priority at the real
// landscape panel width: 40 columns leaves room for the identity, the session
// count and the health segment, but not the state counts after them. Health is
// the segment worth that last slot.
//
// The arithmetic, so this test is maintainable: StatusBarStyle has
// Padding(0, 1), so the content budget is 38. "vigil" is 5, " · 1 sessions" is
// 13, " · no daemon" is 12, totalling 30. The next segment, " · 1 idle", costs
// 9 and would reach 39, so it is dropped.
func TestStatusBarKeepsHealthOverStateCounts(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
	}
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 40, "no daemon"))
	if !strings.Contains(out, "no daemon") {
		t.Errorf("health was dropped: %q", out)
	}
	if strings.Contains(out, "idle") {
		t.Errorf("a state count was kept ahead of health: %q", out)
	}
}

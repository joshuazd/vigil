package view

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

func TestStatusBarShowsHealth(t *testing.T) {
	out := RenderStatusBar(nil, nil, session.SortCreated, 80, "daemon stale 9s", 0)
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
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 80, "", 0))
	if strings.Contains(out, "·  ·") {
		t.Errorf("empty health left a doubled separator in %q", out)
	}

	bare := strings.TrimRight(StripANSI(RenderStatusBar(nil, nil, session.SortCreated, 80, "", 0)), " ")
	if strings.HasSuffix(bare, "·") {
		t.Errorf("empty health left a dangling separator in %q", bare)
	}
}

// TestStatusBarNeverExceedsItsWidth is the panel's requirement: lipgloss
// Width pads but does not truncate, so a status bar wider than the pane wraps
// and pushes every table row down by one.
//
// The queueCount=4 sweep is what would have caught the badge's "⚡": it is
// Emoji_Presentation and two terminal cells wide, but the budgeting used to
// measure it with visibleLen, which counts runes. That undercounted every
// segment following it by one column and the bar wrapped - confirmed to wrap
// at widths 12, 25 and 34 before the fix. lipgloss.Width is used for the
// assertion because it is the same display-cell measure the fix uses to
// budget, not visibleLen.
func TestStatusBarNeverExceedsItsWidth(t *testing.T) {
	sessions := []*session.Session{
		{Name: "SC-1 one", Git: session.GitStatus{Branch: "a"}},
		{Name: "SC-2 two", Git: session.GitStatus{Branch: "b"}},
		{Name: "SC-3 three", Git: session.GitStatus{Branch: "c"}},
	}
	for _, queueCount := range []int{0, 4} {
		for width := 8; width <= 120; width++ {
			out := RenderStatusBar(sessions, nil, session.SortAlpha, width, "no daemon", queueCount)
			if strings.Contains(out, "\n") {
				t.Fatalf("queueCount %d width %d wrapped: %q", queueCount, width, out)
			}
			if got := lipgloss.Width(out); got != width {
				t.Errorf("queueCount %d width %d: rendered %d visible columns", queueCount, width, got)
			}
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
	out := StripANSI(RenderStatusBar(sessions, nil, session.SortCreated, 40, "no daemon", 0))
	if !strings.Contains(out, "no daemon") {
		t.Errorf("health was dropped: %q", out)
	}
	if strings.Contains(out, "idle") {
		t.Errorf("a state count was kept ahead of health: %q", out)
	}
}

func TestStatusBarShowsTheQueueBadge(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 120, "", 4)
	if !strings.Contains(got, "4") {
		t.Errorf("status bar missing the queue count:\n%s", got)
	}
}

func TestStatusBarOmitsTheBadgeWhenTheQueueIsEmpty(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 120, "", 0)
	if strings.Contains(got, "⚡") {
		t.Errorf("status bar shows a badge with an empty queue:\n%s", got)
	}
}

// TestStatusBarDropsTheBadgeWhenItDoesNotFit relies on addSegment's existing
// budget behaviour rather than a new guard.
func TestStatusBarDropsTheBadgeWhenItDoesNotFit(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 8, "", 4)
	if strings.Contains(got, "⚡") {
		t.Errorf("status bar kept the badge at width 8:\n%s", got)
	}
}

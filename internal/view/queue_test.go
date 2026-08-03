package view

import (
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

func queueFixture() []session.QueueItem {
	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	return []session.QueueItem{
		{Kind: session.QueueStory, ID: "223480", Title: "Backfill remediation audit rows",
			UpdatedAt: base.Add(-2 * time.Hour).Unix()},
		{Kind: session.QueueReview, ID: "34967", Repo: "portal", Title: "Partner facing incident report Timeline tab",
			UpdatedAt: base.Add(-26 * time.Hour).Unix()},
	}
}

func TestRenderQueueIsEmptyWithNoItems(t *testing.T) {
	if got := RenderQueue(nil, 0, -1, 120, 0, time.Now()); got != "" {
		t.Errorf("RenderQueue(nil) = %q, want empty", got)
	}
}

func TestRenderQueueShowsLabelsAndTitles(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	got := RenderQueue(queueFixture(), 0, -1, 120, 0, now)

	for _, want := range []string{"QUEUE", "sc-223480", "portal#34967", "Backfill remediation audit rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
}

func TestRenderQueueReportsHiddenCount(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	got := RenderQueue(queueFixture(), 3, -1, 120, 0, now)
	if !strings.Contains(got, "3 in progress") {
		t.Errorf("render missing the hidden count:\n%s", got)
	}

	none := RenderQueue(queueFixture(), 0, -1, 120, 0, now)
	if strings.Contains(none, "in progress") {
		t.Errorf("render claims a hidden count with hidden=0:\n%s", none)
	}
}

func TestRenderQueueShowsAge(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	got := RenderQueue(queueFixture(), 0, -1, 120, 0, now)

	if !strings.Contains(got, "2h") {
		t.Errorf("render missing the 2h age:\n%s", got)
	}
	if !strings.Contains(got, "1d") {
		t.Errorf("render missing the 1d age:\n%s", got)
	}
}

// TestRenderQueueMarksTheCursor pins that the cursor is visible at all. Without
// it the section renders identically whether or not a row is selected, and the
// user cannot tell what enter would dispatch.
func TestRenderQueueMarksTheCursor(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	first := RenderQueue(queueFixture(), 0, 0, 120, 0, now)
	second := RenderQueue(queueFixture(), 0, 1, 120, 0, now)
	none := RenderQueue(queueFixture(), 0, -1, 120, 0, now)

	if first == second {
		t.Error("cursor 0 and cursor 1 render identically")
	}
	if first == none {
		t.Error("cursor 0 and no cursor render identically")
	}
}

// TestRenderQueueTruncatesToMaxRows pins the overflow guard: maxRows caps how
// many item rows are drawn, and anything past it collapses into one summary
// row rather than growing the section without bound.
func TestRenderQueueTruncatesToMaxRows(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	items := queueFixture()

	got := RenderQueue(items, 0, -1, 120, 1, now)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d lines with maxRows=1, want 2 (header + 1 summary)\n%s", len(lines), got)
	}
	if !strings.Contains(got, "+2 more") {
		t.Errorf("render missing the overflow summary:\n%s", got)
	}
	if strings.Contains(got, "sc-223480") || strings.Contains(got, "portal#34967") {
		t.Errorf("render shows a truncated item's label:\n%s", got)
	}

	unbounded := RenderQueue(items, 0, -1, 120, 0, now)
	if strings.Contains(unbounded, "more") {
		t.Errorf("maxRows=0 (no cap) still truncated:\n%s", unbounded)
	}
}

// TestQueueAgeNeverGoesNegative pins Minor 4: a future UpdatedAt (clock skew
// between this machine and GitHub or Shortcut) must not render as "-3m".
func TestQueueAgeNeverGoesNegative(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	future := now.Add(3 * time.Minute).Unix()

	if got := queueAge(future, now); got != "0m" {
		t.Errorf("queueAge(future) = %q, want 0m", got)
	}
}

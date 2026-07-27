package view

import (
	"strings"
	"testing"
)

// TestLayoutMatchesTodayAtFullWidth is the regression pin for the TUI: at the
// width the dashboard has always run at, nothing about the geometry moves.
func TestLayoutMatchesTodayAtFullWidth(t *testing.T) {
	for _, width := range []int{104, 140, 200} {
		l := LayoutForWidth(width)
		if !l.Indicator || !l.Index {
			t.Errorf("width %d dropped a column", width)
		}
		if l.Name != 52 {
			t.Errorf("width %d: name column is %d, want 52 (unchanged from today)", width, l.Name)
		}
		if l.Git != 18 || l.PR != 22 {
			t.Errorf("width %d: git/pr are %d/%d, want 18/22", width, l.Git, l.PR)
		}
	}
}

func TestLayoutShrinksNameBeforeDroppingColumns(t *testing.T) {
	l := LayoutForWidth(80)
	if l.Git == 0 || l.PR == 0 {
		t.Fatal("width 80 dropped a column instead of shrinking the name")
	}
	if l.Name != 28 {
		t.Errorf("got name %d, want 28 (80 - 52)", l.Name)
	}
}

func TestLayoutDropsGitBeforePR(t *testing.T) {
	l := LayoutForWidth(50)
	if l.Git != 0 {
		t.Error("git survived at width 50")
	}
	if l.PR == 0 {
		t.Error("PR was dropped before git")
	}
}

// TestLayoutDropsIndexAndShrinksPRWhenNarrow uses 40, the default width of
// the landscape panel, so this is the layout the feature is actually judged on.
func TestLayoutDropsIndexAndShrinksPRWhenNarrow(t *testing.T) {
	l := LayoutForWidth(40)
	if l.Index {
		t.Error("the index column survived at width 40")
	}
	if l.PR != 12 {
		t.Errorf("got PR %d, want the compact 12", l.PR)
	}
	if l.Name != 20 {
		t.Errorf("got name %d, want 20 (40 - 20)", l.Name)
	}
}

func TestLayoutDropsPRWhenVeryNarrow(t *testing.T) {
	l := LayoutForWidth(20)
	if l.PR != 0 {
		t.Error("PR survived at width 20")
	}
	if !l.Indicator || !l.State {
		t.Error("the indicator or state dot was dropped before PR")
	}
	if l.Name != 13 {
		t.Errorf("got name %d, want 13 (20 - 7)", l.Name)
	}
}

func TestLayoutKeepsANameAtAnyWidth(t *testing.T) {
	for _, width := range []int{1, 4, 8, 11, 12, 26, 43, 62} {
		l := LayoutForWidth(width)
		if l.Name < 1 {
			t.Errorf("width %d: name column collapsed to %d", width, l.Name)
		}
	}
}

// TestLayoutDropsTheStateDotOnlyAsALastResort: under four columns there is
// nothing to spend on a dot and a separator.
func TestLayoutDropsTheStateDotOnlyAsALastResort(t *testing.T) {
	if l := LayoutForWidth(4); !l.State {
		t.Error("the state dot was dropped at width 4, where it still fits")
	}
	if l := LayoutForWidth(3); l.State {
		t.Error("the state dot survived at width 3, where it cannot fit")
	}
}

// TestLayoutNeverExceedsItsWidth is the invariant the panel depends on. A
// total wider than the pane wraps, and one wrapped row shifts every row under
// it for as long as the panel is open.
func TestLayoutNeverExceedsItsWidth(t *testing.T) {
	for width := 1; width <= 220; width++ {
		if got := LayoutForWidth(width).Total(); got > width {
			t.Fatalf("width %d: layout totals %d", width, got)
		}
	}
}

// --- truncation ---

func TestTruncateVisibleKeepsShortStrings(t *testing.T) {
	if got := TruncateVisible("abc", 10); got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}

func TestTruncateVisibleCountsVisibleColumnsOnly(t *testing.T) {
	styled := "\x1b[32m#1234\x1b[0m ✓"
	got := TruncateVisible(styled, 5)
	if visibleLen(got) != 5 {
		t.Errorf("got %d visible columns from %q, want 5", visibleLen(got), got)
	}
}

func TestTruncateVisibleResetsStyleWhenItCuts(t *testing.T) {
	got := TruncateVisible("\x1b[32m#1234 ✓", 3)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("%q ends mid-style: the color bleeds into the rest of the row", got)
	}
}

func TestTruncateVisibleZeroWidth(t *testing.T) {
	if got := TruncateVisible("\x1b[32mabc", 0); visibleLen(got) != 0 {
		t.Errorf("got %q, want no visible output", got)
	}
}

func TestTruncateNameAddsAnEllipsis(t *testing.T) {
	got := truncateName("SC-190583 a very long story title indeed", 12)
	if visibleLen(got) != 12 {
		t.Errorf("got %d columns from %q, want 12", visibleLen(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("%q does not show it was cut", got)
	}
}

func TestTruncateNameLeavesShortNames(t *testing.T) {
	if got := truncateName("SC-1 short", 40); got != "SC-1 short" {
		t.Errorf("got %q, want it untouched", got)
	}
}

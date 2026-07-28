package view

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jzinkduda/vigil/internal/session"
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
	if l.Name != 30 {
		t.Errorf("got name %d, want 30 (80 - 50)", l.Name)
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
	if l.Name != 21 {
		t.Errorf("got name %d, want 21 (40 - 19)", l.Name)
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
	if l.Name != 14 {
		t.Errorf("got name %d, want 14 (20 - 6)", l.Name)
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

// TestTruncateVisibleNeverSplitsARune covers the failure a per-byte cut makes
// invisible to every width assertion: when the budget runs out partway through
// a multi-byte character, writing its lead byte and dropping the continuation
// bytes emits invalid UTF-8 that the terminal draws as a replacement glyph,
// while visibleLen still counts the orphan as exactly one column. Every
// boundary offset through each string is checked rather than one, because the
// bug only fires at the offsets where the budget lands mid-rune.
func TestTruncateVisibleNeverSplitsARune(t *testing.T) {
	inputs := []string{
		"~45 +200 -15 ↑ 9 ⚠↻ 1d",
		"日本語テスト",
		"\x1b[32m#4521 ✓\x1b[0m ✗ ⚠↻",
		"↑↓⚠↻…",
	}
	for _, s := range inputs {
		for width := 0; width <= VisibleWidth(s)+2; width++ {
			got := TruncateVisible(s, width)
			if !utf8.ValidString(got) {
				t.Errorf("TruncateVisible(%q, %d) = %q: invalid UTF-8", s, width, got)
			}
			if want, have := min(width, VisibleWidth(s)), VisibleWidth(got); have != want {
				t.Errorf("TruncateVisible(%q, %d) = %q: %d visible columns, want %d", s, width, got, have, want)
			}
		}
	}
}

// TestTruncateVisibleCutsBeforeAWideCharacterItCannotFit pins the two exact
// reproductions from the real render path: a git cell at colGit and a
// CJK name, both cut precisely at a multi-byte character.
func TestTruncateVisibleCutsBeforeAWideCharacterItCannotFit(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"~45 +200 -15 ↑ 9 ⚠↻ 1d", 18, "~45 +200 -15 ↑ 9 ⚠\x1b[0m"},
		{"日本語テスト", 3, "日本語\x1b[0m"},
	}
	for _, tc := range cases {
		if got := TruncateVisible(tc.in, tc.width); got != tc.want {
			t.Errorf("TruncateVisible(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}

// TestRenderedRowsAreValidUTF8 is the same invariant one level up, against the
// widths the dashboard and the panel actually run at. 104 is the default
// dashboard width, where the wide fixture's git cell overflows colGit and is
// cut mid-character.
func TestRenderedRowsAreValidUTF8(t *testing.T) {
	fixtures := map[string][]*session.Session{
		"tableFixture":    tableFixture(),
		"wideCellFixture": wideCellFixture(),
	}
	for name, sessions := range fixtures {
		for width := 1; width <= 220; width++ {
			out := RenderTable(sessions, 0, map[string]bool{}, 86400, width, 2, "")
			if !utf8.ValidString(out) {
				t.Fatalf("%s width %d: rendered invalid UTF-8\n%q", name, width, out)
			}
		}
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

// TestTotalMatchesWhatTheRowActuallyRenders is the constraint that was missing.
// The constants drifted from the renderers because nothing compared them, and
// Total() <= width passes happily while rows come out narrower than budgeted.
func TestTotalMatchesWhatTheRowActuallyRenders(t *testing.T) {
	staleAge := 172800 // 2 days, renders wider than the column
	s := &session.Session{
		Name: "SC-999999 an extremely long session name that overflows every name tier",
		Git: session.GitStatus{
			Branch:        "feature/wide",
			Modified:      300,
			Added:         1200,
			Deleted:       700,
			Unpushed:      500,
			RebaseAgeSecs: &staleAge,
		},
		PR: &session.PRStatus{
			Number: 99999, State: "OPEN", Checks: "fail",
			ReviewDecision: "CHANGES_REQUESTED", UnresolvedComments: 999, HasConflicts: true,
		},
	}
	for _, width := range []int{200, 104, 80, 60, 41, 40, 28, 20, 15, 8, 4, 1} {
		layout := LayoutForWidth(width)
		row := renderRow(s, 3, false, 86400, width, false, layout)
		if got := VisibleWidth(row); got != layout.Total() {
			t.Errorf("width %d: row renders %d columns, Total() claims %d", width, got, layout.Total())
		}
	}
}

// TestFrozenThresholdsAdmitAUsefulName stops a future edit to a fixed cost from
// silently producing a name below nameMin at a tier boundary. The thresholds are
// tuned, not derived, so nothing else checks them against the costs.
func TestFrozenThresholdsAdmitAUsefulName(t *testing.T) {
	for _, tier := range []struct {
		name           string
		threshold, fix int
	}{
		{"full", tierFull, fullFixed},
		{"noGit", tierNoGit, noGitFixed},
		{"compact", tierCompact, compactFixed},
		{"noPR", tierNoPR, noPRFixed},
	} {
		if got := tier.threshold - tier.fix; got < nameMin {
			t.Errorf("%s: threshold %d leaves %d columns of name, want at least %d",
				tier.name, tier.threshold, got, nameMin)
		}
	}
}

// TestPanelWidthStillPicksTheCompactTier pins the layout the phase 2 resize
// verification was run at. Deriving the thresholds from the corrected costs
// moves 40 onto the noGit tier, where it gets a 9-column name beside a full PR
// column instead of 20 columns of name and a compact one.
func TestPanelWidthStillPicksTheCompactTier(t *testing.T) {
	l := LayoutForWidth(40)
	if l.Index {
		t.Error("width 40 kept the index column, so it is not on the compact tier")
	}
	if l.PR != colPRCompact {
		t.Errorf("got PR %d, want the compact %d", l.PR, colPRCompact)
	}
	if l.Name < 20 {
		t.Errorf("got name %d, want at least the 20 columns it has today", l.Name)
	}
}

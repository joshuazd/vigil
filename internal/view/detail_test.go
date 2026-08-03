package view

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// TestPRCommentsShowsLoadingBeforeTheFetchAnswers separates "not fetched"
// from "none", which polling used to make indistinguishable because it
// always carried an answer.
func TestPRCommentsShowsLoadingBeforeTheFetchAnswers(t *testing.T) {
	s := &session.Session{PR: &session.PRStatus{Number: 1, UnresolvedComments: 2}}

	var b strings.Builder
	renderPRComments(&b, s, nil, 10)
	if !strings.Contains(b.String(), "Loading") {
		t.Errorf("got %q, want a loading state for an unfetched thread list", b.String())
	}

	b.Reset()
	renderPRComments(&b, s, []session.ReviewComment{}, 10)
	if strings.Contains(b.String(), "Loading") {
		t.Errorf("got %q, want a settled answer once the fetch returned empty", b.String())
	}
}

// TestTruncateVisibleRespectsItsWidth pins the contract the name promises:
// the result never exceeds maxW cells. It used to return maxW characters and
// then append "…", so a truncated string was maxW+1 and every caller padding
// to maxW overflowed its column by one. Nothing caught that, which is why
// this test exists rather than only the caller-level width sweeps.
func TestTruncateVisibleRespectsItsWidth(t *testing.T) {
	inputs := []string{
		"short",
		"exactlyten",
		"a considerably longer string than the width allows",
		"soc-workflows#soc-workflows",
		"unicode … in the middle of it all, several times over",
	}
	for _, in := range inputs {
		for maxW := 2; maxW <= 30; maxW++ {
			got := truncateVisible(in, maxW)
			if w := lipgloss.Width(got); w > maxW {
				t.Errorf("truncateVisible(%q, %d) = %q, %d cells wide", in, maxW, got, w)
			}
		}
	}
}

// TestTruncateVisibleLeavesShortStringsAlone is the other half: it must not
// mangle anything that already fits, or every column would gain a stray "…".
func TestTruncateVisibleLeavesShortStringsAlone(t *testing.T) {
	if got := truncateVisible("abc", 10); got != "abc" {
		t.Errorf("truncateVisible(\"abc\", 10) = %q, want abc", got)
	}
	if got := truncateVisible("abcde", 5); got != "abcde" {
		t.Errorf("truncateVisible(\"abcde\", 5) = %q, want abcde unchanged at exactly maxW", got)
	}
}

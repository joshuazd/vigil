package view

import (
	"strings"
	"testing"

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

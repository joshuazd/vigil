package model

import (
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/view"
)

func commentSession() *session.Session {
	return &session.Session{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "feature/x", GitRoot: "/repo"},
		PR: &session.PRStatus{
			Number: 42, State: "OPEN",
			UnresolvedComments: 2,
		},
	}
}

// TestCommentsModeFetchesBodiesOnDemand is the consequence of the polling trim:
// nothing carries the bodies any more, so opening the mode has to go and get
// them.
func TestCommentsModeFetchesBodiesOnDemand(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode

	cmd := m.refreshDetailCmd()
	if cmd == nil {
		t.Fatal("comments mode produced no fetch command")
	}
	if _, ok := cmd().(PRCommentsMsg); !ok {
		t.Fatalf("got %T, want PRCommentsMsg", cmd())
	}
}

func TestPRCommentsMsgIsStoredByBranch(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}

	next, _ := m.Update(PRCommentsMsg{
		Branch:   "feature/x",
		Comments: []session.ReviewComment{{Author: "reviewer", Body: "needs a test"}},
	})

	got := next.(Model).reviewComments["feature/x"]
	if len(got) != 1 || got[0].Author != "reviewer" {
		t.Fatalf("got %+v, want the reviewer's comment stored under its branch", got)
	}
}

// TestCommentsModeDoesNotRefetchWhatItHas keeps the mode from spending a gh
// call on every render tick while the panel is open.
func TestCommentsModeDoesNotRefetchWhatItHas(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{commentSession()}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode
	m.reviewComments = map[string][]session.ReviewComment{
		"feature/x": {{Author: "reviewer", Body: "needs a test"}},
	}

	if cmd := m.refreshDetailCmd(); cmd != nil {
		t.Error("refetched comments that are already loaded")
	}
}

// TestPRCommentsMsgNormalisesNilToEmpty pins the other half of the store
// contract: a fetch that legitimately found nothing must still land as a
// non-nil entry, or the view (which treats nil as "not fetched yet") would
// show a permanent loading state for a branch that was already answered.
func TestPRCommentsMsgNormalisesNilToEmpty(t *testing.T) {
	m := newTestModel()

	next, _ := m.Update(PRCommentsMsg{Branch: "feature/x", Comments: nil})
	m2 := next.(Model)

	got, ok := m2.reviewComments["feature/x"]
	if !ok {
		t.Fatal("branch was not stored at all")
	}
	if got == nil {
		t.Error("got a nil slice stored for an answered fetch, want a non-nil empty slice")
	}
}

// TestCommentsModeFetchesOnceThenSuppressesRefetch drives the real
// refreshDetailCmd and the real Update rather than constructing a
// PRCommentsMsg by hand: it runs the command refreshDetailCmd actually
// returns, feeds the message that command actually produces into Update, and
// only then checks that a second refreshDetailCmd call declines to refetch.
// The PR here has a git root the mock commander has no remote registered for,
// so FetchReviewComments genuinely answers with nil - the real "found
// nothing" path, not a hand-built empty slice.
func TestCommentsModeFetchesOnceThenSuppressesRefetch(t *testing.T) {
	m := newTestModel()
	s := commentSession()
	s.Git.GitRoot = "/repo-task10-fetches-once"
	m.sessions = []*session.Session{s}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode

	cmd := m.refreshDetailCmd()
	if cmd == nil {
		t.Fatal("want a fetch command for a branch with nothing cached yet")
	}
	msg := cmd()
	prMsg, ok := msg.(PRCommentsMsg)
	if !ok {
		t.Fatalf("got %T, want PRCommentsMsg", msg)
	}

	next, _ := m.Update(prMsg)
	m2 := next.(Model)

	if got := m2.reviewComments[s.Git.Branch]; got == nil {
		t.Error("Update did not normalise the fetch's nil result to a non-nil empty slice")
	}
	if cmd := m2.refreshDetailCmd(); cmd != nil {
		t.Error("refetched comments after a completed fetch that answered with none")
	}
}

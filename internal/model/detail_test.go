package model

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

// TestRefreshDetailCmdNilPRProducesNoCommand pins the PR nil-check in the
// DetailPRComments case of refreshDetailCmd: the branch is set, so only the
// PR guard stands between this and an immediate s.PR.Number dereference
// (refreshDetailCmd evaluates that before ever returning a tea.Cmd, so a
// missing guard here panics on the call itself, not merely once the
// returned command runs).
func TestRefreshDetailCmdNilPRProducesNoCommand(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "feature/x", GitRoot: "/repo"},
		PR:   nil,
	}}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode

	if cmd := m.refreshDetailCmd(); cmd != nil {
		t.Error("want no fetch command for a session with no PR")
	}
}

// TestRefreshDetailCmdEmptyBranchProducesNoCommand is the sibling of the nil
// PR case: PR is set, but the branch is empty. Only the Git.Branch == ""
// check catches this - if it were dropped, refreshDetailCmd would return a
// live command keyed on an empty branch string.
func TestRefreshDetailCmdEmptyBranchProducesNoCommand(t *testing.T) {
	m := newTestModel()
	m.sessions = []*session.Session{{
		Name: "alpha",
		Git:  session.GitStatus{Branch: "", GitRoot: "/repo"},
		PR:   &session.PRStatus{Number: 42, State: "OPEN"},
	}}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode

	if cmd := m.refreshDetailCmd(); cmd != nil {
		t.Error("want no fetch command for a session with an empty branch")
	}
}

// TestRefreshKeyClearsReviewCommentsCache is the escape hatch for a comment
// cache that otherwise never invalidates: pressing r must drop any cached
// entries so the next comments-mode render refetches. Driven through Update
// with a real key message, not by calling handleKey's Refresh branch
// directly, so production is what supplies the clear. This is the
// self-polling configuration (daemonConn is nil); see
// TestRefreshKeyClearsReviewCommentsCacheWhenDaemonFed for the daemon-fed
// one, which is the configuration where this clear actually matters.
func TestRefreshKeyClearsReviewCommentsCache(t *testing.T) {
	m := newTestModel()
	s := commentSession()
	m.sessions = []*session.Session{s}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode
	m.reviewComments = map[string][]session.ReviewComment{
		s.Git.Branch: {{Author: "reviewer", Body: "stale"}},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 := next.(Model)

	if _, ok := m2.reviewComments[s.Git.Branch]; ok {
		t.Fatal("refresh did not clear the cached comments")
	}
	if cmd := m2.refreshDetailCmd(); cmd == nil {
		t.Error("want refreshDetailCmd to refetch once the cache entry is cleared")
	}
}

// TestRefreshKeyClearsReviewCommentsCacheWhenDaemonFed pins the clear in the
// one configuration where it is not merely nice to have but the only lever
// available: a daemon-fed client (daemonConn set) cannot force a poll of its
// own, since startPoll refuses outright while a daemon is connected, so the
// unconditional clear ahead of that refusal is its sole escape hatch from a
// stale comment cache. Gating the clear on m.daemonConn == nil would leave
// TestRefreshKeyClearsReviewCommentsCache (daemonConn nil) as the only
// coverage and this exact regression would pass the whole suite.
func TestRefreshKeyClearsReviewCommentsCacheWhenDaemonFed(t *testing.T) {
	m := newTestModel()
	s := commentSession()
	m.sessions = []*session.Session{s}
	m.detailOpen = true
	mode := view.DetailPRComments
	m.detailMode = &mode
	m.daemonConn = &fakeConn{}
	m.reviewComments = map[string][]session.ReviewComment{
		s.Git.Branch: {{Author: "reviewer", Body: "stale"}},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 := next.(Model)

	if _, ok := m2.reviewComments[s.Git.Branch]; ok {
		t.Fatal("refresh did not clear the cached comments for a daemon-fed client")
	}
	if cmd := m2.refreshDetailCmd(); cmd == nil {
		t.Error("want refreshDetailCmd to refetch once the cache entry is cleared")
	}
}

package session

import (
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

// --- SessionState tests ---

func TestBellIsAttention(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", HasBell: true}
	if s.State() != Attention {
		t.Errorf("got %v, want Attention", s.State())
	}
}

func TestNoPRIsIdle(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp"}
	if s.State() != Idle {
		t.Errorf("got %v, want Idle", s.State())
	}
}

func TestMergedIsDone(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "MERGED"}}
	if s.State() != Done {
		t.Errorf("got %v, want Done", s.State())
	}
}

func TestCIFailIsBlocked(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", Checks: "fail"}}
	if s.State() != Blocked {
		t.Errorf("got %v, want Blocked", s.State())
	}
}

func TestChangesRequestedIsBlocked(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}}
	if s.State() != Blocked {
		t.Errorf("got %v, want Blocked", s.State())
	}
}

func TestConflictsIsBlocked(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", HasConflicts: true}}
	if s.State() != Blocked {
		t.Errorf("got %v, want Blocked", s.State())
	}
}

func TestUnresolvedComments(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", UnresolvedComments: 2}}
	if s.State() != Unresolved {
		t.Errorf("got %v, want Unresolved", s.State())
	}
}

func TestApprovedAndPassingIsMergeable(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", Checks: "pass"}}
	if s.State() != Mergeable {
		t.Errorf("got %v, want Mergeable", s.State())
	}
}

func TestApprovedPendingCI(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", Checks: "pending"}}
	if s.State() != Approved {
		t.Errorf("got %v, want Approved", s.State())
	}
}

func TestCIPendingIsPending(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", Checks: "pending"}}
	if s.State() != Pending {
		t.Errorf("got %v, want Pending", s.State())
	}
}

func TestOpenNonDraftIsReview(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", Checks: "pass"}}
	if s.State() != Review {
		t.Errorf("got %v, want Review", s.State())
	}
}

func TestDraftIsIdle(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", PR: &PRStatus{Number: 1, State: "OPEN", IsDraft: true}}
	if s.State() != Idle {
		t.Errorf("got %v, want Idle", s.State())
	}
}

func TestBellPriorityOverPR(t *testing.T) {
	s := Session{Name: "test", PanePath: "/tmp", HasBell: true, PR: &PRStatus{Number: 1, State: "OPEN", Checks: "fail"}}
	if s.State() != Attention {
		t.Errorf("got %v, want Attention", s.State())
	}
}

// --- Indicator tests ---

func TestIndicatorCurrent(t *testing.T) {
	s := Session{Name: "a", PanePath: "/", IsCurrent: true}
	if s.Indicator() != "▸" {
		t.Errorf("got %q, want ▸", s.Indicator())
	}
}

func TestIndicatorLast(t *testing.T) {
	s := Session{Name: "a", PanePath: "/", IsLast: true}
	if s.Indicator() != "·" {
		t.Errorf("got %q, want ·", s.Indicator())
	}
}

func TestIndicatorNeither(t *testing.T) {
	s := Session{Name: "a", PanePath: "/"}
	if s.Indicator() != " " {
		t.Errorf("got %q, want space", s.Indicator())
	}
}

// --- GitStatus display tests ---

func TestGitCleanDisplay(t *testing.T) {
	g := GitStatus{}
	if g.Display() != "—" {
		t.Errorf("got %q, want —", g.Display())
	}
}

func TestGitMixedDisplay(t *testing.T) {
	g := GitStatus{Modified: 2, Added: 1, Deleted: 3, Unpushed: 5}
	want := "~2 +1 -3 ↑5"
	if g.Display() != want {
		t.Errorf("got %q, want %q", g.Display(), want)
	}
}

func TestRebaseAgeHours(t *testing.T) {
	g := GitStatus{RebaseAgeSecs: intPtr(7200)}
	if g.RebaseAgeDisplay() != "↻ 2h" {
		t.Errorf("got %q", g.RebaseAgeDisplay())
	}
}

func TestRebaseAgeDays(t *testing.T) {
	g := GitStatus{RebaseAgeSecs: intPtr(172800)}
	if g.RebaseAgeDisplay() != "↻ 2d" {
		t.Errorf("got %q", g.RebaseAgeDisplay())
	}
}

func TestRebaseAgeUnderThreshold(t *testing.T) {
	g := GitStatus{RebaseAgeSecs: intPtr(1800)}
	if g.RebaseAgeDisplay() != "" {
		t.Errorf("got %q, want empty", g.RebaseAgeDisplay())
	}
}

// --- PRStatus display tests ---

func TestPRNoNumber(t *testing.T) {
	p := PRStatus{}
	if p.Display() != "—" {
		t.Errorf("got %q, want —", p.Display())
	}
}

func TestPRPassingApproved(t *testing.T) {
	p := PRStatus{Number: 42, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED"}
	d := p.Display()
	for _, want := range []string{"#42", "✓", "☑"} {
		if !strings.Contains(d, want) {
			t.Errorf("display %q missing %q", d, want)
		}
	}
}

func TestPRFailing(t *testing.T) {
	p := PRStatus{Number: 10, State: "OPEN", Checks: "fail"}
	if !strings.Contains(p.Display(), "✗") {
		t.Errorf("display %q missing ✗", p.Display())
	}
}

func TestPRUnresolved(t *testing.T) {
	p := PRStatus{Number: 10, State: "OPEN", UnresolvedComments: 3}
	if !strings.Contains(p.Display(), "☐ 3") {
		t.Errorf("display %q missing ☐ 3", p.Display())
	}
}

func TestPRNoReviewersWarning(t *testing.T) {
	p := PRStatus{Number: 5, State: "OPEN", Checks: "pass", ReviewersRequested: 0}
	if !strings.Contains(p.Display(), "⚠") {
		t.Errorf("display %q missing ⚠ for no reviewers", p.Display())
	}
}

func TestPRWithReviewersNoWarning(t *testing.T) {
	p := PRStatus{Number: 5, State: "OPEN", Checks: "pass", ReviewersRequested: 1}
	if strings.Contains(p.Display(), "⚠") {
		t.Errorf("display %q should not have ⚠ when reviewers requested", p.Display())
	}
}

func TestPRNoReviewersButApprovedNoWarning(t *testing.T) {
	p := PRStatus{Number: 5, State: "OPEN", Checks: "pass", ReviewDecision: "APPROVED", ReviewersRequested: 0}
	if strings.Contains(p.Display(), "⚠") {
		t.Errorf("display %q should not have ⚠ when already approved", p.Display())
	}
}

// --- Sort tests ---

func TestStateSortOrder(t *testing.T) {
	sessions := []*Session{
		{Name: "idle", PanePath: "/"},
		{Name: "bell", PanePath: "/", HasBell: true},
		{Name: "done", PanePath: "/", PR: &PRStatus{Number: 1, State: "MERGED"}},
	}
	SortSessions(sessions, SortState)
	want := []string{"bell", "done", "idle"}
	for i, s := range sessions {
		if s.Name != want[i] {
			t.Errorf("index %d: got %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestAlphaSortOrder(t *testing.T) {
	sessions := []*Session{
		{Name: "charlie", PanePath: "/"},
		{Name: "Alpha", PanePath: "/"},
		{Name: "bravo", PanePath: "/"},
	}
	SortSessions(sessions, SortAlpha)
	want := []string{"Alpha", "bravo", "charlie"}
	for i, s := range sessions {
		if s.Name != want[i] {
			t.Errorf("index %d: got %q, want %q", i, s.Name, want[i])
		}
	}
}


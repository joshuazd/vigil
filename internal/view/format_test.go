package view

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jzinkduda/vigil/internal/session"
)

func indicatorLeft(s *session.Session, selected bool) string {
	return string([]rune(stripANSIForTest(Indicator(s, selected)))[0])
}

func TestIndicatorMarks(t *testing.T) {
	cases := []struct {
		name     string
		s        session.Session
		selected bool
		want     string
	}{
		{"a working agent", session.Session{Agent: session.AgentWorking}, false, "⠋"},
		{"a bell with no agent", session.Session{HasBell: true}, false, "*"},
		{"a bell from a working agent", session.Session{Agent: session.AgentWorking, HasBell: true}, false, "⠋"},
		{"a bell from a done agent", session.Session{Agent: session.AgentDone, HasBell: true}, false, " "},
		{"selection over a working agent", session.Session{Agent: session.AgentWorking}, true, "◆"},
		{"an idle agent", session.Session{Agent: session.AgentIdle}, false, " "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := indicatorLeft(&c.s, c.selected); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEveryStateHasAColourAndDot(t *testing.T) {
	for _, st := range session.AllStates() {
		if _, ok := StateColor[st]; !ok {
			t.Errorf("%v has no colour", st)
		}
		if _, ok := StateDot[st]; !ok {
			t.Errorf("%v has no dot", st)
		}
	}
}

func TestTheSpinnerAdvancesWithTheFrameAndWraps(t *testing.T) {
	s := &session.Session{Agent: session.AgentWorking}
	first := stripANSIForTest(IndicatorWithBg(s, false, nil, 0))
	second := stripANSIForTest(IndicatorWithBg(s, false, nil, 1))
	if first == second {
		t.Errorf("frames 0 and 1 both render %q", first)
	}
	if wrapped := stripANSIForTest(IndicatorWithBg(s, false, nil, len(spinnerFrames))); wrapped != first {
		t.Errorf("frame %d renders %q, want it to wrap to %q", len(spinnerFrames), wrapped, first)
	}
}

func TestDot(t *testing.T) {
	cases := []struct {
		name string
		s    session.Session
		want string
	}{
		{"an agent asking", session.Session{Agent: session.AgentNeedsInput}, "?"},
		{"a bell", session.Session{HasBell: true}, "●"},
		{"a finished agent", session.Session{Agent: session.AgentDone}, "✓"},
		{"no agent, no PR", session.Session{}, "·"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dot(&c.s); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestTheStateColumnAndDetailHeaderShowTheQuestionMark(t *testing.T) {
	s := &session.Session{Name: "alpha", Agent: session.AgentNeedsInput}
	if got := stripANSIForTest(StateIndicator(s)); got != "?" {
		t.Errorf("state column got %q, want ?", got)
	}
	if got := stripANSIForTest(RenderDetail(s, DetailPane, "", nil, 86400, 80, 10)); !strings.Contains(got, "? attention") {
		t.Errorf("detail header missing \"? attention\":\n%s", got)
	}
}

func TestAQueuedPRRendersOnlyItsNumberInYellow(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	pr := &session.PRStatus{
		Number: 42, State: "OPEN", Checks: "pending", ReviewDecision: "APPROVED",
		UnresolvedComments: 2, HasConflicts: true, InMergeQueue: true,
	}
	got := ColorizePR(pr)
	if want := lipgloss.NewStyle().Foreground(Yellow).Render("#42"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	pr.InMergeQueue = false
	if stripANSIForTest(ColorizePR(pr)) == "#42" {
		t.Error("the fixture renders only its number outside the queue too, so the assertion above proves nothing")
	}
}

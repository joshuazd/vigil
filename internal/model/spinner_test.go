package model

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func workingSession(name string) *session.Session {
	s := idleSession(name)
	s.Agent = session.AgentWorking
	return s
}

func TestASnapshotWithAWorkingAgentStartsTheSpinner(t *testing.T) {
	for _, local := range []bool{true, false} {
		var m Model
		if local {
			m = transitionModel()
		} else {
			m = fakeDaemonClient()
		}
		next, cmd := m.Update(SnapshotMsg{Sessions: []*session.Session{workingSession("alpha")}, Local: local})
		m = next.(Model)
		drain(cmd)
		if !m.spinning {
			t.Errorf("local=%v: spinner not started", local)
		}
	}
}

func TestOnlyOneSpinnerChainRuns(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{workingSession("alpha")}
	if m.ensureSpinner() == nil {
		t.Fatal("first ensureSpinner started nothing")
	}
	if m.ensureSpinner() != nil {
		t.Error("second ensureSpinner started another chain")
	}
}

func TestNoSpinnerWithoutAWorkingAgent(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{waitingSession("alpha")}
	if m.ensureSpinner() != nil || m.spinning {
		t.Error("spinner started with nothing working")
	}
}

func TestSpinnerTickAdvancesWhileWorking(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{workingSession("alpha")}
	m.spinning = true
	next, cmd := m.Update(SpinnerTickMsg{})
	m = next.(Model)
	if m.spinFrame != 1 {
		t.Errorf("got frame %d, want 1", m.spinFrame)
	}
	if cmd == nil {
		t.Error("tick did not reschedule while working")
	}
}

func TestSpinnerChainEndsWhenNothingIsWorking(t *testing.T) {
	m := transitionModel()
	m.sessions = []*session.Session{waitingSession("alpha")}
	m.spinning = true
	next, cmd := m.Update(SpinnerTickMsg{})
	m = next.(Model)
	if cmd != nil {
		t.Error("tick rescheduled with nothing working")
	}
	if m.spinning {
		t.Error("spinning left set, so the spinner can never restart")
	}
}

func TestEveryViewDrawsTheCurrentSpinnerFrame(t *testing.T) {
	for _, panel := range []bool{false, true} {
		m := newTestModel()
		m.panelMode = panel
		m.detailOpen = false
		m.width, m.height = 120, 20
		m.sessions = []*session.Session{workingSession("alpha")}
		m.spinFrame = 3
		out := m.View()
		if !strings.Contains(out, "⠸") || strings.Contains(out, "⠋") {
			t.Errorf("panel=%v: frame 3 not drawn:\n%s", panel, out)
		}
	}
}

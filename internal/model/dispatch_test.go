package model

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// TestASnapshotsJobsReachTheModel exercises the daemon path
// (handleSnapshot's non-Local branch), which is dropped entirely without a
// live daemonConn or daemonDecoder (see
// TestHandleSnapshotDropsANonLocalSnapshotWithNoConnection in client_test.go).
// liveDecoder gives it one without a real daemon on the other end.
func TestASnapshotsJobsReachTheModel(t *testing.T) {
	m := newTestModel()
	m.daemonDecoder = liveDecoder()
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs: []protocol.Job{{
			ID: "a", Input: "sc-12345", State: protocol.JobRunning, Status: "classifying",
		}},
	})
	got := updated.(Model)
	if len(got.jobs) != 1 || got.jobs[0].ID != "a" {
		t.Fatalf("got jobs %+v", got.jobs)
	}
	if !strings.Contains(got.View(), "sc-12345") {
		t.Error("the view does not show the job")
	}
}

func TestThePanelShowsTheJobLineToo(t *testing.T) {
	m := panelModel(t)
	m.daemonDecoder = liveDecoder()
	updated, _ := m.Update(SnapshotMsg{
		Epoch: m.epoch,
		Jobs:  []protocol.Job{{ID: "a", Input: "sc-1", State: protocol.JobRunning}},
	})
	if !strings.Contains(updated.(Model).View(), "sc-1") {
		t.Error("the panel does not show the job line")
	}
}

func TestNoJobsMeansNoJobLine(t *testing.T) {
	m := newTestModel()
	m.daemonDecoder = liveDecoder()
	m.width, m.height = 100, 30
	updated, _ := m.Update(SnapshotMsg{Epoch: m.epoch})
	if strings.Contains(updated.(Model).View(), "⚡") {
		t.Error("the view shows a job line with no jobs")
	}
}

func TestTheDispatchKeySubmitsAndValidates(t *testing.T) {
	m := newTestModel()
	m.dispatchActive = true
	m.dispatchInput.SetValue("   ")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.dispatchActive {
		t.Error("the input stayed open")
	}
	if cmd == nil {
		t.Fatal("no command was returned")
	}
	msg := cmd()
	result, ok := msg.(ActionResultMsg)
	if !ok {
		t.Fatalf("got %T, want ActionResultMsg", msg)
	}
	if result.OK {
		t.Error("empty input was accepted")
	}
	if !strings.Contains(result.Message, "empty") {
		t.Errorf("got %q, want an empty-input reason", result.Message)
	}
}

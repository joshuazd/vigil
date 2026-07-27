package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// staleTickCases covers every self-rescheduling tick. A tick born in an
// earlier epoch must die rather than reschedule itself, or a mode switch
// leaves two independent tickers running for the life of the process.
func TestStaleTicksDoNotReschedule(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 0}},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 0}},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 0}},
		{"render", RenderTickMsg{Time: time.Now(), Epoch: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 1
			if _, cmd := m.Update(tc.msg); cmd != nil {
				t.Error("a stale tick produced a command")
			}
		})
	}
}

func TestCurrentTicksReschedule(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"tmux", TmuxTickMsg{Time: time.Now(), Epoch: 7}},
		{"git", GitTickMsg{Time: time.Now(), Epoch: 7}},
		{"pr", PRTickMsg{Time: time.Now(), Epoch: 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.epoch = 7
			if _, cmd := m.Update(tc.msg); cmd == nil {
				t.Error("a current tick produced no command")
			}
		})
	}
}

func TestStaleSnapshotIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	m.sessions = nil
	got, _ := m.Update(SnapshotMsg{Sessions: fixtureSessions(), Epoch: 1})
	if len(got.(Model).sessions) != 0 {
		t.Error("a snapshot from a closed connection was applied")
	}
}

func TestStaleDaemonLostIsIgnored(t *testing.T) {
	m := newTestModel()
	m.epoch = 2
	got, cmd := m.Update(DaemonLostMsg{Epoch: 1})
	if cmd != nil {
		t.Error("a stale daemon-lost restarted the fallback poll loops")
	}
	if len(got.(Model).notifications) != 0 {
		t.Error("a stale daemon-lost notified the user")
	}
}

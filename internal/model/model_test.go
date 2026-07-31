package model

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/session"
)

func modelWithQueue(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.sessions = []*session.Session{{Name: "alpha", PanePath: "/repo/alpha"}}
	m.queue = []session.QueueItem{
		{Kind: session.QueueStory, ID: "223480", Title: "Backfill", Input: "https://app.shortcut.com/huntress/story/223480"},
		{Kind: session.QueueReview, ID: "34967", Repo: "portal", Title: "Timeline", Input: "https://github.com/huntresslabs/portal/pull/34967"},
	}
	m.width, m.height = 120, 40
	return m
}

func TestRowCountSpansSessionsAndQueue(t *testing.T) {
	m := modelWithQueue(t)
	if got := m.rowCount(); got != 3 {
		t.Errorf("rowCount() = %d, want 3 (1 session + 2 queue)", got)
	}
}

func TestQueueCursorTranslatesFromTheGlobalCursor(t *testing.T) {
	m := modelWithQueue(t)
	tests := []struct{ cursor, want int }{
		{0, -1}, // the session row
		{1, 0},  // first queue row
		{2, 1},  // second queue row
	}
	for _, tt := range tests {
		m.cursor = tt.cursor
		if got := m.queueCursor(); got != tt.want {
			t.Errorf("cursor=%d: queueCursor() = %d, want %d", tt.cursor, got, tt.want)
		}
	}
}

// TestSelectedSessionIsNilOnAQueueRow is why session actions need no new
// guard: selectedSession already bounds-checks against visibleSessions, and
// every action handler goes through it or through batchSessions.
func TestSelectedSessionIsNilOnAQueueRow(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 1
	if s := m.selectedSession(); s != nil {
		t.Errorf("selectedSession() = %v on a queue row, want nil", s.Name)
	}
}

func TestBatchSessionsNeverIncludesQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.selected = map[string]bool{"sc-223480": true, "portal#34967": true, "alpha": true}
	batch := m.batchSessions()
	if len(batch) != 1 || batch[0].Name != "alpha" {
		t.Errorf("batchSessions() = %v, want just alpha", batch)
	}
}

// TestCursorWrapsOverQueueRows is what makes j/k reach the queue at all.
func TestCursorWrapsOverQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got := next.(Model).cursor; got != 0 {
		t.Errorf("cursor after j from the last queue row = %d, want 0", got)
	}

	m.cursor = 0
	prev, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got := prev.(Model).cursor; got != 2 {
		t.Errorf("cursor after k from the first session = %d, want 2", got)
	}
}

func TestViewRendersTheQueueSection(t *testing.T) {
	m := modelWithQueue(t)
	out := m.View()
	if !strings.Contains(out, "QUEUE") {
		t.Errorf("dashboard view has no QUEUE section:\n%s", out)
	}
	if !strings.Contains(out, "sc-223480") {
		t.Errorf("dashboard view missing a queue row:\n%s", out)
	}
}

// TestPanelShowsTheBadgeAndNoQueueRows is the measured constraint made
// executable: the panel is 9 rows with sessions already in them.
func TestPanelShowsTheBadgeAndNoQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.panelMode = true
	m.width, m.height = 152, 9

	out := m.View()
	if !strings.Contains(out, "⚡2") {
		t.Errorf("panel missing the queue badge:\n%s", out)
	}
	if strings.Contains(out, "QUEUE") || strings.Contains(out, "sc-223480") {
		t.Errorf("panel rendered queue rows:\n%s", out)
	}
}

// TestHandleSnapshotStoresTheQueue goes through handleSnapshot, which is where
// the assignment lives. An earlier draft of this test called applySnapshot,
// which never touches the queue - it would have passed with the subject
// deleted, which is the exact defect class this repo has hit ten times.
func TestHandleSnapshotStoresTheQueue(t *testing.T) {
	m := newTestModel()
	items := []session.QueueItem{{Kind: session.QueueStory, ID: "1", Title: "x", Input: "sc-1"}}

	next, _ := m.handleSnapshot(SnapshotMsg{Epoch: m.epoch, Local: true, Queue: items, QueueHidden: 2})
	got := next.(Model)

	if len(got.queue) != 1 {
		t.Fatalf("queue has %d items, want 1", len(got.queue))
	}
	if got.queue[0].ID != "1" {
		t.Errorf("queue[0].ID = %q, want 1", got.queue[0].ID)
	}
	if got.queueHidden != 2 {
		t.Errorf("queueHidden = %d, want 2", got.queueHidden)
	}
}

// TestEnterOnAQueueRowDispatchesDetached pins both halves: the right input and
// the detached flag. Without the flag assertion, a regression to
// dispatchCmd(input, false) is silent and the user gets teleported.
func TestEnterOnAQueueRowDispatchesDetached(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 2 // portal#34967

	input, detached, ok := m.queueDispatchTarget()
	if !ok {
		t.Fatal("queueDispatchTarget() reported no target on a queue row")
	}
	if input != "https://github.com/huntresslabs/portal/pull/34967" {
		t.Errorf("input = %q, want the item's URL", input)
	}
	if !detached {
		t.Error("detached = false, want true: a queue selection must not teleport")
	}

	m.cursor = 0
	if _, _, ok := m.queueDispatchTarget(); ok {
		t.Error("queueDispatchTarget() reported a target on a session row")
	}
}

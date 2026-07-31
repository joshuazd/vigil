package model

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
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

// TestPanelCursorNeverReachesQueueRows is the design's rejected-alternative
// made executable: a panel never draws queue rows (they would either displace
// sessions or be invisible), so the cursor must not be able to reach them
// either. Without this, j past the last session in a panel with a non-empty
// queue lands the cursor on a row the panel never renders - the highlight
// vanishes, and enter there would fire a detached dispatch of an item the
// user cannot see.
func TestPanelCursorNeverReachesQueueRows(t *testing.T) {
	m := panelModel(t)
	m.queue = []session.QueueItem{
		{Kind: session.QueueStory, ID: "1", Title: "x", Input: "sc-1"},
		{Kind: session.QueueReview, ID: "2", Title: "y", Input: "sc-2"},
	}
	m.cursor = len(m.sessions) - 1 // the last session row

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got := next.(Model).cursor; got != 0 {
		t.Errorf("cursor after j from the last session in a panel = %d, want 0 (wrap to sessions, never a queue row)", got)
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

	// Sessions must be non-nil: a real successful collectCmd call always
	// pairs a non-nil Sessions slice with the Queue it computed from those
	// same sessions (collect.go), and handleSnapshot's failed-poll guard
	// (nil Sessions) is what TestAFailedLocalSnapshotLeavesTheQueueIntact
	// covers below.
	next, _ := m.handleSnapshot(SnapshotMsg{Epoch: m.epoch, Local: true, Sessions: []*session.Session{}, Queue: items, QueueHidden: 2})
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

// TestAFailedLocalSnapshotLeavesTheQueueIntact is Finding 2 made executable:
// collectCmd returns a nil Queue alongside a nil Sessions on a failed poll,
// and handleSnapshot must not let that blank a queue a prior successful poll
// populated - the same "leave state alone" rule Sessions already gets.
func TestAFailedLocalSnapshotLeavesTheQueueIntact(t *testing.T) {
	m := newTestModel()
	m.queue = []session.QueueItem{{Kind: session.QueueStory, ID: "1", Title: "x", Input: "sc-1"}}
	m.queueHidden = 3

	next, _ := m.handleSnapshot(SnapshotMsg{Epoch: m.epoch, Local: true})
	got := next.(Model)

	if len(got.queue) != 1 || got.queue[0].ID != "1" {
		t.Errorf("queue = %+v, want the prior queue left untouched", got.queue)
	}
	if got.queueHidden != 3 {
		t.Errorf("queueHidden = %d, want 3 (untouched)", got.queueHidden)
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

// TestDashboardViewFitsItsHeightWithQueue pins the height arithmetic: without
// `tableHeight -= lipgloss.Height(queueSection)`, the table keeps its
// pre-queue height and the queue section is added on top, so the dashboard
// overflows m.height by the queue section's height. TestPanelViewFitsItsPane
// is this test's panel-side sibling; the dashboard had no such assertion at
// all before this.
func TestDashboardViewFitsItsHeightWithQueue(t *testing.T) {
	m := modelWithQueue(t)
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Errorf("rendered %d lines, want %d (m.height)", len(lines), m.height)
	}
}

// TestDashboardTableIsNotShortenedByAnEmptyQueue is the mirror trap:
// lipgloss.Height("") is 1, not 0, so an unconditional subtraction shrinks
// the table by a row whenever the queue is empty, which is most machines most
// of the time.
//
// A total-line-count assertion (the obvious first attempt) does not catch
// this: View()'s gap-fill padding always tops the output up to exactly
// m.height by adding one more blank filler line, so a table that is one row
// short is invisible to a line count - confirmed by mutation testing below.
// What the bug actually does is cut the last session row off RenderTable's
// output entirely, so this asserts that row survives.
func TestDashboardTableIsNotShortenedByAnEmptyQueue(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40

	want := m.tableHeight(false) // no queue, no job line: the un-reduced height
	for i := 0; i < want; i++ {
		m.sessions = append(m.sessions, &session.Session{Name: fmt.Sprintf("session-%02d", i), PanePath: "/repo"})
	}

	out := m.View()
	last := fmt.Sprintf("session-%02d", want-1)
	if !strings.Contains(out, last) {
		t.Errorf("last session row %q is missing: an empty queue shrank the table by a row\n%s", last, out)
	}
}

// TestEnterOnAQueueRowDispatchesOverTheWire is
// TestEnterOnAQueueRowDispatchesDetached's missing other half: that test only
// calls queueDispatchTarget directly, so deleting the routing block in
// handleSelect (or the Detached field on the dispatch.Options literal in
// dispatchCmd) leaves the whole suite green. This drives the real key through
// Update, runs the returned tea.Cmd against a fake daemon socket (the pattern
// TestTheDispatchKeySubmitsValidInputToTheDaemon already uses in
// dispatch_test.go), and decodes the protocol.Request the daemon actually
// received.
func TestEnterOnAQueueRowDispatchesOverTheWire(t *testing.T) {
	dir, err := os.MkdirTemp("", "vd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	sockDir := filepath.Join(dir, "vigil")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(sockDir, "vigild.sock"))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	reqCh := make(chan *protocol.Request, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		req, err := protocol.NewRequestDecoder(conn).Next()
		if err != nil {
			return
		}
		reqCh <- req
		_ = protocol.Encode(conn, &protocol.Snapshot{
			Version: protocol.Version,
			Jobs:    []protocol.Job{{ID: req.ID, Input: req.Input, State: protocol.JobQueued}},
		})
	}()

	m := modelWithQueue(t)
	m.cursor = 2 // portal#34967

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a queue row produced no command")
	}
	msg := cmd()
	result, ok := msg.(ActionResultMsg)
	if !ok {
		t.Fatalf("got %T, want ActionResultMsg", msg)
	}
	if !result.OK {
		t.Fatalf("got %+v, want OK", result)
	}

	var req *protocol.Request
	select {
	case req = <-reqCh:
	default:
		t.Fatal("the daemon never received a request")
	}
	if req.Input != "https://github.com/huntresslabs/portal/pull/34967" {
		t.Errorf("req.Input = %q, want the queue item's URL", req.Input)
	}
	if !req.Detached {
		t.Error("req.Detached = false, want true: a queue selection must not teleport")
	}
}

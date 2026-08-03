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

// TestApplySnapshotClampsTheCursorToTheNewRowCount is Minor 3: a session
// leaving between polls (auto_cleanup, most often) can leave the cursor
// pointing past the end of the new session+queue space. Left alone, enter
// would dispatch whatever queue row the cursor's stale absolute position now
// happens to land on - unconfirmed, unlike every session action a stale
// cursor could otherwise mis-target.
func TestApplySnapshotClampsTheCursorToTheNewRowCount(t *testing.T) {
	m := newTestModel()
	m.queue = []session.QueueItem{
		{Kind: session.QueueStory, ID: "1", Title: "x", Input: "sc-1"},
		{Kind: session.QueueReview, ID: "2", Title: "y", Input: "sc-2"},
	}
	// 5 sessions + 2 queue items = 7 rows; cursor on the last one (queue row 1).
	m.cursor = 6

	m.applySnapshot([]*session.Session{
		{Name: "alpha", PanePath: "/repo/alpha"},
		{Name: "beta", PanePath: "/repo/beta"},
	})

	if want := m.rowCount() - 1; m.cursor != want {
		t.Errorf("cursor = %d, want %d (rowCount()-1, clamped)", m.cursor, want)
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

// TestHandleSnapshotStoresTheQueueFromTheDaemon is
// TestHandleSnapshotStoresTheQueue's daemon-fed half: that test only drives
// the Local branch, so deleting `m.queue = msg.Queue` / `m.queueHidden =
// msg.QueueHidden` from the non-local branch left the whole suite green - no
// vigil client had ever been observed rendering a daemon-published queue, in
// a test or on the real machine. This feeds a Snapshot through the
// daemon-fed path (Local: false) and asserts the row actually renders,
// rather than just checking the model's fields, since the fields alone
// would not have caught the same class of defect in View() itself.
func TestHandleSnapshotStoresTheQueueFromTheDaemon(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}
	m.width, m.height = 120, 40
	items := []session.QueueItem{{Kind: session.QueueStory, ID: "223480", Title: "Backfill", Input: "sc-223480"}}

	next, _ := m.handleSnapshot(SnapshotMsg{
		Epoch:       m.epoch,
		Local:       false,
		Sessions:    []*session.Session{{Name: "alpha", PanePath: "/repo/alpha"}},
		Queue:       items,
		QueueHidden: 2,
	})
	got := next.(Model)

	if len(got.queue) != 1 || got.queue[0].ID != "223480" {
		t.Fatalf("queue = %+v, want one item with ID 223480", got.queue)
	}
	if got.queueHidden != 2 {
		t.Errorf("queueHidden = %d, want 2", got.queueHidden)
	}

	out := got.View()
	if !strings.Contains(out, "QUEUE") {
		t.Errorf("daemon-fed dashboard has no QUEUE section:\n%s", out)
	}
	if !strings.Contains(out, "sc-223480") {
		t.Errorf("daemon-fed dashboard missing the queue row:\n%s", out)
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

// TestQueueCursorReclampsWhenJobsShrinkTheDrawnRows is the regression from
// d4c0391 made executable: rowCount()'s clamp in applySnapshot depends on
// m.jobs (via drawnQueueRows -> tableHeight -> RenderJobLine), but both
// handleSnapshot branches used to assign m.jobs after calling applySnapshot,
// so the clamp ran against the previous tick's job set.
//
// At height 24 with 8 sessions and a 20-item queue, drawnQueueRows() is 5
// with no jobs and 4 once a job line takes a row (mirroring
// TestCursorNeverOutrunsTheDrawnQueueRows's own height-24 case). The cursor
// starts at 12, the last drawn row before the job arrives. A correctly
// ordered clamp reclamps it to 11 (8 sessions + 4 drawn rows - 1) in the same
// call that installs the job, so queueDispatchTarget() must dispatch queue
// item 3 - the new last drawn row - not report no target and not dispatch
// the stale item 4.
func TestQueueCursorReclampsWhenJobsShrinkTheDrawnRows(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}
	m.width, m.height = 120, 24
	m.detailOpen = true
	m.sessions = make([]*session.Session, 8)
	for i := range m.sessions {
		m.sessions[i] = &session.Session{
			Name:     fmt.Sprintf("session-%d", i),
			PanePath: fmt.Sprintf("/repo/session-%d", i),
		}
	}
	m.queue = make([]session.QueueItem, 20)
	for i := range m.queue {
		m.queue[i] = session.QueueItem{
			Kind:  session.QueueStory,
			ID:    fmt.Sprintf("%d", i),
			Title: fmt.Sprintf("item %d", i),
			Input: fmt.Sprintf("sc-%d", i),
		}
	}
	if got := m.drawnQueueRows(); got != 5 {
		t.Fatalf("precondition: drawnQueueRows() = %d, want 5 with no jobs", got)
	}
	m.cursor = len(m.sessions) + m.drawnQueueRows() - 1 // 12

	jobs := []protocol.Job{{ID: "j1", Input: "sc-1", State: protocol.JobRunning}}
	next, _ := m.handleSnapshot(SnapshotMsg{
		Epoch:    m.epoch,
		Local:    false,
		Sessions: m.sessions,
		Queue:    m.queue,
		Jobs:     jobs,
	})
	got := next.(Model)

	if drawn := got.drawnQueueRows(); drawn != 4 {
		t.Fatalf("precondition: drawnQueueRows() with the job line = %d, want 4", drawn)
	}
	input, _, ok := got.queueDispatchTarget()
	if !ok {
		t.Fatal("queueDispatchTarget() reported no target after the reclamp: the cursor was left stranded past the drawn rows")
	}
	if input != "sc-3" {
		t.Errorf("queueDispatchTarget() = %q, want sc-3 (the reclamped cursor's row)", input)
	}
}

// TestQueueDispatchTargetIsBoundedByDrawnRowsAfterGeometryChanges covers the
// second route to the same failure: a geometry change (here, closing the
// detail panel) shrinks drawnQueueRows() without any clamp running at all,
// since only applySnapshot's clamp exists and no snapshot arrives on a key
// press. queueDispatchTarget() must fail closed on its own rather than rely
// on a clamp having run - measured at dashboard height 40, 8 sessions and a
// 20-item queue: detail open gives drawnQueueRows() 13 (cursor 27 sits on
// the last drawn row, matching TestCursorNeverOutrunsTheDrawnQueueRows),
// detail closed gives drawnQueueRows() 20, so the same cursor is left seven
// rows past the drawn section with nothing to reclamp it.
func TestQueueDispatchTargetIsBoundedByDrawnRowsAfterGeometryChanges(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.detailOpen = false
	m.sessions = make([]*session.Session, 8)
	for i := range m.sessions {
		m.sessions[i] = &session.Session{
			Name:     fmt.Sprintf("session-%d", i),
			PanePath: fmt.Sprintf("/repo/session-%d", i),
		}
	}
	m.queue = make([]session.QueueItem, 20)
	for i := range m.queue {
		m.queue[i] = session.QueueItem{
			Kind:  session.QueueStory,
			ID:    fmt.Sprintf("%d", i),
			Title: fmt.Sprintf("item %d", i),
			Input: fmt.Sprintf("sc-%d", i),
		}
	}
	if got := m.drawnQueueRows(); got != 20 {
		t.Fatalf("precondition: drawnQueueRows() with detail closed = %d, want 20", got)
	}
	m.cursor = len(m.sessions) + m.drawnQueueRows() - 1 // 27, the last drawn row

	m.detailOpen = true
	if got := m.drawnQueueRows(); got != 13 {
		t.Fatalf("precondition: drawnQueueRows() with detail open = %d, want 13", got)
	}

	if _, _, ok := m.queueDispatchTarget(); ok {
		t.Error("queueDispatchTarget() reported a target for a row the detail toggle pushed out of the drawn section")
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

// TestDashboardViewFitsItsHeightAcrossQueueLengths is the whole-branch
// review's sweep: TestDashboardViewFitsItsHeightWithQueue above only ever
// exercised one height, one queue length and one detailOpen state, so nothing
// caught RenderQueue growing without a bound - a long queue could push the
// combined table+queue section past m.height regardless of the table's own
// 1-row floor. In an alt-screen Bubble Tea program, rendering more lines than
// the terminal has scrolls the frame away and corrupts the display until a
// resize.
//
// The first three cases are the reviewer's own measured overflow: height
// 40/24/12 with a 20-item queue, which overflowed by +3, +11 and +12 lines
// respectively before the fix.
func TestDashboardViewFitsItsHeightAcrossQueueLengths(t *testing.T) {
	type tc struct {
		height     int
		queueLen   int
		detailOpen bool
	}
	cases := []tc{
		{40, 20, true},
		{24, 20, true},
		{12, 20, false},

		{40, 0, true},
		{40, 0, false},
		{40, 1, false},
		{40, 5, true},
		{40, 20, false},
		{24, 0, true},
		{24, 0, false},
		{24, 5, false},
		{24, 20, false},
		{12, 0, false},
		{12, 0, true},
		{12, 1, false},
		{12, 20, true},
		{10, 20, true},
		{10, 20, false},
		{10, 0, false},
	}

	for _, c := range cases {
		m := newTestModel()
		m.width, m.height = 120, c.height
		m.detailOpen = c.detailOpen
		m.paneContent = "some pane output\nline two\nline three"
		m.sessions = []*session.Session{
			{Name: "alpha", PanePath: "/repo/alpha"},
			{Name: "beta", PanePath: "/repo/beta"},
			{Name: "gamma", PanePath: "/repo/gamma"},
			{Name: "delta", PanePath: "/repo/delta"},
			{Name: "epsilon", PanePath: "/repo/epsilon"},
		}
		m.queue = make([]session.QueueItem, c.queueLen)
		for i := range m.queue {
			m.queue[i] = session.QueueItem{
				Kind:  session.QueueStory,
				ID:    fmt.Sprintf("%d", i),
				Title: fmt.Sprintf("item %d", i),
			}
		}

		lines := strings.Split(m.View(), "\n")
		if len(lines) > c.height {
			t.Errorf("height=%d queue=%d detailOpen=%v: rendered %d lines, overflow +%d",
				c.height, c.queueLen, c.detailOpen, len(lines), len(lines)-c.height)
		}
	}
}

// TestCursorNeverOutrunsTheDrawnQueueRows is the reviewer's finding made
// executable: rowCount() used to span all of m.queue while View() drew only
// a fraction of it (truncating to a "... +N more" line), so a cursor could
// land on a queue row the frame draws no marker for at all - j walked the
// highlight off the bottom into nothing, and enter there fired an
// unconfirmed detached dispatch of an item the user could not see.
//
// The four heights are the reviewer's own measurement, taken with 8
// sessions, a 20-item queue and the detail panel open (the dashboard
// default): 13/8/5/0 queue rows drawn at heights 40/30/24/12. Those counts
// are pinned directly, and then every cursor value rowCount() admits is
// swept: the property that actually matters is that each one leaves a
// visible marker somewhere in the frame.
//
// The marker check compares against a cursor=-1 baseline rendered from the
// same model (which selectedSession() and queueCursor() both treat the same
// as any cursor past the end of the queue's drawn rows, since neither
// special-cases -1), isolating exactly the cursor's own effect on the frame
// from anything else height or content might change. A queue row's marker is
// the literal text "> ", which survives lipgloss's default no-color test
// profile, so no color profile needs forcing.
func TestCursorNeverOutrunsTheDrawnQueueRows(t *testing.T) {
	cases := []struct {
		height    int
		wantDrawn int
	}{
		{40, 13},
		{30, 8},
		{24, 5},
		{12, 0},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("height=%d", tc.height), func(t *testing.T) {
			m := newTestModel()
			m.width, m.height = 120, tc.height
			m.detailOpen = true
			m.sessions = make([]*session.Session, 8)
			for i := range m.sessions {
				m.sessions[i] = &session.Session{
					Name:     fmt.Sprintf("session-%d", i),
					PanePath: fmt.Sprintf("/repo/session-%d", i),
				}
			}
			m.queue = make([]session.QueueItem, 20)
			for i := range m.queue {
				m.queue[i] = session.QueueItem{
					Kind:  session.QueueStory,
					ID:    fmt.Sprintf("%d", i),
					Title: fmt.Sprintf("item %d", i),
				}
			}

			if got := m.drawnQueueRows(); got != tc.wantDrawn {
				t.Fatalf("drawnQueueRows() = %d, want %d", got, tc.wantDrawn)
			}
			if want := len(m.sessions) + tc.wantDrawn; m.rowCount() != want {
				t.Fatalf("rowCount() = %d, want %d (sessions + drawn queue rows)", m.rowCount(), want)
			}

			baseline := m
			baseline.cursor = -1
			baselineFrame := baseline.View()

			for c := 0; c < m.rowCount(); c++ {
				mc := m
				mc.cursor = c
				if mc.View() == baselineFrame {
					t.Errorf("cursor=%d: no cursor marker anywhere in the frame", c)
				}
			}
		})
	}
}

// TestDashboardShowsTheQueueBadgeWhenTooShortToDrawTheSection is the other
// half of the reviewer's finding: at height 12 the QUEUE section draws
// nothing at all (drawnQueueRows() == 0 above), and until now View() passed
// a literal 0 for the status bar's queue count regardless, so a short
// dashboard with a real queue looked identical to an empty one. It should
// look like panelView's badge does, which is why the assertion mirrors
// TestPanelShowsTheBadgeAndNoQueueRows above.
func TestDashboardShowsTheQueueBadgeWhenTooShortToDrawTheSection(t *testing.T) {
	m := modelWithQueue(t)
	m.detailOpen = true
	m.height = 12

	out := m.View()
	if !strings.Contains(out, "⚡2") {
		t.Errorf("short dashboard missing the queue badge:\n%s", out)
	}
	if strings.Contains(out, "QUEUE") {
		t.Errorf("short dashboard rendered a QUEUE section it has no room for:\n%s", out)
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

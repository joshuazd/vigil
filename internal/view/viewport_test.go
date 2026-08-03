package view

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

// scrollFixture returns n sessions named sess-a, sess-b, ... Deliberately
// digit-free: TestTableKeepsAbsoluteIndexesAfterScrolling asserts on the digit
// in the index column, and a name like "session-05" contains digits that make
// that assertion pass whatever index was rendered.
func scrollFixture(n int) []*session.Session {
	sessions := make([]*session.Session, n)
	for i := range sessions {
		sessions[i] = &session.Session{
			Name: fmt.Sprintf("sess-%c", rune('a'+i)),
			Git:  session.GitStatus{Branch: "main"},
		}
	}
	return sessions
}

// Counts at and below height, not just equal to it: at count == height the
// count-height clamp already yields 0 on its own, so only count < height - where
// that clamp would go negative - actually needs the short circuit.
func TestTableWindowDoesNotScrollWhenEverythingFits(t *testing.T) {
	const height = 5
	for count := 1; count <= height; count++ {
		for cursor := range count + 3 {
			if got := TableWindow(cursor, count, height); got != 0 {
				t.Errorf("cursor %d of %d in %d rows: got offset %d, want 0", cursor, count, height, got)
			}
		}
	}
}

func TestTableWindowEndsAtTheCursorOncePastTheFirstScreen(t *testing.T) {
	// 10 sessions, 3 rows. Cursors 0-2 need no scroll; past that the window
	// ends at the cursor, so the offset trails it by height-1.
	for _, tc := range []struct{ cursor, want int }{
		{0, 0}, {1, 0}, {2, 0}, {3, 1}, {4, 2}, {9, 7},
	} {
		if got := TableWindow(tc.cursor, 10, 3); got != tc.want {
			t.Errorf("cursor %d of 10 in 3 rows: got offset %d, want %d", tc.cursor, got, tc.want)
		}
	}
}

// A cursor past the last session is on a queue row. The table must hold still
// rather than scrolling itself to the end or snapping back to the top.
func TestTableWindowHoldsStillForACursorInTheQueue(t *testing.T) {
	atLastSession := TableWindow(9, 10, 3)
	for _, cursor := range []int{10, 11, 25} {
		if got := TableWindow(cursor, 10, 3); got != atLastSession {
			t.Errorf("queue cursor %d: got offset %d, want %d (unchanged from the last session)", cursor, got, atLastSession)
		}
	}
}

func TestTableWindowNeverScrollsPastTheLastScreen(t *testing.T) {
	const count, height = 10, 3
	for cursor := range 40 {
		got := TableWindow(cursor, count, height)
		if got < 0 || got > count-height {
			t.Errorf("cursor %d: got offset %d, want within [0,%d]", cursor, got, count-height)
		}
	}
}

// Cursor 7 of 10 in 3 rows puts the window at 5,6,7 - sessions above it and
// sessions below it - so this pins the bound in both directions. The
// last-window test cannot: there the loop runs out of sessions before the
// height bound is ever reached.
func TestTableDrawsTheCursorRowWhenItIsPastTheVisibleHeight(t *testing.T) {
	sessions := scrollFixture(10)
	out := RenderTable(sessions, 7, nil, 0, 120, 3, "")

	if !strings.Contains(out, "sess-h") {
		t.Errorf("cursor row sess-h (index 7) was not drawn:\n%s", out)
	}
	if strings.Contains(out, "sess-a") {
		t.Errorf("sess-a should have scrolled out of a 3 row window:\n%s", out)
	}
	if strings.Contains(out, "sess-i") || strings.Contains(out, "sess-j") {
		t.Errorf("rows below a 3 row window ending at the cursor should not be drawn:\n%s", out)
	}
	if n := len(strings.Split(out, "\n")); n != 3 {
		t.Errorf("got %d lines, want exactly the 3 allocated:\n%s", n, out)
	}
}

// The index column and the 1-9 jump keys address absolute positions, so a
// scrolled row must keep its own index rather than its position in the window.
func TestTableKeepsAbsoluteIndexesAfterScrolling(t *testing.T) {
	sessions := scrollFixture(10)
	out := RenderTable(sessions, 5, nil, 0, 120, 3, "")

	var cursorLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "sess-f") {
			cursorLine = stripANSIForTest(l)
		}
	}
	if cursorLine == "" {
		t.Fatalf("cursor row sess-f was not drawn:\n%s", out)
	}
	// Window is sessions 3,4,5. sess-f is absolute index 5 and would be index 2
	// if the window's own positions were passed through instead. The fixture's
	// names carry no digits, so the index column is the only digit on the line.
	if !strings.Contains(cursorLine, "5") {
		t.Errorf("cursor row should carry absolute index 5: %q", cursorLine)
	}
	if strings.Contains(cursorLine, "2") {
		t.Errorf("cursor row carries its window position 2, not its absolute index: %q", cursorLine)
	}
}

// Asserts the content of the last window as well as the line count: the padding
// loop fills the height whether or not the window moved, so a line count alone
// passes with no viewport at all.
func TestTableStillFillsItsHeightWhenScrolled(t *testing.T) {
	sessions := scrollFixture(10)
	out := RenderTable(sessions, 9, nil, 0, 120, 4, "")

	if n := len(strings.Split(out, "\n")); n != 4 {
		t.Errorf("got %d lines, want exactly the 4 allocated:\n%s", n, out)
	}
	for _, want := range []string{"sess-g", "sess-h", "sess-i", "sess-j"} {
		if !strings.Contains(out, want) {
			t.Errorf("last window should contain %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sess-f") {
		t.Errorf("sess-f is above the last 4 row window:\n%s", out)
	}
}

func stripANSIForTest(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}

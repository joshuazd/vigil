package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestListSessions(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|alpha|/home/alpha\n1000|alpha|/home/alpha/pane2\n999|beta|/home/beta", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	// "1000" sorts before "999" lexicographically
	if sessions[0].Name != "alpha" {
		t.Errorf("got %q, want alpha", sessions[0].Name)
	}
	if sessions[1].Name != "beta" {
		t.Errorf("got %q, want beta", sessions[1].Name)
	}
}

func TestListSessionsDeduplicates(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|session1|/path1\n1000|session1|/path2", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d, want 1 (deduplicated)", len(sessions))
	}
}

func TestCurrentSession(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "my-session", nil)

	name := CurrentSession(context.Background(), mock)
	if name != "my-session" {
		t.Errorf("got %q", name)
	}
}

func TestLastSession(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "other-session", nil)

	last := LastSession(context.Background(), mock)
	if last != "other-session" {
		t.Errorf("got %q, want other-session", last)
	}
}

func TestAttachedSessions(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "session1|0\nsession2|1\nsession3|0", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached["session2"] {
		t.Error("expected session2 to be attached")
	}
	if attached["session1"] {
		t.Error("session1 should not be attached")
	}
	if attached["session3"] {
		t.Error("session3 should not be attached")
	}
}

func TestAttachedSessionsWithNoSessions(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatalf("empty output with no error is a legitimate \"no sessions\", got err: %v", err)
	}
	if len(attached) != 0 {
		t.Errorf("got %v, want an empty map", attached)
	}
}

func TestAttachedSessionsPropagatesTheError(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "", errors.New("tmux: no server running"))

	_, err := AttachedSessions(context.Background(), mock)
	if err == nil {
		t.Fatal("want the tmux error returned, not swallowed into an empty map")
	}
}

// TestAttachedSessionsCountsMultipleClients is the guard on session_attached
// being a count, not a boolean: `man tmux` defines it as "Number of clients
// session is attached to". Two panels on one session - this project's stated
// normal case - would report "2", and a `== "1"` comparison would read that
// as unattached and let cleanup destroy a session two people are looking at.
func TestAttachedSessionsCountsMultipleClients(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|2", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached["canary"] {
		t.Error("2 clients attached should count as attached")
	}
}

func TestAttachedSessionsCountsThreeClients(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|3", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached["canary"] {
		t.Error("3 clients attached should count as attached")
	}
}

// TestAttachedSessionsTreatsAMalformedValueAsAttached and its empty-value
// sibling pin the fail-closed direction for any value tmux does not actually
// produce: since "0" is the only reading that means "go ahead and destroy
// this", everything else - including garbage - must not.
func TestAttachedSessionsTreatsAMalformedValueAsAttached(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|yes", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached["canary"] {
		t.Error("a non-numeric value should fail closed as attached")
	}
}

func TestAttachedSessionsTreatsAnEmptyValueAsAttached(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached["canary"] {
		t.Error("an empty value should fail closed as attached")
	}
}

// TestAttachedSessionsHandlesAPipeInTheSessionName is why the split is on
// the LAST "|" rather than the first: tmux accepts "|" in a session name, and
// SplitN(line, "|", 2) would read "al|pha|1" as name "al", value "pha|1" -
// neither "0" nor matched at all, so either reading destroys or misparses it.
func TestAttachedSessionsHandlesAPipeInTheSessionName(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "al|pha|0", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := attached["al|pha"]
	if !ok {
		t.Fatalf("got %v, want the whole name \"al|pha\" as one key", attached)
	}
	if value {
		t.Error("al|pha reported 0 clients, should not be attached")
	}
	if _, splitOnFirstPipe := attached["al"]; splitOnFirstPipe {
		t.Error("split on the first \"|\" instead of the last: \"al\" should not be a key")
	}
}

// TestAttachedSessionsTrimsATrailingCR uses a second line after the one under
// test so the CR under test sits in the middle of the raw output, not at its
// very end - the outer strings.TrimSpace(out) already strips a lone trailing
// CR, which would make this pass even without the fix.
func TestAttachedSessionsTrimsATrailingCR(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|0\r\nzzz|1", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if attached["canary"] {
		t.Error("a trailing CR on \"0\" should still read as not attached")
	}
	if !attached["zzz"] {
		t.Error("zzz should still be attached")
	}
}

// TestAttachedSessionsPreservesALeadingSpaceInTheFirstName is why the trim
// happens per line rather than over the whole output: tmux accepts a session
// name starting with whitespace, and such a name sorts first, so it is
// exactly the first line that strings.TrimSpace(out) would corrupt.
func TestAttachedSessionsPreservesALeadingSpaceInTheFirstName(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", " alpha|1\nbeta|0", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if !attached[" alpha"] {
		t.Errorf("got %v, want \" alpha\" (leading space intact) attached", attached)
	}
	if _, strippedKey := attached["alpha"]; strippedKey {
		t.Error("the leading space was stripped from the session name")
	}
}

// TestAttachedSessionsTrimsATrailingSpaceOnTheValue is distinct from the CR
// test: TrimRight(line, "\r") only ever strips a carriage return, so it does
// not exercise the separate strings.TrimSpace on the value. The value here is
// "0 " (zero, not one): without the trim, "0 " != "0" reads as attached
// regardless of whitespace, so only a zero count actually distinguishes
// "trimmed" from "not trimmed" in the != "0" comparison.
func TestAttachedSessionsTrimsATrailingSpaceOnTheValue(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "canary|0 ", nil)

	attached, err := AttachedSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if attached["canary"] {
		t.Error("a trailing space on \"0\" should still read as not attached")
	}
}

func TestBellFlags(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "session1|0\nsession2|1\nsession3|0", nil)

	bells := BellFlags(context.Background(), mock)
	if !bells["session2"] {
		t.Error("expected session2 to have bell")
	}
	if bells["session1"] {
		t.Error("session1 should not have bell")
	}
}

func TestMostRecentClientPicksTheHighestActivity(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "1200|/dev/ttys002\n1900|/dev/ttys009\n1500|/dev/ttys004\n", nil)
	if got := MostRecentClient(context.Background(), m); got != "/dev/ttys009" {
		t.Errorf("got %q, want /dev/ttys009", got)
	}
}

func TestMostRecentClientIsEmptyWithNoClients(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "", nil)
	if got := MostRecentClient(context.Background(), m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMostRecentClientIsEmptyWhenTmuxFails(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "", errors.New("no server running"))
	if got := MostRecentClient(context.Background(), m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// Pipe-separated, not whitespace-separated. The phase 3 handoff records a
// verification run misled by awk splitting on a session name's spaces; a client
// name is a tty today but the format string must not be the thing that breaks
// if that ever stops being true.
func TestMostRecentClientUsesPipeSeparatedFormat(t *testing.T) {
	m := NewMockCommander()
	m.On("tmux", "1|/dev/ttys002\n", nil)
	MostRecentClient(context.Background(), m)
	if len(m.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(m.Calls))
	}
	joined := strings.Join(m.Calls[0].Args, " ")
	if !strings.Contains(joined, "|") {
		t.Errorf("format has no pipe separator: %q", joined)
	}
}

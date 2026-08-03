package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

const listPanesKey = "tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}"

// droppedServer builds a Server whose session list is whatever lines() returns
// at the moment of the call, so one test can poll twice with different sets.
func droppedServer(t *testing.T, buf *bytes.Buffer, lines func() string) *Server {
	t.Helper()
	return droppedServerErr(t, buf, func() (string, error) { return lines(), nil })
}

// droppedServerErr is droppedServer with a session list that can fail, so a test
// can drive poll's err != nil return with a seeded prevSessions.
func droppedServerErr(t *testing.T, buf *bytes.Buffer, lines func() (string, error)) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs[listPanesKey] = func(ctx context.Context, dir string, args []string) (string, error) {
		return lines()
	}
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.On("git", "", nil)

	return &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Log:       log.New(buf, "", 0),
	}
}

func TestPollLogsADroppedSession(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha"
	s.poll(context.Background())

	got := buf.String()
	if !strings.Contains(got, "session dropped: beta") {
		t.Errorf("got log %q, want it to name the dropped session beta", got)
	}
	if strings.Contains(got, "alpha") {
		t.Errorf("got log %q, want nothing about the surviving session alpha", got)
	}
}

// The test that would pass with the feature deleted, which is why it is paired
// with the positive case above in the same run rather than standing alone.
func TestPollWithAnUnchangedSessionSetLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()
	s.poll(context.Background())

	if buf.String() != "" {
		t.Errorf("got log %q, want nothing for an unchanged session set", buf.String())
	}
}

func TestPollWithAGrowingSessionSetLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta"
	s.poll(context.Background())

	if buf.String() != "" {
		t.Errorf("got log %q, want nothing for a new session", buf.String())
	}
}

func TestPollLogsEverySessionDroppedInOnePoll(t *testing.T) {
	var buf bytes.Buffer
	set := "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta\n1700000002|$3|gamma|/tmp/gamma"
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = "1700000000|$1|alpha|/tmp/alpha"
	s.poll(context.Background())

	got := buf.String()
	if !strings.Contains(got, "session dropped: beta") {
		t.Errorf("got log %q, want beta", got)
	}
	if !strings.Contains(got, "session dropped: gamma") {
		t.Errorf("got log %q, want gamma", got)
	}
}

// logDroppedSessions is called after poll's err != nil return, and that
// placement is the whole difference between a useful diagnostic and a burst of
// false drops on every failed poll: a failing Snapshot returns no sessions, so
// the entire previous set reads as departed. Nothing but this test pins it -
// moving the call above the return leaves the rest of the package green.
func TestAFailingPollLogsNoDrops(t *testing.T) {
	var buf bytes.Buffer
	fail := false
	s := droppedServerErr(t, &buf, func() (string, error) {
		if fail {
			return "", errors.New("boom")
		}
		return "1700000000|$1|alpha|/tmp/alpha\n1700000001|$2|beta|/tmp/beta", nil
	})

	s.poll(context.Background())
	if len(s.prevSessions) != 2 {
		t.Fatalf("got %d seeded sessions, want 2 - the failure case is vacuous without them", len(s.prevSessions))
	}
	buf.Reset()

	fail = true
	s.poll(context.Background())

	got := buf.String()
	if strings.Contains(got, "session dropped") {
		t.Errorf("got log %q, want no drops from a failed poll", got)
	}
	if !strings.Contains(got, "poll failed") {
		t.Errorf("got log %q, want the poll to have actually failed", got)
	}
	if len(s.prevSessions) != 2 {
		t.Errorf("got %d sessions after the failure, want the last successful set of 2", len(s.prevSessions))
	}
}

// Map iteration order is random, so a diagnostic log read against timestamps
// needs the names sorted. Eight dropped names rather than two: an unsorted
// implementation passes an exact-order assertion by chance, and 1/8! is small
// enough that the assertion actually discriminates.
func TestDroppedSessionsAreLoggedInSortedOrder(t *testing.T) {
	var buf bytes.Buffer
	names := []string{"hotel", "golf", "foxtrot", "echo", "delta", "charlie", "bravo", "alpha"}
	var lines []string
	for i, name := range names {
		lines = append(lines, fmt.Sprintf("17000000%02d|$%d|%s|/tmp/%s", i, i+1, name, name))
	}
	set := strings.Join(lines, "\n")
	s := droppedServer(t, &buf, func() string { return set })

	s.poll(context.Background())
	buf.Reset()

	set = ""
	s.poll(context.Background())

	var want []string
	for _, name := range names {
		want = append(want, "session dropped: "+name)
	}
	sort.Strings(want)
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got log lines %q, want %q", got, want)
	}
}

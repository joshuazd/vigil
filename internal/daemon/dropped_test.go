package daemon

import (
	"bytes"
	"context"
	"log"
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
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs[listPanesKey] = func(ctx context.Context, dir string, args []string) (string, error) {
		return lines(), nil
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

// The first poll of a process has no previous set to compare against. It must
// seed rather than report every session as dropped - or worse, report nothing
// ever because the seed was skipped.
func TestTheFirstPollLogsNoDrops(t *testing.T) {
	var buf bytes.Buffer
	s := droppedServer(t, &buf, func() string {
		return "1700000000|$1|alpha|/tmp/alpha"
	})

	s.poll(context.Background())

	if strings.Contains(buf.String(), "session dropped") {
		t.Errorf("got log %q, want no drops on the first poll", buf.String())
	}
}

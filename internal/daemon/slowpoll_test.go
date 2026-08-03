package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

// slowPollServer builds a Server whose every poll spends at least delay inside
// fillGit on one pane path, and whose Interval is short enough that such a poll
// counts as slow. GitInterval is set below the delay so the git memo can never
// skip and every poll is slow, not just the first.
func slowPollServer(t *testing.T, delay time.Duration, buf *bytes.Buffer) *Server {
	t.Helper()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|fast|/tmp/fast\n1700000001|slow|/tmp/slow", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs["git rev-parse --show-toplevel"] = func(ctx context.Context, dir string, args []string) (string, error) {
		if dir == "/tmp/slow" {
			time.Sleep(delay)
		}
		return dir, nil
	}
	cmd.On("git", "", nil)

	c := collect.New(&config.Config{}, cmd)
	c.GitInterval = time.Nanosecond

	return &Server{
		Collector: c,
		Interval:  delay / 4,
		Log:       log.New(buf, "", 0),
	}
}

func TestPollLogsASlowPollWithItsBreakdown(t *testing.T) {
	var buf bytes.Buffer
	s := slowPollServer(t, 60*time.Millisecond, &buf)

	s.poll(context.Background())

	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, "slow poll") {
		t.Fatalf("got log %q, want a slow poll line", line)
	}
	if !strings.Contains(line, "/tmp/slow") {
		t.Errorf("got log %q, want it to name the slowest pane path", line)
	}
}

func TestPollUnderATickLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	s := slowPollServer(t, 60*time.Millisecond, &buf)
	s.Interval = time.Second

	s.poll(context.Background())

	if buf.String() != "" {
		t.Errorf("got log %q, want nothing for a poll inside one tick", buf.String())
	}
}

func TestASecondSlowPollInsideTheRateLimitWindowLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	s := slowPollServer(t, 60*time.Millisecond, &buf)
	clock := time.Unix(1700000000, 0)
	s.clock = func() time.Time { return clock }

	s.poll(context.Background())
	clock = clock.Add(slowPollLogInterval - time.Second)
	s.poll(context.Background())

	if n := strings.Count(buf.String(), "slow poll"); n != 1 {
		t.Errorf("got %d slow poll lines in %q, want 1", n, buf.String())
	}
}

// Without this, a rate limit that only ever logged once would still satisfy
// the test above.
func TestASlowPollLogsAgainOnceTheRateLimitWindowHasPassed(t *testing.T) {
	var buf bytes.Buffer
	s := slowPollServer(t, 60*time.Millisecond, &buf)
	clock := time.Unix(1700000000, 0)
	s.clock = func() time.Time { return clock }

	s.poll(context.Background())
	clock = clock.Add(slowPollLogInterval + time.Second)
	s.poll(context.Background())

	if n := strings.Count(buf.String(), "slow poll"); n != 2 {
		t.Errorf("got %d slow poll lines in %q, want 2", n, buf.String())
	}
}

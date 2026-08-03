package collect

import (
	"context"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

// slowGitCmd returns a commander whose git calls for slowPath take at least
// delay, and whose calls for every other path return immediately. Real sleeps
// rather than a fake clock: the thing under test is wall time, so there is
// nothing to fake.
func slowGitCmd(slowPath string, delay time.Duration) *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|fast|/tmp/fast\n1700000001|slow|/tmp/slow", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)

	cmd.HandlerFuncs = make(map[string]func(ctx context.Context, dir string, args []string) (string, error))
	cmd.HandlerFuncs["git rev-parse --show-toplevel"] = func(ctx context.Context, dir string, args []string) (string, error) {
		if dir == slowPath {
			time.Sleep(delay)
		}
		return dir, nil
	}
	cmd.On("git", "", nil)
	return cmd
}

func TestFillGitReportsItsSlowestPanePath(t *testing.T) {
	const delay = 60 * time.Millisecond
	cmd := slowGitCmd("/tmp/slow", delay)

	c := New(&config.Config{}, cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	stats := c.GitStats()
	if stats.SlowestPath != "/tmp/slow" {
		t.Errorf("slowest path: got %q, want /tmp/slow", stats.SlowestPath)
	}
	if stats.Slowest < delay {
		t.Errorf("slowest: got %s, want at least %s", stats.Slowest, delay)
	}
	if stats.Total < delay {
		t.Errorf("total: got %s, want at least %s", stats.Total, delay)
	}
}

// A memoized poll issues no git subprocesses, so it must report zero rather
// than the previous poll's numbers. Same line covers a Snapshot that fails
// before fillGit runs at all.
func TestGitStatsAreZeroWhenEveryPathIsMemoized(t *testing.T) {
	cmd := slowGitCmd("/tmp/slow", 60*time.Millisecond)

	c := New(&config.Config{}, cmd)
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if c.GitStats().Total == 0 {
		t.Fatal("first Snapshot should have measured a fan-out")
	}
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}

	stats := c.GitStats()
	if stats != (GitStats{}) {
		t.Errorf("got %+v, want the zero value", stats)
	}
}

package model

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
)

func collectFixtureCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|1", nil)
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd
}

func TestCollectCmdEmitsALocalSnapshot(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	msg := m.collectCmd()()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg", msg)
	}
	if !snap.Local {
		t.Error("a self-collected snapshot must be marked Local")
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Name != "alpha" {
		t.Fatalf("got %+v, want one session named alpha", snap.Sessions)
	}
	if !snap.Sessions[0].HasBell {
		t.Error("collect should have carried the bell flag through")
	}
	if !snap.Sessions[0].IsCurrent {
		t.Error("collectCmd must annotate per-client flags, like the daemon path does")
	}
}

// TestCollectCmdEmitsASnapshotWhenTmuxFails is the reschedule hazard. The
// fallback poll self-schedules from its own result, so an outcome that produces
// no message stops polling permanently and silently.
func TestCollectCmdEmitsASnapshotWhenTmuxFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", context.DeadlineExceeded)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	msg := m.collectCmd()()

	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("got %T, want SnapshotMsg even on failure", msg)
	}
	if !snap.Local {
		t.Error("a failed local poll is still a local poll")
	}
	if snap.Sessions != nil {
		t.Errorf("got sessions %+v, want nil so handleSnapshot leaves state alone", snap.Sessions)
	}
}

// TestBothPathsProduceIdenticalSessions is the structural payoff of the
// collapse: "the daemon path and the self-polling path must render identically"
// stops being a convention held up by review and becomes one assertion. It has
// already drifted once.
func TestBothPathsProduceIdenticalSessions(t *testing.T) {
	fixture := func() *fetch.MockCommander {
		cmd := fetch.NewMockCommander()
		cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
			"1700000000|alpha|/tmp/alpha\n1700000001|beta|/tmp/beta", nil)
		cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|1\nbeta|0", nil)
		cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
		cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
			"git rev-parse --show-toplevel": func(_ context.Context, dir string, _ []string) (string, error) {
				return "/repo" + dir, nil
			},
			"git branch --show-current": func(_ context.Context, dir string, _ []string) (string, error) {
				if dir == "/repo/tmp/alpha" {
					return "feature/a", nil
				}
				return "feature/b", nil
			},
		}
		cmd.On("git", "", nil)
		cmd.On("gh", "", nil)
		return cmd
	}

	// The daemon path: collect on the server side, then annotate client-side,
	// which is exactly what daemon.poll plus listenDaemonCmd do.
	serverCmd := fixture()
	served, err := collect.New(&config.Config{}, serverCmd).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("server-side Snapshot: %v", err)
	}
	annotateClientFlags(context.Background(), serverCmd, served, "")

	// The self-polling path.
	localCmd := fixture()
	lm := newTestModel()
	lm.cmd = localCmd
	lm.collector = collect.New(&config.Config{}, localCmd)
	msg, ok := lm.collectCmd()().(SnapshotMsg)
	if !ok {
		t.Fatal("collectCmd did not produce a SnapshotMsg")
	}

	if len(msg.Sessions) != len(served) {
		t.Fatalf("got %d local sessions, want %d from the daemon path", len(msg.Sessions), len(served))
	}
	for i := range served {
		got, want := *msg.Sessions[i], *served[i]
		if got != want {
			t.Errorf("session %d differs between paths:\n local: %+v\ndaemon: %+v", i, got, want)
		}
	}
}

// collectedAgain walks a command tree, invoking each command, and reports
// whether any produced a local SnapshotMsg. That is the fallback loop
// rescheduling itself, which nothing else drives: there is no ticker.
func collectedAgain(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case SnapshotMsg:
		return msg.Local
	case tea.BatchMsg:
		for _, c := range msg {
			if collectedAgain(c) {
				return true
			}
		}
	}
	return false
}

func TestLocalSnapshotSchedulesTheNextPoll(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	_, next := m.handleSnapshot(SnapshotMsg{Sessions: fixtureSessions(), Local: true})

	if !collectedAgain(next) {
		t.Fatal("a local snapshot scheduled no further poll, so the fallback loop is dead")
	}
}

// TestAFailedLocalPollStillSchedulesTheNextOne is the same property on the
// branch that actually threatens it. A poll that errored carries no sessions,
// and if that branch forgets to reschedule the client goes quiet for the life
// of the process with no indication.
func TestAFailedLocalPollStillSchedulesTheNextOne(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.sessions = fixtureSessions()

	updated, next := m.handleSnapshot(SnapshotMsg{Local: true})

	if !collectedAgain(next) {
		t.Fatal("a failed local poll scheduled no further poll")
	}
	if got := updated.(Model).sessions; len(got) != 1 {
		t.Errorf("a failed poll blanked the session list: %+v", got)
	}
}

package model

import (
	"context"
	"testing"
	"time"

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

	msg := m.collectCmd(false)()

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

	msg := m.collectCmd(false)()

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
	msg, ok := lm.collectCmd(false)().(SnapshotMsg)
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
// whether any produced a CollectTickMsg for the given epoch. That message is
// what paces the fallback loop into rescheduling itself: nothing else drives
// it, and there is no free-running ticker.
func collectedAgain(cmd tea.Cmd, epoch int) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case CollectTickMsg:
		return msg.Epoch == epoch
	case tea.BatchMsg:
		for _, c := range msg {
			if collectedAgain(c, epoch) {
				return true
			}
		}
	}
	return false
}

// setPollInterval shortens the self-poll pace so a test can invoke the
// scheduled CollectTickMsg's command without waiting out a real interval.
// tmux_interval's built-in default ("1", i.e. 1s) always beats
// defaultPollInterval's zero-value fallback, so this also forces the
// setting itself to 0 to route pollInterval through the shortened var.
func setPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	t.Setenv("VIGIL_TMUX_INTERVAL", "0")
	orig := defaultPollInterval
	defaultPollInterval = d
	t.Cleanup(func() { defaultPollInterval = orig })
}

// TestLocalSnapshotSchedulesTheNextPoll is the pacing regression pin: a local
// snapshot must reschedule via a paced CollectTickMsg, not by appending
// another collectCmd directly. An immediate reschedule would run the self-poll
// loop as fast as tmux answers - tens of subprocess calls a second, forever -
// instead of once per pollInterval.
func TestLocalSnapshotSchedulesTheNextPoll(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	_, next := m.handleSnapshot(SnapshotMsg{Sessions: fixtureSessions(), Local: true, Epoch: m.epoch})

	if !collectedAgain(next, m.epoch) {
		t.Fatal("a local snapshot scheduled no further poll, so the fallback loop is dead")
	}
}

// TestAFailedLocalPollStillSchedulesTheNextOne is the same property on the
// branch that actually threatens it. A poll that errored carries no sessions,
// and if that branch forgets to reschedule the client goes quiet for the life
// of the process with no indication.
func TestAFailedLocalPollStillSchedulesTheNextOne(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.sessions = fixtureSessions()

	updated, next := m.handleSnapshot(SnapshotMsg{Local: true, Epoch: m.epoch})

	if !collectedAgain(next, m.epoch) {
		t.Fatal("a failed local poll scheduled no further poll")
	}
	if got := updated.(Model).sessions; len(got) != 1 {
		t.Errorf("a failed poll blanked the session list: %+v", got)
	}
}

// TestLocalSnapshotClearsPollInFlight is the other half of the failed-poll
// test above: handleSnapshot's Local branch must clear pollInFlight
// regardless of outcome, or a client that hits one failed poll would refuse
// every startPoll call (a forced refresh, a future fallback) for the rest of
// the process.
func TestLocalSnapshotClearsPollInFlight(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.pollInFlight = true

	updated, _ := m.handleSnapshot(SnapshotMsg{Local: true, Epoch: m.epoch})
	if updated.(Model).pollInFlight {
		t.Error("handleSnapshot's Local branch should clear pollInFlight")
	}
}

// TestStartPollRefusesASecondPollInFlight pins the single-flight guard that
// makes a forced refresh safe to offer alongside the ambient self-poll loop:
// with a poll already in flight, startPoll must return nil rather than issue
// a second collectCmd, which would call Collector.Snapshot concurrently with
// the one already running.
func TestStartPollRefusesASecondPollInFlight(t *testing.T) {
	m := newTestModel()
	m.pollInFlight = true

	if cmd := m.startPoll(false); cmd != nil {
		t.Error("startPoll issued a second poll while one was already in flight")
	}
}

// TestStartPollMutatesTheReturnedModel guards the pointer-receiver subtlety:
// startPoll's pollInFlight = true must land on the same Model that Update
// returns, or the single-flight guard above is a no-op in production even
// though it passes in isolation. Driving it through two Update calls, the way
// the real runtime would deliver two CollectTickMsgs back to back, is what
// would catch a regression where the mutation was made on a copy instead.
func TestStartPollMutatesTheReturnedModel(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	next, cmd1 := m.Update(CollectTickMsg{Epoch: m.epoch})
	if cmd1 == nil {
		t.Fatal("the first CollectTickMsg issued no poll")
	}
	if !next.(Model).pollInFlight {
		t.Fatal("startPoll's mutation did not survive on the model Update returned")
	}

	_, cmd2 := next.Update(CollectTickMsg{Epoch: m.epoch})
	if cmd2 != nil {
		t.Error("a second CollectTickMsg issued a poll while one was already in flight")
	}
}

// TestRefreshKeyForcesAPollWhenSelfPolling restores the 'r' keybinding's
// feature: with no daemon, it must issue a forced (memo-invalidating) poll
// through the same single-flight path as the ambient loop.
func TestRefreshKeyForcesAPollWhenSelfPolling(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	next, got := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if got == nil {
		t.Fatal("Refresh issued no command while self-polling")
	}
	if !next.(Model).pollInFlight {
		t.Error("Refresh should have marked a poll in flight")
	}
	msg, ok := got().(SnapshotMsg)
	if !ok || !msg.Local {
		t.Fatalf("got %T, want a local SnapshotMsg from the forced poll", msg)
	}
}

// TestRefreshKeyDoesNothingWhenDaemonConnected pins the owner's ruling: a
// daemon-fed client has no memos of its own to invalidate and the daemon
// already polls at tmux_interval, so forcing a redundant local Snapshot
// would just spend this client's own subprocess budget for nothing.
func TestRefreshKeyDoesNothingWhenDaemonConnected(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}

	_, got := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if got != nil {
		t.Error("Refresh forced a poll while a daemon was connected")
	}
}

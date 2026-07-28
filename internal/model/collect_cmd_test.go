package model

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
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

	// This comparison is only a meaningful pin if the fixture actually
	// produced real data on both sides: an empty served and an empty
	// msg.Sessions compare equal (both length 0, the for loop below never
	// iterates) and would pass just as happily as two correct, identical
	// non-empty slices. Assert the fixture did its job before trusting the
	// comparison that follows.
	if len(served) < 2 {
		t.Fatalf("fixture produced %d server-side sessions, want at least 2", len(served))
	}
	for _, s := range served {
		if s.Git.Branch == "" {
			t.Fatalf("fixture session %q has no branch; the comparison below would be vacuous", s.Name)
		}
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

func countCalls(cmd *fetch.MockCommander, name string) int {
	n := 0
	for _, c := range cmd.Calls {
		if c.Name == name {
			n++
		}
	}
	return n
}

// TestStartPollForceReachesInvalidateEndToEnd pins the wiring, not just the
// primitive: internal/collect's own TestInvalidateForcesARefetchOfGitAndPR
// proves Collector.Invalidate works, but nothing before this test exercised
// startPoll(true) -> collectCmd(force) -> Invalidate as one path. Dropping
// the Invalidate call from collectCmd's force branch left every other test
// in this package green.
func TestStartPollForceReachesInvalidateEndToEnd(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}",
		"1700000000|alpha|/tmp/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.On("gh", `{"number": 1, "state": "OPEN"}`, nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	unforced := m.startPoll(false)
	if unforced == nil {
		t.Fatal("the first poll was refused")
	}
	if _, ok := unforced().(SnapshotMsg); !ok {
		t.Fatal("the first poll did not produce a SnapshotMsg")
	}
	if got := countCalls(cmd, "gh"); got != 1 {
		t.Fatalf("got %d gh calls after the first poll, want 1", got)
	}

	m.pollInFlight = false // stand in for that first poll's SnapshotMsg having landed
	forced := m.startPoll(true)
	if forced == nil {
		t.Fatal("the forced poll was refused")
	}
	if _, ok := forced().(SnapshotMsg); !ok {
		t.Fatal("the forced poll did not produce a SnapshotMsg")
	}
	if got := countCalls(cmd, "gh"); got != 2 {
		t.Errorf("got %d gh calls after a forced poll, want 2 (force should have invalidated the PR memo)", got)
	}
}

// TestActionResultDoesNothingWhenDaemonConnected pins the guard nothing else
// in this package failed without: a daemon-fed client has nothing of its own
// to force, and dropping this guard would have this client running its own
// redundant Snapshot alongside the daemon's shared one for no reason.
func TestActionResultDoesNothingWhenDaemonConnected(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}

	_, got := m.Update(ActionResultMsg{Action: "merge", OK: true, Message: "done"})
	if got != nil {
		t.Error("ActionResultMsg forced a poll while a daemon was connected")
	}
}

// TestBatchResultDoesNothingWhenDaemonConnected is BatchResultMsg's half of
// the same guard, for the same reason.
func TestBatchResultDoesNothingWhenDaemonConnected(t *testing.T) {
	m := newTestModel()
	m.daemonConn = &fakeConn{}

	_, got := m.Update(BatchResultMsg{Action: "merge", OK: 1})
	if got != nil {
		t.Error("BatchResultMsg forced a poll while a daemon was connected")
	}
}

// runAll invokes cmd, and every command it unwraps to if it is a batch, each
// in its own goroutine - the same concurrency Bubble Tea's real runtime gives
// a returned tea.Cmd - and sends every resulting message to the returned
// channel before closing it. This is what lets two wrongly-issued polls
// actually run concurrently against the same Collector in a test, which is
// what -race needs to see to catch the reconnect race below.
func runAll(cmd tea.Cmd) <-chan tea.Msg {
	out := make(chan tea.Msg, 4)
	if cmd == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		msg := cmd()
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			out <- msg
			return
		}
		var wg sync.WaitGroup
		for _, c := range batch {
			if c == nil {
				continue
			}
			wg.Add(1)
			go func(c tea.Cmd) {
				defer wg.Done()
				out <- c()
			}(c)
		}
		wg.Wait()
	}()
	return out
}

// TestReconnectRaceDoesNotDoublePollOrWedge drives the exact sequence an
// adversarial review reproduced under -race: a poll (P1) issued for epoch E
// is still running when a reconnect bumps to E+1, and that daemon dies
// immediately, bumping to E+2. Resetting pollInFlight at either epoch bump
// (a mistake this task made once already) let handleDaemonLost see the flag
// false and start a second, concurrent Collector.Snapshot call (P2) against
// the same *Collector as P1 - a live data race on gitMemo/prMemo, caught by
// this exact sequence under -race during that mistake's own review.
//
// pollInFlight tracks a running goroutine, not a generation: an epoch bump
// must never touch it, and only the goroutine that set it may clear it, on
// landing, regardless of which epoch it belonged to.
func TestReconnectRaceDoesNotDoublePollOrWedge(t *testing.T) {
	setProbeInterval(t, time.Millisecond)

	release := make(chan struct{})
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}": func(context.Context, string, []string) (string, error) {
			<-release
			return "1700000000|alpha|/tmp/alpha", nil
		},
	}
	cmd.On("tmux", "", nil)
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.epoch = 5

	// P1: a CollectTickMsg for the current epoch issues the first poll, which
	// immediately blocks inside Snapshot at the gated tmux call.
	next, pollCmd := m.Update(CollectTickMsg{Epoch: 5})
	m = next.(Model)
	if pollCmd == nil || !m.pollInFlight {
		t.Fatal("CollectTickMsg should have issued P1 and marked a poll in flight")
	}
	p1Done := make(chan tea.Msg, 1)
	go func() { p1Done <- pollCmd() }()

	// The daemon reconnects while P1 is still running, bumping to E+1.
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	next, _ = m.Update(DaemonProbeResultMsg{Epoch: 5, Conn: client, Decoder: protocol.NewDecoder(client)})
	m = next.(Model)
	if m.epoch != 6 || !m.pollInFlight {
		t.Fatalf("got epoch=%d pollInFlight=%v after reconnect, want epoch=6 pollInFlight=true (P1 is still running)",
			m.epoch, m.pollInFlight)
	}

	// That daemon dies immediately, bumping to E+2.
	next, lostCmd := m.Update(DaemonLostMsg{Epoch: 6})
	m = next.(Model)
	if m.epoch != 7 {
		t.Fatalf("got epoch %d, want 7 after the second loss", m.epoch)
	}

	// Run whatever handleDaemonLost scheduled concurrently, the same way
	// Bubble Tea's real runtime would - including a wrongly-issued P2, if the
	// bug were present, blocking on the very same gate as P1. Only then
	// release the gate, so if P2 exists it races P1 through fillGit/fillPRs
	// on the same *Collector for -race to catch.
	lostResults := runAll(lostCmd)
	close(release)

	p1Msg := <-p1Done
	snap, ok := p1Msg.(SnapshotMsg)
	if !ok || !snap.Local || snap.Epoch != 5 {
		t.Fatalf("got %+v, want a local SnapshotMsg for epoch 5 (P1)", p1Msg)
	}

	deadline := time.After(2 * time.Second)
	draining := true
	for draining {
		select {
		case msg, ok := <-lostResults:
			if !ok {
				draining = false
				continue
			}
			if _, isSnapshot := msg.(SnapshotMsg); isSnapshot {
				t.Fatal("handleDaemonLost started a second poll (P2) while P1 was still running")
			}
		case <-deadline:
			t.Fatal("timed out draining handleDaemonLost's commands")
		}
	}

	// The straggler landing must clear pollInFlight and restart the loop for
	// the current epoch - or the fallback is wedged for the life of the
	// process, since nothing else will ever poll again. pollInFlight ends up
	// true again here, not false: handleSnapshot clears it first (the
	// straggler's own goroutine finished), then startPoll immediately sets
	// it right back to true for the replacement poll it just issued.
	next, restartCmd := m.Update(p1Msg)
	m = next.(Model)
	if !m.pollInFlight {
		t.Error("restarting the loop should mark the replacement poll in flight")
	}
	if restartCmd == nil {
		t.Fatal("the straggler landing scheduled nothing: the fallback is wedged")
	}
	restartMsg := restartCmd()
	restartSnap, ok := restartMsg.(SnapshotMsg)
	if !ok || restartSnap.Epoch != 7 {
		t.Fatalf("got %+v, want a fresh poll for the current epoch (7)", restartMsg)
	}
}

// countCollectTicks walks a command tree, invoking each command, and counts
// how many produced a CollectTickMsg. tea.Tick makes asserting on wall-clock
// behavior awkward, so tests assert on the shape of what Update returned
// instead: how many links a poll's completion added to the self-poll chain.
func countCollectTicks(cmd tea.Cmd) int {
	if cmd == nil {
		return 0
	}
	switch msg := cmd().(type) {
	case CollectTickMsg:
		return 1
	case tea.BatchMsg:
		n := 0
		for _, c := range msg {
			n += countCollectTicks(c)
		}
		return n
	}
	return 0
}

// TestForcedPollDoesNotForkTheChain is the regression pin for the fork this
// round of review found: handleSnapshot's Local branch used to schedule a
// CollectTickMsg on every completed poll, ambient or forced. A forced poll
// completing while an ambient tick is already pending would then add a
// second, independent chain link on top of it - the ambient tick still fires
// on its own schedule, so the two chains run forever afterward, each
// producing its own tick from then on, doubling the effective poll rate for
// the rest of the process's life.
func TestForcedPollDoesNotForkTheChain(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	next, forcedCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if forcedCmd == nil {
		t.Fatal("Refresh issued no command while self-polling")
	}
	m = next.(Model)

	forcedMsg := forcedCmd()
	snap, ok := forcedMsg.(SnapshotMsg)
	if !ok || !snap.Forced {
		t.Fatalf("got %+v, want a forced SnapshotMsg", forcedMsg)
	}

	_, after := m.Update(forcedMsg)
	if got := countCollectTicks(after); got != 0 {
		t.Errorf("a forced poll's completion scheduled %d ticks, want 0: it must not fork the chain "+
			"the pending ambient tick already continues", got)
	}
}

// TestRepeatedForcedPollsDoNotAccumulateChains is the same property under
// repetition: each forced poll's completion clears pollInFlight (the same as
// an ambient one), so nothing stops a second, third, or Nth Refresh press
// from each starting its own poll - none of them may add a chain link either.
func TestRepeatedForcedPollsDoNotAccumulateChains(t *testing.T) {
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	for i := 1; i <= 3; i++ {
		next, forcedCmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		if forcedCmd == nil {
			t.Fatalf("press %d: Refresh issued no command", i)
		}
		m = next.(Model)

		forcedMsg := forcedCmd()
		next, after := m.Update(forcedMsg)
		m = next.(Model)
		if got := countCollectTicks(after); got != 0 {
			t.Errorf("press %d: forced poll's completion scheduled %d ticks, want 0", i, got)
		}
	}
}

// TestFailedForcedPollDoesNotForkTheChain covers the branch that actually
// threatens this fix: collectCmd's error path builds its SnapshotMsg
// separately from the success path, so it is the one place Forced could be
// dropped without any other test noticing (a failed poll still carries no
// sessions either way).
func TestFailedForcedPollDoesNotForkTheChain(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("tmux", "", context.DeadlineExceeded)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	forcedCmd := m.startPoll(true)
	if forcedCmd == nil {
		t.Fatal("startPoll refused the forced poll")
	}
	msg := forcedCmd()
	snap, ok := msg.(SnapshotMsg)
	if !ok || !snap.Forced || snap.Sessions != nil {
		t.Fatalf("got %+v, want a forced SnapshotMsg with no sessions (a failed poll)", msg)
	}

	_, after := m.Update(msg)
	if got := countCollectTicks(after); got != 0 {
		t.Errorf("a failed forced poll's completion scheduled %d ticks, want 0", got)
	}
}

package model

import (
	"context"
	"net"
	"os"
	"path/filepath"
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
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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
		cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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

// TestAmbientPollCompletionSchedulesNoTick pins where chain continuation
// lives. It used to live here, in poll completion, which meant a tick could be
// consumed without ever producing a completion - a tick that landed while a
// poll was already in flight was refused by startPoll and, tea.Tick being
// one-shot, simply vanished. Continuation belongs to the tick handler, the one
// place that sees every consumption, so a completed poll must add nothing.
func TestAmbientPollCompletionSchedulesNoTick(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	_, next := m.handleSnapshot(SnapshotMsg{Sessions: fixtureSessions(), Local: true, Epoch: m.epoch})

	if got := countCollectTicks(next); got != 0 {
		t.Errorf("a completed poll scheduled %d ticks, want 0: the tick handler owns the chain", got)
	}
}

// TestAFailedPollSchedulesNoTickAndKeepsTheSessions covers the other outcome
// of the same branch. A poll that errored carries no sessions, and applying it
// verbatim would blank the table; it must not schedule a tick either, for the
// same reason an ambient one must not.
func TestAFailedPollSchedulesNoTickAndKeepsTheSessions(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.sessions = fixtureSessions()

	updated, next := m.handleSnapshot(SnapshotMsg{Local: true, Epoch: m.epoch})

	if got := countCollectTicks(next); got != 0 {
		t.Errorf("a failed poll scheduled %d ticks, want 0", got)
	}
	if got := updated.(Model).sessions; len(got) != 1 {
		t.Errorf("a failed poll blanked the session list: %+v", got)
	}
}

// TestATickAlwaysReschedules is the core of the fix: the tick handler's
// reschedule must not depend on startPoll succeeding. With a poll already in
// flight startPoll returns nil, and if the reschedule were gated on that the
// consumed tick would have no successor - no tick pending, no poll in flight -
// and the self-poll loop would be dead for the rest of the generation.
func TestATickAlwaysReschedules(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	m := newTestModel()
	m.pollInFlight = true

	_, next := m.Update(CollectTickMsg{Epoch: m.epoch})

	if got := countCollectTicks(next); got != 1 {
		t.Fatalf("a tick consumed while a poll was in flight produced %d ticks, want exactly 1", got)
	}
}

// TestTheRescheduledTickCarriesTheCurrentEpoch is the other half of the
// reschedule: a link stamped with anything but the current epoch is dropped by
// the epoch guard the moment it fires, which is a wedge with extra steps.
func TestTheRescheduledTickCarriesTheCurrentEpoch(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	m := newTestModel()
	m.epoch = 7
	m.pollInFlight = true // so the batch is the tick alone

	_, next := m.Update(CollectTickMsg{Epoch: 7})

	if !collectedAgain(next, 7) {
		t.Error("the rescheduled tick was not stamped with the current epoch")
	}
}

// TestTickConsumedByAnInFlightForcedPollKeepsTheChainAlive drives the exact
// sequence a reviewer wedged the loop with: a forced poll (the r key) is in
// flight, the ambient tick fires and is refused, then the forced poll lands.
// A forced poll invalidates the memos and so runs slower than an ambient one,
// which makes this the likely case rather than a corner one. Every step is a
// real Update call, and the loop has to still be running afterwards.
func TestTickConsumedByAnInFlightForcedPollKeepsTheChainAlive(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	next, forced := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = next.(Model)
	if forced == nil || !m.pollInFlight {
		t.Fatal("Refresh did not put a forced poll in flight")
	}

	// The ambient tick fires while that forced poll is still running.
	next, afterTick := m.Update(CollectTickMsg{Epoch: m.epoch})
	m = next.(Model)
	if got := countCollectTicks(afterTick); got != 1 {
		t.Fatalf("the refused tick left %d successors, want 1: the chain is dead", got)
	}

	// The forced poll lands. It contributes nothing, which is correct - the
	// tick above already carried the chain forward.
	next, afterForced := m.Update(forced())
	m = next.(Model)
	if got := countCollectTicks(afterForced); got != 0 {
		t.Fatalf("the forced poll's completion scheduled %d ticks, want 0", got)
	}
	if m.pollInFlight {
		t.Fatal("the forced poll's completion did not clear pollInFlight")
	}

	// The chain's next link, now that nothing is in flight, must both poll and
	// continue the chain.
	next, afterNext := m.Update(CollectTickMsg{Epoch: m.epoch})
	if !next.(Model).pollInFlight {
		t.Error("the next tick issued no poll, so the chain is alive but idle")
	}
	if got := countCollectTicks(afterNext); got != 1 {
		t.Errorf("the next tick produced %d successors, want 1", got)
	}
}

// TestRepeatedForcedPollsLeaveExactlyOneChain: forced polls ride alongside the
// chain and never extend it, so no number of r presses can accumulate chains.
// Two chains never recover on their own - each link mints its own successor -
// so the poll rate would double, and double again, for the life of the process.
func TestRepeatedForcedPollsLeaveExactlyOneChain(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	ticks := 0
	for i := 1; i <= 3; i++ {
		next, forced := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		m = next.(Model)
		if forced == nil {
			t.Fatalf("press %d: Refresh issued no command", i)
		}
		next, after := m.Update(forced())
		m = next.(Model)
		ticks += countCollectTicks(after)
	}
	if ticks != 0 {
		t.Errorf("three forced polls added %d chain links, want 0", ticks)
	}

	// And the one chain that does exist still produces exactly one successor.
	_, afterTick := m.Update(CollectTickMsg{Epoch: m.epoch})
	if got := countCollectTicks(afterTick); got != 1 {
		t.Errorf("the chain produced %d successors after the forced polls, want 1", got)
	}
}

// TestTickEndsTheChainWhenADaemonConnects: a daemon-fed client has no chain of
// its own. Rescheduling anyway would leave a tick chain running for a client
// that can never poll (startPoll refuses while daemonConn is set) - and, if the
// daemon were later installed without the epoch bump that retires this chain
// today, a second chain once the daemon died.
func TestTickEndsTheChainWhenADaemonConnects(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	m := newTestModel()
	m.daemonConn = &fakeConn{}

	if _, cmd := m.Update(CollectTickMsg{Epoch: m.epoch}); cmd != nil {
		t.Error("a tick kept the chain alive after a daemon took over")
	}
}

// TestInitStartsExactlyOneChain and TestDaemonLostStartsExactlyOneChain pin the
// only two places a chain is ever created. Neither is covered by the tick tests
// above - those assume a chain already exists - and without one of them a
// self-polling generation would never tick at all.
func TestInitStartsExactlyOneChain(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	setProbeInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	if got := countCollectTicks(m.Init()); got != 1 {
		t.Errorf("Init started %d chains, want exactly 1", got)
	}
}

func TestDaemonLostStartsExactlyOneChain(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	setProbeInterval(t, time.Millisecond)
	cmd := collectFixtureCommander()
	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	m.daemonConn = client
	m.daemonDecoder = protocol.NewDecoder(client)

	_, lost := m.Update(DaemonLostMsg{Epoch: m.epoch})
	if got := countCollectTicks(lost); got != 1 {
		t.Errorf("handleDaemonLost started %d chains, want exactly 1", got)
	}
}

// TestInitIssuesTheOnePollThatClearsItsOwnPriming pins a three-part contract
// that spans two functions and that every chain-shaped assertion in this
// package is blind to:
//
//  1. newModel primes pollInFlight if and only if m.daemonDecoder == nil.
//  2. Init bypasses startPoll if and only if the same condition holds.
//  3. Init must therefore always issue exactly one poll on that branch.
//
// Break leg 3 - delete Init's collectCmd, or route it through startPoll, which
// the priming makes refuse - and nothing ever clears the primed flag, because
// handleSnapshot's Local branch is the only clearer and no SnapshotMsg ever
// arrives. Every subsequent startPoll is refused for the life of the process:
// the TUI paints the startup cache forever, with no error and no notification.
//
// So this asserts the observable consequence, not the flag. Asserting only
// pollInFlight == true after newModel would pass with Init's poll deleted.
func TestInitIssuesTheOnePollThatClearsItsOwnPriming(t *testing.T) {
	setPollInterval(t, time.Millisecond)
	setProbeInterval(t, time.Millisecond)

	fallbackDir := shortTempDir(t)
	// No vigil/ subdirectory, so there is no socket to dial and New takes the
	// fallback branch. HOME keeps the cache load (and applySnapshot's write)
	// off the developer's real one.
	t.Setenv("HOME", fallbackDir)
	t.Setenv("XDG_RUNTIME_DIR", fallbackDir)

	cmd := collectFixtureCommander()
	m := New(&config.Config{}, cmd)
	if m.daemonDecoder != nil {
		t.Fatal("New reached a daemon; this test needs the fallback branch")
	}
	if !m.pollInFlight {
		t.Fatal("leg 1: newModel did not prime pollInFlight for the fallback branch")
	}

	// Each Init() call builds its own command tree, which is why this calls it
	// three times rather than reusing one: tea.Tick creates its timer when the
	// command is built and its closure drains that timer once, so invoking the
	// same tree twice blocks forever.
	if got := pollsIssued(m.Init()); got != 1 {
		t.Fatalf("leg 3: Init issued %d polls, want exactly 1 - nothing else can clear the priming", got)
	}
	if got := countCollectTicks(m.Init()); got != 1 {
		t.Fatalf("Init started %d chains, want exactly 1", got)
	}

	// The consequence that matters: that poll's SnapshotMsg is reachable, it
	// clears the priming, and only then can anything poll again.
	snap := awaitSnapshot(t, runAll(m.Init()))
	next, _ := m.Update(snap)
	m = next.(Model)
	if m.pollInFlight {
		t.Fatal("Init's poll landed but did not clear the primed pollInFlight")
	}
	_, afterTick := m.Update(CollectTickMsg{Epoch: m.epoch})
	if got := pollsIssued(afterTick); got != 1 {
		t.Errorf("the chain issued %d polls after startup, want 1: the priming was never released", got)
	}

	// Leg 1's other direction. A daemon-fed model must not be primed: Init
	// issues no collectCmd for it, so the flag would stay true forever and the
	// fallback would be wedged from birth the moment that daemon died.
	daemonDir := shortTempDir(t)
	sockDir := filepath.Join(daemonDir, "vigil")
	if err := os.Mkdir(sockDir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(sockDir, "vigild.sock"))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	t.Setenv("HOME", daemonDir)
	t.Setenv("XDG_RUNTIME_DIR", daemonDir)

	dm := New(&config.Config{}, collectFixtureCommander())
	if dm.daemonDecoder == nil {
		t.Fatal("New did not reach the listener above; the daemon half of the contract is untested")
	}
	if dm.pollInFlight {
		t.Error("leg 1: a daemon-fed model was primed, so a later fallback starts wedged")
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
//
// The second tick still returns a command - its own chain link, unconditionally
// - so what this asserts is that the command carries no second poll.
func TestStartPollMutatesTheReturnedModel(t *testing.T) {
	setPollInterval(t, time.Millisecond)
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
	if got := pollsIssued(cmd2); got != 0 {
		t.Errorf("a second CollectTickMsg issued %d polls while one was already in flight, want 0", got)
	}
}

// pollsIssued walks a command tree, invoking each command, and counts how many
// produced a local SnapshotMsg - i.e. how many collectCmds it carried.
//
// Never call this and countCollectTicks on the same tea.Cmd: both invoke it,
// and a second invocation of a one-shot tea.Tick reads from an already-drained
// timer channel and blocks forever.
func pollsIssued(cmd tea.Cmd) int {
	if cmd == nil {
		return 0
	}
	switch msg := cmd().(type) {
	case SnapshotMsg:
		if msg.Local {
			return 1
		}
	case tea.BatchMsg:
		n := 0
		for _, c := range msg {
			n += pollsIssued(c)
		}
		return n
	}
	return 0
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

// countGhPrCalls and countGhSearchCalls distinguish prPoller's "gh pr view"
// from reviewPoller's "gh search prs" - both are the "gh" binary, so
// CallCount("gh") alone cannot tell them apart. Unlike countCalls above,
// these go through CallCountFunc rather than ranging over cmd.Calls
// directly: they are read while collect.Collector's remote workers may still
// be running on their own goroutines, and cmd.Calls is only safe to read
// under MockCommander's own lock in that case.
func countGhPrCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "gh" && len(c.Args) > 0 && c.Args[0] == "pr"
	})
}

func countGhSearchCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "gh" && len(c.Args) > 0 && c.Args[0] == "search"
	})
}

// TestStartPollForceReachesInvalidateEndToEnd pins the wiring, not just the
// primitive: internal/collect's own TestInvalidateForcesARefetchOfGitAndPR
// proves Collector.Invalidate works, but nothing before this test exercised
// startPoll(true) -> collectCmd(force) -> Invalidate as one path. Dropping
// the Invalidate call from collectCmd's force branch left every other test
// in this package green.
func TestStartPollForceReachesInvalidateEndToEnd(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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
	// queue_enabled: false, since this fixture's bare "gh" handler would
	// otherwise also answer reviewPoller's search and inflate the counts
	// below, which are about prPoller alone.
	m.collector = collect.New(&config.Config{Settings: map[string]any{"queue_enabled": "false"}}, cmd)

	ctx := context.Background()

	unforced := m.startPoll(false)
	if unforced == nil {
		t.Fatal("the first poll was refused")
	}
	if _, ok := unforced().(SnapshotMsg); !ok {
		t.Fatal("the first poll did not produce a SnapshotMsg")
	}
	if got := countCalls(cmd, "gh"); got != 0 {
		t.Fatalf("got %d gh calls inside a poll, want 0: Snapshot must not touch the network", got)
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}

	m.pollInFlight = false // stand in for that first poll's SnapshotMsg having landed
	forced := m.startPoll(true)
	if forced == nil {
		t.Fatal("the forced poll was refused")
	}
	if _, ok := forced().(SnapshotMsg); !ok {
		t.Fatal("the forced poll did not produce a SnapshotMsg")
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 2 {
		t.Errorf("got %d gh calls after a forced poll, want 2 (force should have made every entry due)", got)
	}
}

// TestCollectCmdReturnsThePopulatedQueue is the third of the three
// deletion-silent sites the whole-branch review named: `queue, hidden :=
// collector.Queue(sessions)` inside collectCmd. TestStartPollForceReaches
// InvalidateEndToEnd above builds a fixture of the same shape but sets
// queue_enabled: "false" to keep prPoller's gh call count isolated from the
// queue pollers' own gh/short calls - this one needs queue_enabled left at
// its default (true) instead, which is what actually reaches Collector.Queue.
func TestCollectCmdReturnsThePopulatedQueue(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.On("gh", `[{"number":34967,"repository":{"name":"portal"},"title":"Timeline tab",
		"updatedAt":"2026-07-31T18:54:14Z","url":"https://github.com/huntresslabs/portal/pull/34967"}]`, nil)
	cmd.On("short", `{"data":[]}`, nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)

	m.collector.RefreshRemote(context.Background())

	msg := m.collectCmd(false)()
	snap, ok := msg.(SnapshotMsg)
	if !ok {
		t.Fatalf("collectCmd returned %T, want SnapshotMsg", msg)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].ID != "34967" {
		t.Fatalf("Queue = %+v, want one item with ID 34967", snap.Queue)
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

// awaitSnapshot reads from a runAll channel until a SnapshotMsg comes out,
// discarding whatever else the command tree carried (a chain link, a probe).
func awaitSnapshot(t *testing.T, msgs <-chan tea.Msg) tea.Msg {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				t.Fatal("the command tree produced no SnapshotMsg")
			}
			if _, isSnapshot := msg.(SnapshotMsg); isSnapshot {
				return msg
			}
		case <-deadline:
			t.Fatal("timed out waiting for a SnapshotMsg")
		}
	}
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
	setPollInterval(t, time.Millisecond)

	release := make(chan struct{})
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}": func(context.Context, string, []string) (string, error) {
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
	// immediately blocks inside Snapshot at the gated tmux call. The tick's
	// batch also carries the chain's next link; run both the way the runtime
	// would and pick P1's result out below.
	next, pollCmd := m.Update(CollectTickMsg{Epoch: 5})
	m = next.(Model)
	if pollCmd == nil || !m.pollInFlight {
		t.Fatal("CollectTickMsg should have issued P1 and marked a poll in flight")
	}
	p1Done := runAll(pollCmd)

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

	p1Msg := awaitSnapshot(t, p1Done)
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

// TestForcedPollCompletionSchedulesNoTick is the ambient-vs-forced distinction
// that SnapshotMsg.Forced used to carry, restated as the invariant that
// replaced it: no completed poll of any kind extends the chain, so nothing has
// to be able to tell them apart. A forced poll rides alongside the chain for
// one snapshot's worth of data and ends.
func TestForcedPollCompletionSchedulesNoTick(t *testing.T) {
	setPollInterval(t, time.Millisecond)
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
	if snap, ok := forcedMsg.(SnapshotMsg); !ok || !snap.Local {
		t.Fatalf("got %+v, want a local SnapshotMsg from the forced poll", forcedMsg)
	}

	_, after := m.Update(forcedMsg)
	if got := countCollectTicks(after); got != 0 {
		t.Errorf("a forced poll's completion scheduled %d ticks, want 0", got)
	}
}

// TestAFailedForcedPollSchedulesNoTick covers collectCmd's error path, which
// builds its SnapshotMsg separately from the success path and is therefore the
// one place the two could diverge again.
func TestAFailedForcedPollSchedulesNoTick(t *testing.T) {
	setPollInterval(t, time.Millisecond)
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
	if !ok || !snap.Local || snap.Sessions != nil {
		t.Fatalf("got %+v, want a local SnapshotMsg with no sessions (a failed poll)", msg)
	}

	_, after := m.Update(msg)
	if got := countCollectTicks(after); got != 0 {
		t.Errorf("a failed forced poll's completion scheduled %d ticks, want 0", got)
	}
}

// TestRefreshKeyReachesInvalidateEndToEnd covers the wiring
// TestStartPollForceReachesInvalidateEndToEnd stops one call short of: that one
// calls startPoll(true) itself, so nothing verified that the r key is what
// passes force. Changing keys.Refresh to startPoll(false) left every other
// test in this package green while silently turning 'r' into "wait out
// pr_interval like any other tick".
func TestRefreshKeyReachesInvalidateEndToEnd(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
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
	// queue_enabled: false, since this fixture's bare "gh" handler would
	// otherwise also answer reviewPoller's search and inflate the counts
	// below, which are about prPoller alone.
	m.collector = collect.New(&config.Config{Settings: map[string]any{"queue_enabled": "false"}}, cmd)

	ctx := context.Background()

	first := m.startPoll(false)
	if first == nil {
		t.Fatal("the first poll was refused")
	}
	if _, ok := first().(SnapshotMsg); !ok {
		t.Fatal("the first poll did not produce a SnapshotMsg")
	}
	if got := countCalls(cmd, "gh"); got != 0 {
		t.Fatalf("got %d gh calls inside a poll, want 0: Snapshot must not touch the network", got)
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 1 {
		t.Fatalf("got %d gh calls after the first pass, want 1", got)
	}
	m.pollInFlight = false // stand in for that poll's SnapshotMsg having landed

	next, refresh := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = next.(Model)
	if refresh == nil {
		t.Fatal("Refresh issued no command while self-polling")
	}
	if _, ok := refresh().(SnapshotMsg); !ok {
		t.Fatal("Refresh did not produce a SnapshotMsg")
	}
	m.collector.RefreshRemote(ctx)
	if got := countCalls(cmd, "gh"); got != 2 {
		t.Errorf("got %d gh calls after pressing r, want 2 (r must invalidate the PR memo)", got)
	}
}

// TestNewStartsTheRemoteWorkers pins the client half of the line the daemon
// needed in Run. Without Collector.Start in newModel, a self-polling panel
// renders tmux and git correctly and never shows a PR column again, and every
// test in internal/collect stays green because they drive RefreshRemote
// directly.
//
// This goes through the real New rather than newTestModel: newTestModel builds
// a Model literal, so it cannot observe anything newModel does.
func TestNewStartsTheRemoteWorkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no socket there, so this client self-polls

	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
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
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	m := New(&config.Config{}, cmd)
	defer m.cancel()
	if m.daemonDecoder != nil {
		t.Fatal("fixture is broken: this client found a daemon and will never self-poll")
	}

	// Init's first collectCmd is what nudges the workers.
	if msg := m.collectCmd(false)(); msg == nil {
		t.Fatal("collectCmd produced no message")
	}

	deadline := time.After(3 * time.Second)
	for cmd.CallCount("gh") == 0 {
		select {
		case <-deadline:
			t.Fatal("no gh call after 3s: New never started the remote workers")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestADaemonFedClientSpendsNoGhBudget is the whole reason the remote layer
// has no ticker. One daemon means one gh budget regardless of how many panels
// are open; a ticker in the poller would quietly restore per-panel polling and
// nothing else in the suite would notice.
//
// Two things give it teeth against exactly that, and both are load-bearing.
// The self-poll before the daemon attaches is one: the PR store has no working
// set until a Snapshot tracks one, and a pass over an empty working set spends
// nothing whether a ticker or a nudge woke it. Zeroing PRInterval and
// QueueInterval is the other: at their defaults (30s, 60s) a woken pass would
// find nothing due and spend nothing either. Zeroing PRInterval is also the
// real path - a client that loses a daemon self-polls and gets one back.
func TestADaemonFedClientSpendsNoGhBudget(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
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
	cmd.On("gh", `{"number": 42, "state": "OPEN"}`, nil)

	m := newTestModel()
	m.cmd = cmd
	m.collector = collect.New(&config.Config{}, cmd)
	m.collector.PRInterval = 0
	// Zeroed for the same reason as PRInterval: at the default 60s a woken
	// storyPoller/reviewPoller pass would find nothing due and spend nothing,
	// which would let a ticker through undetected on the short assertion
	// below - begin would say "not due" regardless of who woke it.
	m.collector.QueueInterval = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.collector.Start(ctx)

	if _, err := m.collector.Snapshot(ctx); err != nil {
		t.Fatalf("the self-polling snapshot failed: %v", err)
	}
	// Waits for prPoller's call, reviewPoller's call and storyPoller's call
	// separately, not just a nonzero gh count: three workers wake off the
	// same nudge and finish in whatever order the scheduler picks, and gh is
	// shared by two of them. Without distinguishing prPoller's "gh pr view"
	// from reviewPoller's "gh search prs", a prior test's warmed nwoCache
	// (see below) could let prPoller alone spend two gh calls - fetchReviewThreads
	// would then also run - satisfying a plain CallCount("gh") >= 2 before
	// reviewPoller ever ran, so the assertions below would pass for the wrong
	// reason and any real problem would surface as a false "only Snapshot may
	// wake a poller" failure after the daemon attaches instead.
	deadline := time.After(3 * time.Second)
	for countGhPrCalls(cmd) < 1 || countGhSearchCalls(cmd) < 1 || cmd.CallCount("short") < 1 {
		select {
		case <-deadline:
			t.Fatalf("the self-polling client never finished: gh pr=%d gh search=%d short=%d, this fixture cannot detect a ticker",
				countGhPrCalls(cmd), countGhSearchCalls(cmd), cmd.CallCount("short"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Hardcoded, not latched off the count so far: fetch.nwoCache is a
	// package-level sync.Map keyed by git root, shared across every test in
	// the binary. This fixture's getNWO has no "git remote get-url origin"
	// handler, so fetchReviewThreads bails before its graphql call, leaving
	// prPoller's "gh pr view" and reviewPoller's "gh search prs" as the only
	// two gh calls a pass over /repo/alpha can ever spend here - but only as
	// long as nothing else in the binary warms that cache entry first.
	// Latching off cmd.CallCount would silently stop catching a ticker
	// regression the day some other test did.
	const selfPolledGh = 2
	const selfPolledShort = 1
	if got := cmd.CallCount("gh"); got != selfPolledGh {
		t.Fatalf("got %d gh calls from the self-polling client, want %d", got, selfPolledGh)
	}
	if got := cmd.CallCount("short"); got != selfPolledShort {
		t.Fatalf("got %d short calls from the self-polling client, want %d", got, selfPolledShort)
	}

	m.daemonConn = &fakeConn{}
	if got := m.startPoll(false); got != nil {
		t.Fatal("a daemon-fed client issued a poll of its own")
	}
	time.Sleep(300 * time.Millisecond)

	// CallCount, not countCalls: the workers are live, so a ticker would have
	// this assertion race the append it exists to catch.
	if got := cmd.CallCount("gh"); got != selfPolledGh {
		t.Errorf("got %d gh calls once a daemon was feeding this client, want %d: only Snapshot may wake a poller", got, selfPolledGh)
	}
	if got := cmd.CallCount("short"); got != selfPolledShort {
		t.Errorf("got %d short calls once a daemon was feeding this client, want %d: only Snapshot may wake a poller", got, selfPolledShort)
	}
}

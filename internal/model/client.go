package model

import (
	"context"
	"errors"
	"net"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
)

// firstSnapshotTimeout bounds how long New waits for the daemon's first
// snapshot after a successful dial. A healthy daemon answers within
// milliseconds; a daemon whose first poll failed has nothing to send and
// would otherwise block Next() forever. On timeout, listenDaemonCmd's error
// path emits DaemonLostMsg and the TUI falls back to self-polling.
//
// A var, not a const, so tests can shorten it rather than waiting out the
// real 5s.
var firstSnapshotTimeout = 5 * time.Second

func dialDaemon(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 300*time.Millisecond)
}

// daemonProbeInterval is how often a self-polling client tries the daemon
// socket again. Fallback is a supported mode, so this is not urgent; it just
// has to be short enough that a daemon restart does not leave a panel
// self-polling for minutes.
//
// A var, not a const, for the same reason firstSnapshotTimeout is one: tests
// that assert a probe was scheduled have to run the tick to see it, and
// waiting out the real interval on every run is not worth the wall clock.
var daemonProbeInterval = 2 * time.Second

// defaultPollInterval paces the self-poll loop when tmux_interval is unset or
// non-positive. GetSettingDuration only has second-granularity, so tests that
// need to observe a CollectTickMsg firing shorten this var instead, the same
// way they shorten daemonProbeInterval.
var defaultPollInterval = 1 * time.Second

func probeTickCmd(epoch int) tea.Cmd {
	return tea.Tick(daemonProbeInterval, func(time.Time) tea.Msg {
		return ProbeTickMsg{Epoch: epoch}
	})
}

// dialDaemonCmd dials off the UI goroutine, where the 300ms connect timeout
// is allowed to block.
func dialDaemonCmd(path string, epoch int) tea.Cmd {
	return func() tea.Msg {
		conn, err := dialDaemon(path)
		if err != nil {
			return DaemonProbeResultMsg{Epoch: epoch}
		}
		return DaemonProbeResultMsg{
			Epoch:   epoch,
			Conn:    conn,
			Decoder: protocol.NewDecoder(conn),
		}
	}
}

// collectCmd runs one self-polling cycle. It returns a SnapshotMsg on every
// outcome, failures included: that message is what clears pollInFlight, so an
// outcome that produced nothing would leave startPoll refusing every future
// poll for the life of the process. Nil Sessions means the poll failed and
// handleSnapshot must leave the existing sessions alone.
//
// force invalidates the collector's git and PR memos before collecting, so a
// caller that just changed state (an action, the Refresh key) does not have
// to wait out git_interval or pr_interval to see it. Invalidate runs inside
// this same closure, on the goroutine that is about to call Snapshot, which
// is the only thing that makes it safe: startPoll guarantees this is the only
// collectCmd in flight, so nothing else can be reading the memos concurrently.
func (m Model) collectCmd(force bool) tea.Cmd {
	collector := m.collector
	ctx := m.ctx
	cmd := m.cmd
	fallbackCurrent := m.currentSessionName
	epoch := m.epoch
	return func() tea.Msg {
		if force {
			collector.Invalidate()
		}
		sessions, err := collector.Snapshot(ctx)
		if err != nil {
			return SnapshotMsg{Epoch: epoch, Local: true}
		}
		annotateClientFlags(ctx, cmd, sessions, fallbackCurrent)
		queue, hidden := collector.Queue(sessions)
		return SnapshotMsg{Sessions: sessions, Epoch: epoch, Local: true, Queue: queue, QueueHidden: hidden}
	}
}

// collectTickCmd is one link of the self-poll chain. A chain is started once
// per self-polling generation (Init, handleDaemonLost) and continued only by
// the CollectTickMsg handler, which always schedules the next link whether or
// not it managed to issue a poll - so a generation has exactly one chain and
// never zero.
func collectTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return CollectTickMsg{Epoch: epoch}
	})
}

// annotateClientFlags fills in the fields that belong to this tmux client
// rather than to the snapshot: which session is current and which was last.
// The daemon serves many clients and cannot know either.
func annotateClientFlags(ctx context.Context, cmd fetch.Commander, sessions []*session.Session, fallbackCurrent string) {
	current := fetch.CurrentSession(ctx, cmd)
	if current == "" {
		current = fallbackCurrent
	}
	last := fetch.LastSession(ctx, cmd)
	for _, s := range sessions {
		s.IsCurrent = s.Name == current
		s.IsLast = s.Name == last
	}
}

// listenDaemonCmd reads one snapshot per invocation; Update re-issues it on
// every SnapshotMsg. Which session is current or last belongs to this tmux
// client rather than to the daemon, so those are resolved here, off the UI
// goroutine, where the tmux queries are allowed to block.
func listenDaemonCmd(
	decoder *protocol.Decoder,
	ctx context.Context,
	cmd fetch.Commander,
	fallbackCurrent string,
	epoch int,
) tea.Cmd {
	return func() tea.Msg {
		snap, err := decoder.Next()
		if err != nil {
			return DaemonLostMsg{Epoch: epoch}
		}
		annotateClientFlags(ctx, cmd, snap.Sessions, fallbackCurrent)
		return SnapshotMsg{
			Sessions:    snap.Sessions,
			Jobs:        snap.Jobs,
			DaemonBin:   snap.DaemonBin,
			Queue:       snap.Queue,
			QueueHidden: snap.QueueHidden,
			Epoch:       epoch,
		}
	}
}

// daemonSpawner starts a detached daemon process. Written only through
// SetDaemonSpawner and read only through spawnDaemon.
//
// The seam is closed rather than conventional. Under `go test`,
// os.Executable() is the test binary, so a test that reached the real spawner
// forked the whole suite as a subprocess - which reached the same code and
// forked again. That was live-reproduced as 800 processes, and a comment
// asking tests not to do it is thin cover for that. This package no longer
// imports internal/daemon at all, so the real spawner is not nameable from
// here or from any test in this package; main wires it in at startup. A test
// installs its own stub, which is the point, but it cannot install the one
// that forks without adding an import that would be conspicuous in review.
var daemonSpawner func() error

// SetDaemonSpawner installs the process spawner. Called once from main,
// before the program starts.
func SetDaemonSpawner(fn func() error) { daemonSpawner = fn }

// spawnDaemon is every caller's entry point. Unset means no daemon can be
// started - the state every test runs in, and a state a caller is told about
// rather than left to wonder at.
func spawnDaemon() error {
	if daemonSpawner == nil {
		return errors.New("no daemon spawner is configured")
	}
	return daemonSpawner()
}

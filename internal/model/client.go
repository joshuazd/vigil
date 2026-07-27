package model

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
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
const daemonProbeInterval = 2 * time.Second

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

		current := fetch.CurrentSession(ctx, cmd)
		if current == "" {
			current = fallbackCurrent
		}
		last := fetch.LastSession(ctx, cmd)

		names := make(map[string]bool, len(snap.Sessions))
		for _, s := range snap.Sessions {
			names[s.Name] = true
		}
		if !names[last] {
			last = ""
		}
		for _, s := range snap.Sessions {
			s.IsCurrent = s.Name == current
			s.IsLast = s.Name == last
		}

		return SnapshotMsg{Sessions: snap.Sessions, Epoch: epoch}
	}
}

// daemonSpawner is the indirection tests replace; production always uses
// spawnDaemon.
var daemonSpawner = spawnDaemon

// spawnDaemon starts `vigil daemon` detached from this process, so it outlives
// the pane that started it. Its output goes to a log file beside the socket:
// the daemon is silent when healthy, and when it is not, that log is the only
// place the reason survives.
func spawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(protocol.SocketPath()), "vigild.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid detaches it from this pane's process group, so closing the pane
	// or the tmux session does not take the daemon with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it if it exits, rather than leaving a zombie for the life of a
	// long-running panel.
	go func() { _ = cmd.Wait() }()
	return nil
}

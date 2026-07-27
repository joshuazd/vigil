package model

import (
	"context"
	"net"
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
const firstSnapshotTimeout = 5 * time.Second

func dialDaemon(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 300*time.Millisecond)
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
) tea.Cmd {
	return func() tea.Msg {
		snap, err := decoder.Next()
		if err != nil {
			return DaemonLostMsg{}
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

		return SnapshotMsg{Sessions: snap.Sessions}
	}
}

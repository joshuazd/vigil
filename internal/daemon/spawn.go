package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// Spawn starts `vigil daemon` detached from this process, so it outlives the
// pane that started it. Its output goes to a log file beside the socket: the
// daemon is silent when healthy, and when it is not, that log is the only place
// the reason survives.
func Spawn() error {
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
	// Not the caller's cwd: that is often a git worktree, and
	// git-worktree-done removes those routinely, leaving a long-lived daemon
	// holding a deleted directory.
	cmd.Dir = "/"
	// TMUX and TMUX_PANE identify one pane's client. A daemon that inherited
	// them would carry that identity for its whole life, stale the moment the
	// pane died, and would make an is-in-tmux check in a job's script lie.
	cmd.Env = withoutTmuxEnv(os.Environ())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid detaches it from this pane's process group, so closing the pane
	// or the tmux session does not take the daemon with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func withoutTmuxEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

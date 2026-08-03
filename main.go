package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/daemon"
	"github.com/jzinkduda/vigil/internal/dispatch"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/model"
	"github.com/jzinkduda/vigil/internal/protocol"
)

var version = "dev"

// startupDependencies are the binaries vigil refuses to start without.
// `short` is deliberately absent: a missing Shortcut CLI leaves the story half
// of the work queue empty, which is a degraded feature rather than a reason to
// refuse to run.
var startupDependencies = []string{"tmux", "git", "gh"}

// execSelf is a seam. A test that called syscall.Exec directly would replace
// the test binary with a second copy of vigil.
var execSelf = syscall.Exec

// restartRequester is satisfied by model.Model. Asserting an interface here
// instead of the concrete type keeps this file free of a test-only exported
// setter on internal/model; TestTheRealModelSatisfiesRestartRequester in
// main_test.go is the compile-time check that the two have not drifted apart.
type restartRequester interface{ RestartRequested() bool }

func parseArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "tui", nil, nil
	}
	switch args[0] {
	case "daemon":
		return "daemon", args[1:], nil
	case "config":
		return "config", args[1:], nil
	case "dispatch":
		return "dispatch", args[1:], nil
	case "--panel":
		return "panel", args[1:], nil
	case "--help", "-h":
		return "help", args[1:], nil
	case "--version", "-v":
		return "version", args[1:], nil
	default:
		return "", nil, fmt.Errorf("unknown argument: %s", args[0])
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	command, rest, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vigil: %v\n", err)
		printUsage(stderr)
		return 2
	}

	switch command {
	case "help":
		printUsage(stdout)
		return 0
	case "version":
		_, _ = fmt.Fprintln(stdout, "vigil "+version)
		return 0
	case "config":
		return runConfigGet(rest, stdout, stderr)
	}

	for _, dep := range startupDependencies {
		if _, err := exec.LookPath(dep); err != nil {
			_, _ = fmt.Fprintf(stderr, "vigil: %s not found in PATH\n", dep)
			return 1
		}
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vigil: %v (using defaults)\n", err)
	}
	warnAboutAnUnmigratedDispatchHook(cfg, stderr)
	cmd := &fetch.ExecCommander{}

	// internal/model does not import internal/daemon, so this is the only
	// place the real spawner is named. See model.SetDaemonSpawner.
	model.SetDaemonSpawner(daemon.Spawn)

	switch command {
	case "daemon":
		err = runDaemon(cfg, cmd)
	case "panel":
		err = runPanel(cfg, cmd)
	case "dispatch":
		err = runDispatch(rest, cfg, stdout)
	default:
		err = runTUI(cfg, cmd)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vigil: %v\n", err)
		return 1
	}
	return 0
}

// warnAboutAnUnmigratedDispatchHook names the one config change phase 4
// requires of an existing user, because neither failure it prevents explains
// itself.
//
// The dispatch hook now runs inside vigild, which has no tty. `--detached`
// tells the workflow scripts to skip the teleport, which is the entire point
// of dispatching; DISPATCH_IN_POPUP (now DISPATCH_INLINE) leaves the hook on
// the branch that runs `tmux display-popup -E` from a client-less process,
// which cannot draw. The README carries the hook to migrate to.
func warnAboutAnUnmigratedDispatchHook(cfg *config.Config, stderr io.Writer) {
	hook := cfg.GetHook("dispatch")
	for _, stale := range []string{"--detached", "DISPATCH_IN_POPUP"} {
		if !strings.Contains(hook, stale) {
			continue
		}
		_, _ = fmt.Fprintf(stderr,
			"vigil: the dispatch hook still passes %s; it now runs inside vigild, "+
				"which has no terminal. See the dispatch section of the README: the hook "+
				"should be DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}\n", stale)
		return
	}

	if hook != "" && !strings.Contains(hook, "{flags}") {
		_, _ = fmt.Fprintf(stderr,
			"vigil: the dispatch hook has no {flags} placeholder, so selecting a queue "+
				"item will teleport instead of dispatching in the background. The hook "+
				"should be DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}\n")
	}
}

// runConfigGet answers before the dependency check on purpose: reading a
// config value has no business requiring gh to be installed.
func runConfigGet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "get" {
		_, _ = fmt.Fprintln(stderr, "vigil: usage: vigil config get <key>")
		return 2
	}
	key := args[1]
	if !config.IsSetting(key) {
		_, _ = fmt.Fprintf(stderr, "vigil: unknown setting: %s\n", key)
		return 1
	}
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vigil: %v (using defaults)\n", err)
	}
	_, _ = fmt.Fprintln(stdout, cfg.GetSetting(key))
	return 0
}

func runDaemon(cfg *config.Config, cmd fetch.Commander) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.New(cfg, cmd).Run(ctx)
}

// restartIfRequested replaces this process with the newer image on disk. It
// runs after p.Run() has returned, because Bubble Tea restores the terminal on
// its way out and an exec from inside Update would hand the new process raw
// mode and an alt screen nobody left.
func restartIfRequested(final tea.Model) error {
	m, ok := final.(restartRequester)
	if !ok || !m.RestartRequested() {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return execSelf(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}

func runTUI(cfg *config.Config, cmd fetch.Commander) error {
	final, err := tea.NewProgram(model.New(cfg, cmd), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return restartIfRequested(final)
}

// runPanel renders the compact session list for a single tmux pane. It shares
// every code path with the dashboard, so panel and dashboard can never
// disagree about state.
func runPanel(cfg *config.Config, cmd fetch.Commander) error {
	final, err := tea.NewProgram(model.NewPanel(cfg, cmd), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return restartIfRequested(final)
}

// runDispatch submits a job and returns. Exit 0 means the daemon accepted the
// job, not that the job succeeded: the point of the daemon owning it is that it
// outlives this process.
func runDispatch(args []string, cfg *config.Config, stdout io.Writer) error {
	cwd := ""
	var input string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd":
			if i+1 >= len(args) {
				return fmt.Errorf("--cwd needs a path")
			}
			cwd = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown option: %s", args[i])
			}
			input = args[i]
		}
	}
	if input == "" {
		return fmt.Errorf("usage: vigil dispatch [--cwd <path>] <url-or-id>")
	}
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	job, err := dispatch.Submit(context.Background(), dispatch.Options{
		Input:      input,
		Cwd:        cwd,
		SocketPath: protocol.SocketPath(),
		Spawn:      daemon.Spawn,
		AckTimeout: dispatch.DefaultAckTimeout,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "dispatch queued: %s\n", job.Input)
	return nil
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "vigil - TUI mission control for tmux sessions")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  vigil            Run the dashboard")
	_, _ = fmt.Fprintln(w, "  vigil daemon     Run the state daemon in the foreground")
	_, _ = fmt.Fprintln(w, "  vigil --panel    Run the compact session list for a tmux pane")
	_, _ = fmt.Fprintln(w, "  vigil config get <key>   Print a config value")
	_, _ = fmt.Fprintln(w, "  vigil dispatch <url-or-id>   Submit a job to the daemon")
	_, _ = fmt.Fprintln(w, "  vigil --help")
	_, _ = fmt.Fprintln(w, "  vigil --version")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Config: ~/.config/vigil/config.toml")
}

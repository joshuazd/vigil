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
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/daemon"
	"github.com/jzinkduda/vigil/internal/dispatch"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/model"
	"github.com/jzinkduda/vigil/internal/protocol"
)

var version = "dev"

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

	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			_, _ = fmt.Fprintf(stderr, "vigil: %s not found in PATH\n", dep)
			return 1
		}
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vigil: %v (using defaults)\n", err)
	}
	cmd := &fetch.ExecCommander{}

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

func runTUI(cfg *config.Config, cmd fetch.Commander) error {
	m := model.New(cfg, cmd)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// runPanel renders the compact session list for a single tmux pane. It shares
// every code path with the dashboard, so panel and dashboard can never
// disagree about state.
func runPanel(cfg *config.Config, cmd fetch.Commander) error {
	p := tea.NewProgram(model.NewPanel(cfg, cmd), tea.WithAltScreen())
	_, err := p.Run()
	return err
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
		AckTimeout: 5 * time.Second,
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

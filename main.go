package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/daemon"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/model"
)

var version = "dev"

func parseArgs(args []string) (string, error) {
	if len(args) == 0 {
		return "tui", nil
	}
	switch args[0] {
	case "daemon":
		return "daemon", nil
	case "--help", "-h":
		return "help", nil
	case "--version", "-v":
		return "version", nil
	default:
		return "", fmt.Errorf("unknown argument: %s", args[0])
	}
}

func main() {
	command, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
		printUsage(os.Stderr)
		os.Exit(2)
	}

	switch command {
	case "help":
		printUsage(os.Stdout)
		return
	case "version":
		fmt.Println("vigil " + version)
		return
	}

	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %s not found in PATH\n", dep)
			os.Exit(1)
		}
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v (using defaults)\n", err)
	}
	cmd := &fetch.ExecCommander{}

	switch command {
	case "daemon":
		if err := runDaemon(cfg, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
			os.Exit(1)
		}
	default:
		if err := runTUI(cfg, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
			os.Exit(1)
		}
	}
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

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "vigil - TUI mission control for tmux sessions")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  vigil            Run the dashboard")
	_, _ = fmt.Fprintln(w, "  vigil daemon     Run the state daemon in the foreground")
	_, _ = fmt.Fprintln(w, "  vigil --help")
	_, _ = fmt.Fprintln(w, "  vigil --version")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Config: ~/.config/vigil/config.toml")
}

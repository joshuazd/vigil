package main

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/model"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h":
			fmt.Println("vigil — TUI mission control for tmux sessions")
			fmt.Println()
			fmt.Println("Usage: vigil [--help] [--version]")
			fmt.Println()
			fmt.Println("Config: ~/.config/vigil/config.toml")
			os.Exit(0)
		case "--version", "-v":
			fmt.Println("vigil " + version)
			os.Exit(0)
		}
	}

	// Check dependencies
	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %s not found in PATH\n", dep)
			os.Exit(1)
		}
	}

	// Load config
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v (using defaults)\n", err)
	}

	// Create commander
	cmd := &fetch.ExecCommander{}

	// Create and run app
	m := model.New(cfg, cmd)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "vigil: %v\n", err)
		os.Exit(1)
	}
}

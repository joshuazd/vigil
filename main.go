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

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Println("vigil — TUI mission control for tmux sessions")
		fmt.Println()
		fmt.Println("Usage: vigil [--help]")
		fmt.Println()
		fmt.Println("Config: ~/.config/vigil/config.toml")
		os.Exit(0)
	}

	// Check dependencies
	for _, dep := range []string{"tmux", "git", "gh"} {
		if _, err := exec.LookPath(dep); err != nil {
			fmt.Fprintf(os.Stderr, "vigil: %s not found in PATH\n", dep)
			os.Exit(1)
		}
	}

	// Load config
	cfg := config.Load(config.ConfigPath())

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

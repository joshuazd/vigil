package model

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/view"
)

func panelModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.panelMode = true
	m.detailOpen = false
	m.width, m.height = 40, 10
	m.sessions = []*session.Session{
		{Name: "SC-1 alpha", Git: session.GitStatus{Branch: "feature/a", Modified: 2}},
		{Name: "SC-2 beta", Git: session.GitStatus{Branch: "feature/b"}},
	}
	return m
}

// TestPanelViewFitsItsPane is the requirement that makes a panel usable at
// all: exactly the pane's height, never wider than the pane.
func TestPanelViewFitsItsPane(t *testing.T) {
	m := panelModel(t)
	for _, size := range [][2]int{{40, 10}, {80, 12}, {24, 6}, {200, 10}} {
		m.width, m.height = size[0], size[1]
		lines := strings.Split(m.View(), "\n")
		if len(lines) != m.height {
			t.Errorf("%dx%d: rendered %d lines", m.width, m.height, len(lines))
		}
		for i, line := range lines {
			if got := view.VisibleWidth(line); got > m.width {
				t.Errorf("%dx%d line %d: %d visible columns", m.width, m.height, i, got)
			}
		}
	}
}

// TestPanelViewHasNoFooter pins that the panel spends its rows on sessions.
func TestPanelViewHasNoFooter(t *testing.T) {
	out := panelModel(t).View()
	for _, help := range []string{"merge", "approve", "cleanup", "rebase"} {
		if strings.Contains(out, help) {
			t.Errorf("panel rendered the keybinding footer (%q)", help)
		}
	}
}

func TestPanelViewShowsNoDaemonWhenSelfPolling(t *testing.T) {
	if !strings.Contains(view.StripANSI(panelModel(t).View()), "no daemon") {
		t.Error("a self-polling panel gave no indication the daemon is down")
	}
}

// TestPanelIgnoresToggleDetail: the detail panel would swallow a 10-row strip
// whole, and its pane captures would run once per panel per tick.
func TestPanelIgnoresToggleDetail(t *testing.T) {
	m := panelModel(t)
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got.(Model).detailOpen {
		t.Error("tab opened the detail panel in a panel")
	}
}

func TestNewPanelSetsPanelMode(t *testing.T) {
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	m := NewPanel(&config.Config{}, fetch.NewMockCommander())
	if !m.panelMode {
		t.Error("NewPanel did not set panelMode")
	}
	if m.detailOpen {
		t.Error("NewPanel left the detail panel open")
	}
}

// TestNewPanelSpawnsTheDaemon is the reason task 1 exists: N panels starting
// at once all try this, and the flock is what makes that safe.
func TestNewPanelSpawnsTheDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t)) // no socket to dial
	NewPanel(&config.Config{}, fetch.NewMockCommander())
	if spawned != 1 {
		t.Errorf("spawned %d daemons, want 1", spawned)
	}
}

func TestNewDoesNotSpawnTheDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
	New(&config.Config{}, fetch.NewMockCommander())
	if spawned != 0 {
		t.Errorf("the TUI spawned %d daemons; only panels do that", spawned)
	}
}

// TestPanelRespawnsARateLimitedDaemon keeps a crashed daemon from leaving
// every panel self-polling forever, without letting a panel fork in a loop.
func TestPanelRespawnsARateLimitedDaemon(t *testing.T) {
	spawned := 0
	daemonSpawner = func() error { spawned++; return nil }
	t.Cleanup(func() { daemonSpawner = spawnDaemon })

	m := panelModel(t)
	m.epoch = 1
	m.lastSpawn = time.Now().Add(-time.Hour)

	got, _ := m.Update(DaemonProbeResultMsg{Epoch: 1})
	if spawned != 1 {
		t.Fatalf("spawned %d, want 1 after a failed probe", spawned)
	}
	next := got.(Model)
	if _, _ = next.Update(DaemonProbeResultMsg{Epoch: next.epoch}); spawned != 1 {
		t.Errorf("spawned %d, want the second attempt rate-limited", spawned)
	}
}

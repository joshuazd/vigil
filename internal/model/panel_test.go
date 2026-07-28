package model

import (
	"context"
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
	// A panel is always in a tmux pane, so the real thing always has this set.
	// Leaving it false made Enter fall through to the detail toggle, which is
	// not a state any real panel can be in.
	m.insideTmux = true
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

// withCancellableCtx gives a model a context whose cancellation is observable,
// which is how these tests tell "switched to the session" apart from "switched
// to the session and started shutting down".
func withCancellableCtx(t *testing.T, m Model) Model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.ctx, m.cancel = ctx, cancel
	return m
}

func tmuxCalls(t *testing.T, m Model) []fetch.MockCall {
	t.Helper()
	mock, ok := m.cmd.(*fetch.MockCommander)
	if !ok {
		t.Fatalf("test model is not using a MockCommander, got %T", m.cmd)
	}
	return mock.Calls
}

// TestPanelSelectSwitchesWithoutQuitting pins the bug this fixes. A panel runs
// inside tmux, so it inherited popup mode's switch-then-quit. The pane's
// process exiting, plus the toggle script's deliberate remain-on-exit off,
// meant the panel deleted itself every time it was used for the one thing it
// exists for.
func TestPanelSelectSwitchesWithoutQuitting(t *testing.T) {
	m := withCancellableCtx(t, panelModel(t))

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := got.(Model)
	if cmd == nil {
		t.Fatal("Enter produced no command")
	}
	cmd()

	var switched bool
	for _, c := range tmuxCalls(t, next) {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "switch-client" {
			switched = true
		}
	}
	if !switched {
		t.Errorf("Enter did not switch to the session; tmux calls were %+v", tmuxCalls(t, next))
	}
	if next.ctx.Err() != nil {
		t.Error("Enter cancelled the panel's context, so the panel is shutting down")
	}
}

// TestPopupSelectStillQuits pins the behaviour the panel must not inherit and
// the popup must keep: pick a session, then get out of the way.
func TestPopupSelectStillQuits(t *testing.T) {
	m := withCancellableCtx(t, panelModel(t))
	m.panelMode = false
	m.insideTmux = true

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter produced no command in popup mode")
	}
	if got.(Model).ctx.Err() == nil {
		t.Error("popup mode no longer cancels its context on select")
	}
}

// TestPanelOpenPRDoesNotQuit covers the same root cause at its second call
// site: o opens the PR and, in popup mode, exits afterwards.
func TestPanelOpenPRDoesNotQuit(t *testing.T) {
	m := withCancellableCtx(t, panelModel(t))
	m.sessions[0].PR = &session.PRStatus{Number: 1, URL: "https://example.invalid/pr/1"}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if got.(Model).ctx.Err() != nil {
		t.Error("o cancelled the panel's context, so the panel is shutting down")
	}
}

// TestOpenPRUsesTheInjectedOpener pins the seam. Without it, running the
// suite opens real browser windows.
func TestOpenPRUsesTheInjectedOpener(t *testing.T) {
	m := withCancellableCtx(t, panelModel(t))
	m.sessions[0].PR = &session.PRStatus{Number: 1, URL: "https://example.invalid/pr/1"}
	var opened []string
	m.openURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	_ = got

	if len(opened) != 1 || opened[0] != "https://example.invalid/pr/1" {
		t.Fatalf("got %v, want exactly the PR URL through the injected opener", opened)
	}
}

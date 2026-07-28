package model

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/transition"
)

type countingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (c *countingEffects) Run(_ context.Context, ev transition.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *countingEffects) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// drain runs every command a batch produced, so effects dispatched as tea.Cmds
// have actually executed by the time the assertion reads the counter.
func drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(c)
		}
	}
}

func idleSession(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp/" + name}
}

// blockedSession transitions away from idle without landing on the zero value
// of SessionState. Attention is `= iota`, so a zero-valued Event would satisfy
// an assertion of `New == session.Attention` without any detection happening.
func blockedSession(name string) *session.Session {
	s := idleSession(name)
	s.Git = session.GitStatus{Branch: "feature/" + name, GitRoot: "/repo/" + name}
	s.PR = &session.PRStatus{Number: 7, State: "OPEN", Checks: "fail"}
	return s
}

func transitionModel(effects transition.EffectRunner) Model {
	m := newTestModel()
	m.cfg = &config.Config{Settings: map[string]any{"notifications_enabled": "true"}}
	m.detector = transition.NewDetector()
	m.effects = effects
	return m
}

// TestDaemonFedClientsToastButDoNotRunEffects is the blocker, asserted
// directly. Two panels attached to one daemon must produce two toasts, because
// each has its own screen, and zero side effects, because the daemon owns them.
func TestDaemonFedClientsToastButDoNotRunEffects(t *testing.T) {
	effects := &countingEffects{}
	a := transitionModel(effects)
	b := transitionModel(effects)

	for _, m := range []*Model{&a, &b} {
		m.sessions = []*session.Session{idleSession("alpha")}
		m.checkStateTransitions(false)
		m.sessions = []*session.Session{blockedSession("alpha")}
		cmds := m.checkStateTransitions(false)
		drain(tea.Batch(cmds...))
	}

	if got := len(a.notifications); got != 1 {
		t.Errorf("client A: got %d notifications, want 1", got)
	}
	if got := len(b.notifications); got != 1 {
		t.Errorf("client B: got %d notifications, want 1", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs across two daemon-fed clients, want 0", got)
	}
}

// TestASelfPollingClientRunsEffectsOnce is the other half: with no daemon this
// client owns the loop, so it must run them, exactly once.
func TestASelfPollingClientRunsEffectsOnce(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs, want 1", got)
	}
	ev := effects.events[0]
	if ev.Session != "alpha" || ev.Old != session.Idle || ev.New != session.Blocked {
		t.Errorf("got %+v, want alpha idle -> blocked", ev)
	}
}

func TestTheFirstSnapshotDoesNotToast(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)
	m.sessions = []*session.Session{blockedSession("alpha"), idleSession("beta")}

	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := len(m.notifications); got != 0 {
		t.Errorf("got %d notifications on the priming snapshot, want 0", got)
	}
	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs on the priming snapshot, want 0", got)
	}
}

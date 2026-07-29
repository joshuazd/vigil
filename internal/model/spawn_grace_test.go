package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/session"
)

// panelThatSpawnedADaemon is a self-polling panel in the state newModel
// leaves it in when it found no daemon and started one: no connection, and
// the grace period running.
func panelThatSpawnedADaemon(effects *countingEffects) Model {
	m := transitionModel(effects)
	m.panelMode = true
	m.effectsDisownedUntil = time.Now().Add(spawnGrace)
	return m
}

func TestAPanelInsideItsSpawnGraceRunsNoEffects(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 0 {
		t.Errorf("got %d effect runs during the grace period, want 0", got)
	}
}

// The toast is per-client and must survive: only hooks and cleanups are
// owned by one process.
func TestAPanelInsideItsSpawnGraceStillToasts(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	m.checkStateTransitions(true)

	if len(m.notifications) == 0 {
		t.Error("got no notifications during the grace period, want one")
	}
}

func TestAPanelOwnsEffectsOnceTheGraceExpires(t *testing.T) {
	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)
	m.effectsDisownedUntil = time.Now().Add(-time.Millisecond)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Errorf("got %d effect runs after the grace expired, want 1", got)
	}
}

// This is the test that pins the "set once" decision. handleProbeResult calls
// spawnDaemonOnce again on every failed probe, so a deadline re-armed on each
// spawn would mean a daemon that never comes up suppresses the panel's
// effects forever - the zero-hooks failure mode, which is worse than the
// double-hook one this change is fixing.
func TestARepeatedFailedProbeDoesNotExtendTheGrace(t *testing.T) {
	original := daemonSpawner
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = original })

	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)
	deadline := m.effectsDisownedUntil

	// Age lastSpawn past the cooldown so the respawn actually happens; with
	// the cooldown still in force spawnDaemonOnce returns early and the test
	// would pass against either implementation.
	m.lastSpawn = time.Now().Add(-spawnCooldown - time.Second)
	m.spawnDaemonOnce()

	if !m.effectsDisownedUntil.Equal(deadline) {
		t.Errorf("grace deadline moved from %v to %v on a respawn, want it unchanged",
			deadline, m.effectsDisownedUntil)
	}
}

func TestADashboardOwnsEffectsImmediately(t *testing.T) {
	effects := &countingEffects{}
	m := transitionModel(effects)

	m.sessions = []*session.Session{idleSession("alpha")}
	m.checkStateTransitions(true)
	m.sessions = []*session.Session{blockedSession("alpha")}
	drain(tea.Batch(m.checkStateTransitions(true)...))

	if got := effects.count(); got != 1 {
		t.Errorf("got %d effect runs on a dashboard, want 1", got)
	}
}

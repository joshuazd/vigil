package model

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
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

// TestARepeatedFailedProbeDoesNotExtendTheGrace pins the bool gate.
// handleProbeResult calls spawnDaemonOnce again on every failed probe, and a
// failed probe never sets daemonSeenSinceArm, so none of those repeats may
// re-arm the deadline: only a real connection since the last arm, via
// newModel's dial or handleProbeResult's live-conn branch, does that.
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

// TestARespawnAfterARealDisconnectReArmsTheGrace is the case arm-once missed:
// a daemon this client was genuinely connected to can die long after
// startup. handleDaemonLost falls back to self-polling and, once the
// cooldown has passed, spawnDaemonOnce respawns - and that respawn must
// re-arm the grace, or the client keeps owning effects the new daemon also
// owns, which for a Done event means two cross-process CleanupSession calls
// against the same worktree.
func TestARespawnAfterARealDisconnectReArmsTheGrace(t *testing.T) {
	original := daemonSpawner
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = original })

	effects := &countingEffects{}
	m := panelThatSpawnedADaemon(effects)
	staleDeadline := m.effectsDisownedUntil

	// The client actually connected to the spawned daemon at some point
	// since the last arm - newModel's dial branch or a live
	// handleProbeResult both set this for real; it is set by hand here to
	// isolate spawnDaemonOnce's own behavior from theirs.
	m.daemonSeenSinceArm = true
	m.lastSpawn = time.Now().Add(-spawnCooldown - time.Second)

	m.spawnDaemonOnce()

	if !m.effectsDisownedUntil.After(staleDeadline) {
		t.Errorf("grace deadline did not move on a respawn after a real disconnect: got %v, want after %v",
			m.effectsDisownedUntil, staleDeadline)
	}
	if m.daemonSeenSinceArm {
		t.Error("daemonSeenSinceArm still true after a re-arm, want it cleared so the next respawn needs a fresh connection")
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

// TestANewPanelWithNoDaemonArmsTheGrace exercises the real arming path end to
// end, through newModel's own dial-then-spawn branch, rather than a fixture
// that sets effectsDisownedUntil by hand. Every test above starts from
// panelThatSpawnedADaemon, which stays green even if the arming block inside
// spawnDaemonOnce were deleted outright - nothing else in this file would
// have caught that.
func TestANewPanelWithNoDaemonArmsTheGrace(t *testing.T) {
	original := daemonSpawner
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = original })

	dir := shortTempDir(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	m := NewPanel(&config.Config{}, fetch.NewMockCommander())

	if !m.effectsDisowned() {
		t.Error("a panel that just spawned a daemon should be inside its grace period, effectsDisowned() is false")
	}
}

// TestANewDashboardDoesNotArmTheGrace is the control for the test above: a
// dashboard never spawns a daemon (newModel only calls spawnDaemonOnce when
// panel is true), so it must never own a grace deadline either.
func TestANewDashboardDoesNotArmTheGrace(t *testing.T) {
	original := daemonSpawner
	daemonSpawner = func() error { return nil }
	t.Cleanup(func() { daemonSpawner = original })

	dir := shortTempDir(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)

	m := New(&config.Config{}, fetch.NewMockCommander())

	if m.effectsDisowned() {
		t.Error("a dashboard should never disown its own effects, effectsDisowned() is true")
	}
	if !m.effectsDisownedUntil.IsZero() {
		t.Errorf("a dashboard armed a grace deadline it should never have touched: %v", m.effectsDisownedUntil)
	}
}

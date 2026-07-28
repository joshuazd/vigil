package transition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// idle and attention build sessions whose State() is unambiguous: HasBell is
// the first branch State() takes, and a nil PR is the second.
func idle(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp/" + name}
}

func attention(name string) *session.Session {
	s := idle(name)
	s.HasBell = true
	return s
}

// pending produces a session with State() = Pending (not the zero value of SessionState).
// This is used to test that new sessions are properly primed without relying on zero-value comparisons.
func pending(name string) *session.Session {
	s := idle(name)
	s.PR = &session.PRStatus{
		Number: 1,
		State:  "OPEN",
		Checks: "pending",
	}
	return s
}

func TestDetectPrimesSilentlyOnTheFirstCall(t *testing.T) {
	d := NewDetector()
	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events on the priming call, want 0", len(events))
	}
}

func TestDetectReportsOneEventPerChange(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha"), idle("beta")})

	// Create alpha with git data for the transition
	alphaWithGit := &session.Session{
		Name:     "alpha",
		PanePath: "/tmp/alpha",
		HasBell:  true,
		Git: session.GitStatus{
			Branch:  "feature/a",
			GitRoot: "/repo/alpha",
		},
	}
	events := d.Detect([]*session.Session{alphaWithGit, idle("beta")})

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Session != "alpha" {
		t.Errorf("got session %q, want alpha", ev.Session)
	}
	if ev.Old != session.Idle || ev.New != session.Attention {
		t.Errorf("got %v -> %v, want idle -> attention", ev.Old, ev.New)
	}
	if ev.PanePath != "/tmp/alpha" {
		t.Errorf("got pane path %q, want /tmp/alpha", ev.PanePath)
	}
	if ev.Branch != "feature/a" {
		t.Errorf("got branch %q, want feature/a", ev.Branch)
	}
	if ev.GitRoot != "/repo/alpha" {
		t.Errorf("got git root %q, want /repo/alpha", ev.GitRoot)
	}
}

func TestDetectIsSilentWhenNothingChanged(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{attention("alpha")})
	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events for an unchanged session, want 0", len(events))
	}
}

func TestDetectPrimesANewSessionRatherThanFiring(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha")})
	if events := d.Detect([]*session.Session{idle("alpha"), pending("beta")}); len(events) != 0 {
		t.Fatalf("got %+v, want nothing for a session seen for the first time", events)
	}
}

// TestDetectPrunesVanishedSessions is why prev is replaced rather than updated.
// Without the prune, a session that goes away and comes back in a different
// state fires an event describing a transition that never happened.
func TestDetectPrunesVanishedSessions(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha")})
	d.Detect(nil)

	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %+v, want nothing: alpha vanished, so its return is a first sighting", events)
	}
}

// TestDetectPrimesNonZeroStateWithoutSpoofing verifies that the priming works
// for sessions with non-zero states. Without the !seen guard, a mutation that
// removes it would incorrectly compare against the zero value and fail to emit
// transitions from pending to other states.
func TestDetectPrimesNonZeroStateWithoutSpoofing(t *testing.T) {
	d := NewDetector()
	if events := d.Detect([]*session.Session{pending("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events on the priming call with non-zero state, want 0", len(events))
	}
	if events := d.Detect([]*session.Session{idle("alpha")}); len(events) != 1 {
		t.Fatalf("got %d events after state change from pending to idle, want 1", len(events))
	}
}

func doneEvent(name string) Event {
	return Event{Session: name, PanePath: "/tmp/" + name, Old: session.Review, New: session.Done}
}

// TestRunSkipsCleanupForTheCurrentSession is the guard that replaces the
// model's !s.IsCurrent check. The daemon never annotates IsCurrent, so an
// Event cannot carry it and Run has to ask tmux itself. The fixture enables
// auto_cleanup and names a Done session, so cleanup is the only thing that
// could run: if the guard is gone, tmux kill-session shows up in the calls.
func TestRunSkipsCleanupForTheCurrentSession(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "alpha", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up the session the user is sitting in")
		}
	}
}

func TestRunCleansUpADoneSessionThatIsNotCurrent(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	var killed bool
	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no kill-session in %+v", cmd.Calls)
	}
}

func TestRunSkipsCleanupWhenDisabled(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{Settings: map[string]any{"notifications_enabled": "false"}}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up with auto_cleanup at its default of false")
		}
	}
}

func TestRunSkipsCleanupForANonDoneTransition(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux display-message -p #{session_name}", "beta", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	ev := Event{Session: "alpha", PanePath: "/tmp/alpha", Old: session.Idle, New: session.Blocked}
	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), ev)

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up a session that only went blocked")
		}
	}
}

// TestRunFiresTheNotifyHook asserts through the commander. RunHook invokes
// `sh -c` through fetch.Commander, so a mock records the script without any
// process running.
func TestRunFiresTheNotifyHook(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "true"},
		Hooks:    map[string]any{"notify": "notify-send {session} {new_state}"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	var script string
	for _, c := range cmd.Calls {
		if c.Name == "sh" && len(c.Args) > 1 {
			script = c.Args[1]
		}
	}
	if script == "" {
		t.Fatalf("no hook invocation in %+v", cmd.Calls)
	}
	if !strings.Contains(script, "'alpha'") {
		t.Errorf("got %q, want the session name expanded", script)
	}
	if !strings.Contains(script, "'done'") {
		t.Errorf("got %q, want the new state expanded", script)
	}
}

func TestRunSkipsTheHookWhenNotificationsAreOff(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "false"},
		Hooks:    map[string]any{"notify": "notify-send x"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "sh" {
			t.Fatalf("the hook ran with notifications disabled: %+v", c)
		}
	}
}

// TestRunDoesNotLogADeliberatelyDisabledHook covers the one path that produces
// HookNotConfigured. notify has a default template, so this is reachable only
// when a user sets notify = "" on purpose, and logging it would put a line in
// the daemon log on every transition forever.
func TestRunDoesNotLogADeliberatelyDisabledHook(t *testing.T) {
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "true"},
		Hooks:    map[string]any{"notify": ""},
	}
	var logged []string
	r := Runner{
		Cfg:  cfg,
		Cmd:  fetch.NewMockCommander(),
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	r.Run(context.Background(), doneEvent("alpha"))

	if len(logged) != 0 {
		t.Fatalf("logged %v for a hook the user disabled on purpose", logged)
	}
}

// TestRunLogsARealHookFailure is the other half: the check above must not
// swallow genuine failures, or a broken notify hook fails silently forever.
func TestRunLogsARealHookFailure(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.On("sh", "boom", errors.New("exit status 1"))
	cfg := &config.Config{
		Settings: map[string]any{"notifications_enabled": "true"},
		Hooks:    map[string]any{"notify": "definitely-not-a-command"},
	}
	var logged []string
	r := Runner{
		Cfg:  cfg,
		Cmd:  cmd,
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	r.Run(context.Background(), doneEvent("alpha"))

	if len(logged) != 1 {
		t.Fatalf("logged %v, want exactly one line for a failing hook", logged)
	}
}

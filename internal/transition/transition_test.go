package transition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

const attachedSessionsCmd = "tmux list-sessions -F #{session_name}|#{session_attached}"

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

// doneEvent's PanePath and GitRoot are placeholders, not real paths: neither
// exists on disk, so builtinCleanup's isWorktree check on PanePath is always
// false for it and no test using it reaches git worktree remove. Both fields
// are still non-empty so the malformed-event guard doesn't intercept it.
func doneEvent(name string) Event {
	return Event{
		Session:  name,
		PanePath: "/tmp/" + name,
		GitRoot:  "/repo/" + name,
		Old:      session.Review,
		New:      session.Done,
	}
}

// TestRunSkipsCleanupForAnAttachedSession is the guard against destroying a
// session anyone is sitting in. It asks fetch.AttachedSessions, not
// fetch.CurrentSession: CurrentSession resolves from TMUX_PANE, which in the
// daemon is whatever pane happened to spawn it - not a live signal once N
// attached clients are normal - and a session with any client attached must
// not be destroyed, whichever client that is.
func TestRunSkipsCleanupForAnAttachedSession(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "alpha|1", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up a session with a client attached")
		}
	}
}

// TestRunSkipsCleanupWhenTheSessionIsAbsentFromAttachedSessions is the guard
// against reading "tmux did not list this session" as "nobody is here." A
// Done event exists only because a poll saw the session, so its absence from
// AttachedSessions means AttachedSessions and reality disagree - the zero
// value for an absent map key is false, and treating that as "unattached"
// used to let cleanup proceed and force-remove a worktree, dirty or not.
func TestRunSkipsCleanupWhenTheSessionIsAbsentFromAttachedSessions(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "beta|1", nil) // alpha not listed at all
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up a session absent from AttachedSessions")
		}
	}
}

// TestRunSkipsCleanupWhenAttachedSessionsFails is the fail-closed guard.
// fetch.CurrentSession returns "" on any tmux error, and "" never equals a
// session name, so the old check read a tmux failure as "nobody is attached"
// and proceeded to force-remove a live worktree. AttachedSessions returns an
// error instead, and Run must treat that as "cannot tell, so don't touch it."
func TestRunSkipsCleanupWhenAttachedSessionsFails(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "", errors.New("tmux: no server running on /tmp/tmux-0/default"))
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}
	var logged []string
	r := Runner{
		Cfg:  cfg,
		Cmd:  cmd,
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	r.Run(context.Background(), doneEvent("alpha"))

	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
			t.Fatal("cleaned up despite being unable to tell who is attached")
		}
	}
	if len(logged) != 1 {
		t.Fatalf("logged %v, want exactly one line for the tmux failure", logged)
	}
	if !strings.Contains(logged[0], "cannot tell which sessions are attached") {
		t.Errorf("got %q, want the log to identify the tmux failure specifically, not any other skip reason", logged[0])
	}
}

// TestRunCleansUpADoneSessionThatIsNotAttached is the test that actually
// reaches git worktree remove, so PanePath points at a real directory with a
// .git file (isWorktree stats it) rather than doneEvent's placeholder. It
// asserts the exact target of both destructive calls rather than "some
// kill-session happened somewhere": two independent substring checks over a
// flattened call log can each be satisfied by a different, unrelated call.
// The fixture lists alpha itself with 0 clients attached (not a different
// session attached with alpha merely absent): that is the actual path a real
// cleanup takes, and it is the only one of the two that this test and
// TestRunSkipsCleanupWhenTheSessionIsAbsentFromAttachedSessions do not share.
func TestRunCleansUpADoneSessionThatIsNotAttached(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /repo/worktrees/alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "alpha|0", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	ev := Event{Session: "alpha", PanePath: dir, GitRoot: "/repo", Old: session.Review, New: session.Done}
	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), ev)

	var killedAlpha, removedWorktree bool
	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) == 3 &&
			c.Args[0] == "kill-session" && c.Args[1] == "-t" && c.Args[2] == "=alpha" {
			killedAlpha = true
		}
		if c.Name == "git" && len(c.Args) == 4 &&
			c.Args[0] == "worktree" && c.Args[1] == "remove" && c.Args[2] == "--force" && c.Args[3] == dir {
			removedWorktree = true
		}
	}
	if !killedAlpha {
		t.Fatalf("no exact `tmux kill-session -t =alpha` in %+v", cmd.Calls)
	}
	if !removedWorktree {
		t.Fatalf("no exact `git worktree remove --force %s` in %+v", dir, cmd.Calls)
	}
}

// TestRunDerivesATimeoutForCleanup pins the two lines around CleanupSession
// that no assertion above touches: a bare ctx has no deadline, and
// cleanupTimeout must be a real number of seconds rather than accidentally
// near-zero. MockCommander.HandlerFuncs is handed the live ctx, so a handler
// can read its deadline directly - no need for a custom Commander.
func TestRunDerivesATimeoutForCleanup(t *testing.T) {
	dir := t.TempDir()
	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "alpha|0", nil)

	var deadline time.Time
	var hasDeadline bool
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux kill-session -t =alpha": func(ctx context.Context, _ string, _ []string) (string, error) {
			deadline, hasDeadline = ctx.Deadline()
			return "", nil
		},
	}

	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}
	ev := Event{Session: "alpha", PanePath: dir, GitRoot: "/repo", Old: session.Review, New: session.Done}

	before := time.Now()
	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), ev)

	if !hasDeadline {
		t.Fatal("cleanup ran with a context that never expires - the outer ctx reached CleanupSession, not a derived one")
	}
	if d := deadline.Sub(before); d <= 10*time.Second || d > 65*time.Second {
		t.Fatalf("got a cleanup deadline %v from now, want roughly cleanupTimeout (60s)", d)
	}
}

// TestRunLogsACleanupFailure is the failure counterpart to
// TestRunCleansUpADoneSessionThatIsNotAttached: builtinCleanup's own error (a
// git worktree remove that fails) must reach the log exactly once, or a
// broken cleanup fails silently forever. Asserts the log names the failure
// specifically and that kill-session still ran - only the worktree removal
// failed, not the whole cleanup - so a mutation that skips cleanup entirely
// for the wrong reason can't produce the same one-line log by accident.
func TestRunLogsACleanupFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /repo/worktrees/alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "alpha|0", nil)
	cmd.OnArgs("git worktree remove --force "+dir, "", errors.New("worktree is dirty"))
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}
	var logged []string
	r := Runner{
		Cfg:  cfg,
		Cmd:  cmd,
		Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}

	ev := Event{Session: "alpha", PanePath: dir, GitRoot: "/repo", Old: session.Review, New: session.Done}
	r.Run(context.Background(), ev)

	if len(logged) != 1 {
		t.Fatalf("logged %v, want exactly one line for the cleanup failure", logged)
	}
	if !strings.Contains(logged[0], "auto-cleanup of alpha failed") || !strings.Contains(logged[0], "worktree is dirty") {
		t.Errorf("got %q, want the log to name the session and the underlying error", logged[0])
	}
	var killedAlpha bool
	for _, c := range cmd.Calls {
		if c.Name == "tmux" && len(c.Args) == 3 &&
			c.Args[0] == "kill-session" && c.Args[1] == "-t" && c.Args[2] == "=alpha" {
			killedAlpha = true
		}
	}
	if !killedAlpha {
		t.Fatalf("no exact `tmux kill-session -t =alpha` in %+v - the session should still be killed even though the worktree remove failed", cmd.Calls)
	}
}

// TestRunSkipsCleanupForAMalformedEvent guards against upstream garbage - an
// Event's strings come straight from tmux and git output with nothing
// upstream validating them, and tmux treats an empty target as meaningful:
// `tmux kill-session -t ''` kills a real session rather than erroring.
func TestRunSkipsCleanupForAMalformedEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"empty session", Event{Session: "", PanePath: "/tmp/x", GitRoot: "/repo/x", New: session.Done}},
		{"empty pane path", Event{Session: "alpha", PanePath: "", GitRoot: "/repo/x", New: session.Done}},
		{"empty git root", Event{Session: "alpha", PanePath: "/tmp/x", GitRoot: "", New: session.Done}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := fetch.NewMockCommander()
			cfg := &config.Config{
				Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
			}
			var logged []string
			r := Runner{
				Cfg:  cfg,
				Cmd:  cmd,
				Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
			}

			r.Run(context.Background(), tc.ev)

			for _, c := range cmd.Calls {
				if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "kill-session" {
					t.Fatal("cleaned up a malformed event")
				}
				if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "worktree" {
					t.Fatal("removed a worktree for a malformed event")
				}
			}
			if len(logged) != 1 {
				t.Fatalf("logged %v, want exactly one line for a malformed event", logged)
			}
		})
	}
}

// TestRunToleratesANilLogf is the guard on logf's nil check. Runner is
// constructed without a Logf whenever nobody cares to observe it - the
// default zero value - and a bare call must not panic.
func TestRunToleratesANilLogf(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}
	ev := Event{Session: "", PanePath: "/tmp/x", GitRoot: "/repo/x", New: session.Done}

	Runner{Cfg: cfg, Cmd: cmd}.Run(context.Background(), ev)
}

func TestRunSkipsCleanupWhenDisabled(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs(attachedSessionsCmd, "alpha|0", nil)
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
	cmd.OnArgs(attachedSessionsCmd, "alpha|0", nil)
	cfg := &config.Config{
		Settings: map[string]any{"auto_cleanup": "true", "notifications_enabled": "false"},
	}

	ev := Event{
		Session: "alpha", PanePath: "/tmp/alpha", GitRoot: "/repo/alpha",
		Old: session.Idle, New: session.Blocked,
	}
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
	if !strings.Contains(logged[0], "notify hook for alpha") || !strings.Contains(logged[0], "exit status 1") {
		t.Errorf("got %q, want the log to name the hook, the session, and the underlying error", logged[0])
	}
}

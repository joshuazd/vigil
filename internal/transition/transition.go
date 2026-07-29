// Package transition detects session state changes and runs the side effects
// they trigger. The two halves have different owners. Detection is shared:
// every TUI client runs a Detector of its own, because a toast belongs to a
// screen. The effects are the daemon's alone - Runner is constructed nowhere
// else - so that ownership is asserted rather than inferred from which process
// happens to own the poll loop at the time.
package transition

import (
	"context"
	"errors"
	"time"

	"github.com/jzinkduda/vigil/internal/action"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// Event is one session changing state. It carries only what the daemon can
// know: nothing per-tmux-client, so the same Event means the same thing
// wherever it is handled.
type Event struct {
	Session  string
	PanePath string
	Branch   string
	GitRoot  string
	Old, New session.SessionState
}

type Detector struct {
	prev map[string]session.SessionState
}

func NewDetector() *Detector {
	return &Detector{prev: make(map[string]session.SessionState)}
}

// Detect returns one Event per session whose state changed since the previous
// call. The first call primes and returns nothing (prev starts empty, so the
// !seen check catches all sessions), avoiding a storm of transitions on startup.
// A session absent from sessions is forgotten, which makes its eventual return
// a first sighting rather than a transition from whatever it was before it vanished.
func (d *Detector) Detect(sessions []*session.Session) []Event {
	next := make(map[string]session.SessionState, len(sessions))
	var events []Event
	for _, s := range sessions {
		state := s.State()
		next[s.Name] = state
		old, seen := d.prev[s.Name]
		if !seen || old == state {
			continue
		}
		events = append(events, Event{
			Session:  s.Name,
			PanePath: s.PanePath,
			Branch:   s.Git.Branch,
			GitRoot:  s.Git.GitRoot,
			Old:      old,
			New:      state,
		})
	}
	d.prev = next
	return events
}

const (
	hookTimeout    = 5 * time.Second
	cleanupTimeout = 60 * time.Second
)

// EffectRunner is the seam. config.RunHook shells out through exec rather than
// through fetch.Commander, so an interface is what lets a caller assert that an
// effect fired exactly once.
type EffectRunner interface {
	Run(ctx context.Context, ev Event)
}

// Runner performs the side effects of one transition. Only the daemon runs
// these, whether or not any client is attached to it, and a client that is
// self-polling for data does not become an owner by doing so. The consequence
// is deliberate: while no daemon is running, no notify hook fires and nothing
// is auto-cleaned. Logf is where failures go, because the daemon has no screen.
type Runner struct {
	Cfg  *config.Config
	Cmd  fetch.Commander
	Logf func(format string, args ...any)
}

func (r Runner) Run(ctx context.Context, ev Event) {
	if r.Cfg.GetSettingBool("notifications_enabled") {
		out, err := r.Cfg.RunHook(ctx, r.Cmd, "notify", map[string]string{
			"session":   ev.Session,
			"old_state": ev.Old.String(),
			"new_state": ev.New.String(),
		}, "", hookTimeout)
		if err != nil && !errors.As(err, new(*config.HookNotConfigured)) {
			r.logf("notify hook for %s: %v (output: %s)", ev.Session, err, out)
		}
	}

	if !r.Cfg.GetSettingBool("auto_cleanup") || ev.New != session.Done {
		return
	}
	if ev.Session == "" || ev.PanePath == "" || ev.GitRoot == "" {
		r.logf("auto-cleanup skipped for a malformed event: %+v", ev)
		return
	}
	// A tmux failure must be treated as "attached": it must never be read as
	// "nobody is here."
	attached, err := fetch.AttachedSessions(ctx, r.Cmd)
	if err != nil {
		r.logf("auto-cleanup of %s skipped: cannot tell which sessions are attached: %v", ev.Session, err)
		return
	}
	live, listed := attached[ev.Session]
	if !listed || live {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	out, err := action.CleanupSession(cctx, r.Cfg, r.Cmd, ev.Session, ev.PanePath, ev.Branch, ev.GitRoot)
	if err != nil {
		r.logf("auto-cleanup of %s failed: %v (output: %s)", ev.Session, err, out)
	}
}

func (r Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

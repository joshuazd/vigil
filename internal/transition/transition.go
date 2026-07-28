// Package transition detects session state changes and runs the side effects
// they trigger. Both the daemon and the TUI need this, and one of them is
// always the owner of the poll loop, so it lives here rather than in either.
package transition

import (
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

package transition

import (
	"testing"

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

func TestDetectPrimesSilentlyOnTheFirstCall(t *testing.T) {
	d := NewDetector()
	if events := d.Detect([]*session.Session{attention("alpha")}); len(events) != 0 {
		t.Fatalf("got %d events on the priming call, want 0", len(events))
	}
}

func TestDetectReportsOneEventPerChange(t *testing.T) {
	d := NewDetector()
	d.Detect([]*session.Session{idle("alpha"), idle("beta")})

	events := d.Detect([]*session.Session{attention("alpha"), idle("beta")})

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
	if events := d.Detect([]*session.Session{idle("alpha"), attention("beta")}); len(events) != 0 {
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

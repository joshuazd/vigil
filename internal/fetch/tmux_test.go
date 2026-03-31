package fetch

import (
	"context"
	"testing"
)

func TestListSessions(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|alpha|/home/alpha\n1000|alpha|/home/alpha/pane2\n999|beta|/home/beta", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	// "1000" sorts before "999" lexicographically
	if sessions[0].Name != "alpha" {
		t.Errorf("got %q, want alpha", sessions[0].Name)
	}
	if sessions[1].Name != "beta" {
		t.Errorf("got %q, want beta", sessions[1].Name)
	}
}

func TestListSessionsDeduplicates(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "1000|session1|/path1\n1000|session1|/path2", nil)

	sessions, err := ListSessions(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d, want 1 (deduplicated)", len(sessions))
	}
}

func TestCurrentSession(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "my-session", nil)

	name := CurrentSession(context.Background(), mock)
	if name != "my-session" {
		t.Errorf("got %q", name)
	}
}

func TestLastSession(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "200|other\n100|current", nil)

	last := LastSession(context.Background(), mock, "current")
	if last != "other" {
		t.Errorf("got %q, want other", last)
	}
}

func TestBellFlags(t *testing.T) {
	mock := NewMockCommander()
	mock.On("tmux", "session1|0\nsession2|1\nsession3|0", nil)

	bells := BellFlags(context.Background(), mock)
	if !bells["session2"] {
		t.Error("expected session2 to have bell")
	}
	if bells["session1"] {
		t.Error("session1 should not have bell")
	}
}

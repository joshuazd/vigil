package model

import (
	"net"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func escKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

// escModel is newTestModel plus the two things the esc path needs and the
// shared fixture does not set. cancel is nil in newTestModel, and the quit
// branch calls it - without this every test here nil-panics rather than
// failing usefully.
func escModel() Model {
	m := newTestModel()
	m.cancel = func() {}
	m.daemonWriteMu = &sync.Mutex{}
	return m
}

// isQuit reports whether a command is tea.Quit. There is no existing
// convention for this in the package; this is it. Only call it on a command
// that is safe to run - the dismiss command writes to a socket.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestEscDismissesAFailedJobInsteadOfQuitting(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobFailed}}

	next, cmd := m.Update(escKey())
	if cmd == nil {
		t.Fatal("esc produced no command; it should have sent a dismiss")
	}
	if next.(Model).hasDismissableJob() != true {
		t.Fatal("esc cleared the job locally; the daemon owns the job table")
	}

	done := make(chan *protocol.Request, 1)
	go func() {
		req, err := protocol.NewRequestDecoder(server).Next()
		if err != nil {
			done <- nil
			return
		}
		done <- req
	}()
	go cmd()

	select {
	case req := <-done:
		if req == nil {
			t.Fatal("no readable request frame reached the daemon")
			return
		}
		if req.Type != protocol.RequestDismiss {
			t.Fatalf("Type = %q, want %q", req.Type, protocol.RequestDismiss)
		}
		if req.ID != "" {
			t.Fatalf("ID = %q, want empty so an old daemon drops it silently", req.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the dismiss frame")
	}
}

func TestEscStillClearsAConfirmPromptBeforeDismissing(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.confirmAction = ConfirmCleanup
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobFailed}}

	next, cmd := m.Update(escKey())

	after := next.(Model)
	if after.confirmAction != ConfirmNone {
		t.Fatal("esc did not clear the confirm prompt first")
	}
	if cmd != nil {
		t.Fatal("esc sent a command in the same press that cleared the prompt")
	}
}

func TestEscQuitsWhenThereIsNothingToDismiss(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	m := escModel()
	m.daemonConn = client
	m.jobs = []protocol.Job{{ID: "a", State: protocol.JobRunning}}

	_, cmd := m.Update(escKey())

	if !isQuit(cmd) {
		t.Fatal("esc did not quit with only a running job present")
	}
}

func TestASelfPollingClientQuitsOnEsc(t *testing.T) {
	m := escModel()
	m.daemonConn = nil
	m.jobs = nil

	_, cmd := m.Update(escKey())

	if !isQuit(cmd) {
		t.Fatal("a client with no daemon and no jobs did not quit on esc")
	}
}

func TestHasDismissableJobCoversRefusedAndFailedOnly(t *testing.T) {
	for state, want := range map[string]bool{
		protocol.JobFailed:    true,
		protocol.JobRefused:   true,
		protocol.JobQueued:    false,
		protocol.JobRunning:   false,
		protocol.JobSucceeded: false,
	} {
		m := newTestModel()
		m.jobs = []protocol.Job{{ID: "a", State: state}}
		if got := m.hasDismissableJob(); got != want {
			t.Errorf("state %q: hasDismissableJob = %v, want %v", state, got, want)
		}
	}
}

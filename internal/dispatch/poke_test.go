package dispatch

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// TestPokeWritesOnePokeFrame is the contract the tmux hook depends on.
func TestPokeWritesOnePokeFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan *protocol.Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		req, err := protocol.NewRequestDecoder(conn).Next()
		if err != nil {
			return
		}
		got <- req
	}()

	if err := Poke(path); err != nil {
		t.Fatalf("Poke: %v", err)
	}

	select {
	case req := <-got:
		if req.Type != protocol.RequestPoke {
			t.Errorf("Type = %q, want %q", req.Type, protocol.RequestPoke)
		}
		if req.ID != "" {
			t.Errorf("ID = %q, want empty", req.ID)
		}
		if req.Version != protocol.Version {
			t.Errorf("Version = %d, want %d", req.Version, protocol.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame arrived")
	}
}

// TestPokeWithNoDaemonFailsWithoutSpawningOne matters because this runs from
// a tmux hook on every session switch. Spawning a daemon there would start
// processes behind the user's back; the error is for main to swallow.
func TestPokeWithNoDaemonFailsWithoutSpawningOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	if err := Poke(path); err == nil {
		t.Fatal("Poke returned nil with nothing listening")
	}
	if _, err := net.Listen("unix", path); err != nil {
		t.Fatalf("Poke appears to have created something at the socket path: %v", err)
	}
}

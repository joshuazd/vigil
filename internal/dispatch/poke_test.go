package dispatch

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// maxSockPath is the largest usable length for a unix socket path
// (sockaddr_un.sun_path is 104 bytes on macOS/BSD, including the null
// terminator). t.TempDir() embeds the full test function name plus a
// counter, which routinely exceeds this on macOS, so the socket lives under
// a short, fixed directory instead. Mirrors internal/daemon/daemon_test.go's
// shortTempDir; this package cannot import that test-only helper.
const maxSockPath = 103

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vigil-poke-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestPokeWritesOnePokeFrame is the contract the tmux hook depends on.
func TestPokeWritesOnePokeFrame(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "s.sock")
	if len(path) > maxSockPath {
		t.Fatalf("socket path %q is %d bytes, over the %d-byte sun_path limit", path, len(path), maxSockPath)
	}
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

// TestPokeWithNoDaemonFails checks only that Poke reports failure and leaves
// nothing at the socket path it was given. It does NOT prove Poke never
// spawns a daemon: Poke takes an explicit path, and a real spawn would bind
// protocol.SocketPath(), a value this test only pins by also redirecting
// XDG_RUNTIME_DIR to a disposable directory - so the absence of anything at
// protocol.SocketPath() here is evidence Poke does not spawn, given today's
// implementation has no spawn call at all, not proof no future change could
// add one and still pass. No-spawn is otherwise defended by review, the same
// way the tickerless remote pollers are (see protocol.go's RequestPoke
// comment). This runs from a tmux hook on every session switch: a spawn
// there would start processes behind the user's back, and any error here is
// for main to swallow, not surface.
func TestPokeWithNoDaemonFails(t *testing.T) {
	runtimeDir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	path := filepath.Join(runtimeDir, "absent.sock")
	if len(path) > maxSockPath {
		t.Fatalf("socket path %q is %d bytes, over the %d-byte sun_path limit", path, len(path), maxSockPath)
	}
	if err := Poke(path); err == nil {
		t.Fatal("Poke returned nil with nothing listening")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Poke left something at %s (stat err = %v); it must create nothing", path, err)
	}
	if _, err := os.Stat(protocol.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("Poke left something at %s (stat err = %v); it must create nothing there either", protocol.SocketPath(), err)
	}
}

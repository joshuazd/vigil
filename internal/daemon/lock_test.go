package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// holdLock takes the same flock the daemon takes, from this process but on a
// separate fd, standing in for a second daemon that is already running. flock
// is per-fd, so this genuinely blocks acquireLock.
func holdLock(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Flock: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
}

// TestRunRefusesWhenLockHeld is the case EADDRINUSE mapping cannot cover:
// there is no socket file at all, so without the lock Run would bind happily
// and two daemons would poll side by side.
func TestRunRefusesWhenLockHeld(t *testing.T) {
	s := testServer(t)
	holdLock(t, s.lockPath())

	if _, err := os.Stat(s.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("want no socket file before Run, got err %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Run(ctx)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
}

// TestRunLocksBeforeTouchingTheSocket pins the ordering. If the lock is taken
// after clearStaleSocket, the losing daemon deletes the winner's socket file
// on its way out, which is worse than the race it was meant to fix.
func TestRunLocksBeforeTouchingTheSocket(t *testing.T) {
	s := testServer(t)
	holdLock(t, s.lockPath())
	if err := writeStaleSocketFile(s.SocketPath); err != nil {
		t.Fatalf("writeStaleSocketFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
	if _, err := os.Stat(s.SocketPath); err != nil {
		t.Fatalf("Run removed the socket file it did not own: %v", err)
	}
}

// TestRunReleasesLockOnShutdown proves the release actually happens: a second
// Run on the same paths must succeed after the first returns.
func TestRunReleasesLockOnShutdown(t *testing.T) {
	s := testServer(t)
	_, stop := startServer(t, s)
	stop()

	second := testServer(t)
	second.SocketPath = s.SocketPath
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx) }()
	waitForSocket(t, second.SocketPath)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

// TestLockFileSurvivesShutdown pins that the lock file itself is not removed.
// Unlinking it lets a starting daemon create a fresh inode and lock that,
// while the running daemon still holds a lock on the old one: two daemons,
// each holding a lock, neither aware of the other.
func TestLockFileSurvivesShutdown(t *testing.T) {
	s := testServer(t)
	_, stop := startServer(t, s)
	stop()
	if _, err := os.Stat(s.lockPath()); err != nil {
		t.Fatalf("lock file gone after shutdown: %v", err)
	}
}

func TestLockPathSitsBesideTheSocket(t *testing.T) {
	s := &Server{SocketPath: filepath.Join("/tmp", "vigild.sock")}
	if got, want := s.lockPath(), "/tmp/vigild.sock.lock"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

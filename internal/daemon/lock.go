package daemon

import (
	"errors"
	"os"
	"syscall"
)

// acquireLock takes an exclusive non-blocking flock on a lock file beside the
// socket. Held across the stale-socket removal and the bind, it makes those
// two steps atomic with respect to another starting daemon: without it, two
// daemons can both find the socket stale, both unlink it, and both bind,
// leaving the first orphaned but still polling and still writing the cache.
//
// flock lives on the open file description, so the kernel drops it when the
// process dies however violently. There is nothing to clean up after a
// SIGKILL, which is the property a pidfile would not give us.
func (s *Server) acquireLock() (func(), error) {
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	// Closing the fd releases the lock. The file itself is deliberately left
	// in place: unlinking it would let the next daemon create a fresh inode
	// and lock that while this one still holds a lock on the old one.
	return func() { _ = f.Close() }, nil
}

func (s *Server) lockPath() string {
	return s.SocketPath + ".lock"
}

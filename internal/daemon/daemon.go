package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jzinkduda/vigil/internal/cache"
	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

var ErrAlreadyRunning = errors.New("daemon already running")

const defaultInterval = 1 * time.Second

type Server struct {
	Collector  *collect.Collector
	Interval   time.Duration
	SocketPath string
	CachePath  string
	Log        *log.Logger

	mu      sync.Mutex
	clients map[net.Conn]struct{}
	latest  *protocol.Snapshot

	// pollFailing is only read and written from poll, which Run only ever
	// calls from its own goroutine, so it needs no mutex.
	pollFailing bool
}

func New(cfg *config.Config, cmd fetch.Commander) *Server {
	interval := cfg.GetSettingDuration("tmux_interval")
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Server{
		Collector:  collect.New(cfg, cmd),
		Interval:   interval,
		SocketPath: protocol.SocketPath(),
		CachePath:  cache.CachePath(),
		Log:        log.New(os.Stderr, "vigil: ", log.LstdFlags),
	}
}

func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return err
	}
	release, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer release()

	if err := s.clearStaleSocket(); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return listenError(err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.SocketPath)
	}()

	s.clients = make(map[net.Conn]struct{})

	go s.accept(ctx, listener)

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			s.closeClients()
			return nil
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

// listenError translates a bind failure into ErrAlreadyRunning. clearStaleSocket
// already rejects a socket a live daemon answers on, so EADDRINUSE here means
// another daemon won a startup race between that check and the bind.
func listenError(err error) error {
	if errors.Is(err, syscall.EADDRINUSE) {
		return ErrAlreadyRunning
	}
	return err
}

// clearStaleSocket removes a socket file left behind by a dead daemon.
// A successful dial means a live daemon owns it.
func (s *Server) clearStaleSocket() error {
	info, err := os.Stat(s.SocketPath)
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket, refusing to remove it", s.SocketPath)
	}
	conn, err := net.DialTimeout("unix", s.SocketPath, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return ErrAlreadyRunning
	}
	return os.Remove(s.SocketPath)
}

func (s *Server) accept(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			_ = conn.Close()
			return
		}
		s.mu.Lock()
		s.clients[conn] = struct{}{}
		latest := s.latest
		s.mu.Unlock()

		if latest != nil {
			s.send(conn, latest)
		}
	}
}

func (s *Server) poll(ctx context.Context) {
	sessions, err := s.Collector.Snapshot(ctx)
	if err != nil {
		if !s.pollFailing {
			s.pollFailing = true
			s.logf("poll failed: %v", err)
		}
		return
	}
	if s.pollFailing {
		s.pollFailing = false
		s.logf("poll recovered")
	}
	snap := &protocol.Snapshot{
		Version:   protocol.Version,
		Timestamp: time.Now().Unix(),
		Sessions:  sessions,
	}

	s.mu.Lock()
	s.latest = snap
	conns := make([]net.Conn, 0, len(s.clients))
	for c := range s.clients {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		s.send(c, snap)
	}

	if s.CachePath != "" {
		_ = cache.Save(s.CachePath, sessions)
	}
}

// logf guards s.Log so a zero-valued Server built directly (e.g. in a test)
// does not nil-panic when logging.
func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

func (s *Server) send(conn net.Conn, snap *protocol.Snapshot) {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.Encode(conn, snap); err != nil {
		s.drop(conn)
	}
}

func (s *Server) drop(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
	_ = conn.Close()
}

func (s *Server) closeClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		_ = c.Close()
		delete(s.clients, c)
	}
}

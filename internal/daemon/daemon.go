package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/cache"
	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
)

var ErrAlreadyRunning = errors.New("daemon already running")

const defaultInterval = 3 * time.Second

type Server struct {
	Collector  *collect.Collector
	Interval   time.Duration
	SocketPath string
	CachePath  string

	mu      sync.Mutex
	clients map[net.Conn]struct{}
	latest  *protocol.Snapshot
}

func New(cfg *config.Config, cmd fetch.Commander) *Server {
	interval := cfg.GetSettingDuration("git_interval")
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Server{
		Collector:  collect.New(cfg, cmd),
		Interval:   interval,
		SocketPath: protocol.SocketPath(),
		CachePath:  cache.CachePath(),
	}
}

func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return err
	}
	if err := s.clearStaleSocket(); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
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

// clearStaleSocket removes a socket file left behind by a dead daemon.
// A successful dial means a live daemon owns it.
func (s *Server) clearStaleSocket() error {
	if _, err := os.Stat(s.SocketPath); err != nil {
		return nil
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
		return
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

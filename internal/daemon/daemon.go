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

	// mu guards latest only. clients is owned by Run's goroutine: poll,
	// addClient and broadcast all run there and nothing else touches it.
	mu     sync.Mutex
	latest *protocol.Snapshot

	clients []*client
	writers sync.WaitGroup

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
	defer func() { _ = os.Remove(s.SocketPath) }()

	// Accept only hands connections over; Run does every send, so a client
	// that never reads cannot block new connections.
	incoming := make(chan net.Conn)
	var accepted sync.WaitGroup
	accepted.Add(1)
	go func() {
		defer accepted.Done()
		s.accept(ctx, listener, incoming)
	}()

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			// Closing the listener is what unblocks accept out of Accept.
			_ = listener.Close()
			accepted.Wait()
			s.closeClients()
			return nil
		case conn := <-incoming:
			s.addClient(conn)
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

func (s *Server) accept(ctx context.Context, listener net.Listener, incoming chan<- net.Conn) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case incoming <- conn:
		case <-ctx.Done():
			_ = conn.Close()
			return
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
	s.mu.Unlock()

	s.broadcast(snap)

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

// addClient registers a connection and sends it the latest snapshot, if there
// is one. A client that connects before the first successful poll gets
// nothing until the next one, and falls back to self-polling if that takes
// too long.
func (s *Server) addClient(conn net.Conn) {
	c := newClient(conn)
	s.clients = append(s.clients, c)
	s.writers.Add(1)
	go func() {
		defer s.writers.Done()
		c.writeLoop(s.logf)
	}()

	s.mu.Lock()
	latest := s.latest
	s.mu.Unlock()
	if latest != nil {
		c.queue(latest)
	}
}

// broadcast queues snap for every live client and prunes the dead ones.
func (s *Server) broadcast(snap *protocol.Snapshot) {
	live := s.clients[:0]
	for _, c := range s.clients {
		if c.gone() {
			continue
		}
		c.queue(snap)
		live = append(live, c)
	}
	for i := len(live); i < len(s.clients); i++ {
		s.clients[i] = nil
	}
	s.clients = live
}

func (s *Server) closeClients() {
	for _, c := range s.clients {
		c.stop()
	}
	s.clients = nil
	s.writers.Wait()
}

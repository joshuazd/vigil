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
	"github.com/jzinkduda/vigil/internal/transition"
)

var ErrAlreadyRunning = errors.New("daemon already running")

const defaultInterval = 1 * time.Second

// effectDoneBuffer bounds effectDone. inFlightEffects allows at most one
// in-flight effect per session, so the number of pending sends can never
// exceed the number of distinct sessions with an effect currently running -
// nowhere near this many concurrent tmux sessions is realistic. Sized this
// generously, a send can never block, so it is safe on either side of
// pendingEffects.Done() without risking a deadlock on a missing receiver.
const effectDoneBuffer = 256

type Server struct {
	Collector  *collect.Collector
	Interval   time.Duration
	SocketPath string
	CachePath  string
	Log        *log.Logger

	// Detector and Effects fire state-transition side effects once per event.
	// Clients render their own toasts from their own detectors; only this
	// process runs the hooks and the cleanups, because only this process has
	// one view of state. Nil disables them, which is what a zero-valued Server
	// in a test gets.
	Detector *transition.Detector
	Effects  transition.EffectRunner

	// mu guards latest only. clients is owned by Run's goroutine: poll,
	// addClient and broadcast all run there and nothing else touches it.
	mu     sync.Mutex
	latest *protocol.Snapshot

	clients []*client
	writers sync.WaitGroup

	// pendingEffects tracks in-flight effect goroutines so shutdown waits for
	// them before Run returns.
	pendingEffects sync.WaitGroup

	// inFlightEffects and effectDone serialize effects per session: a bell
	// flip while a merged session's auto-cleanup is still running would
	// otherwise detect two Done events and start two CleanupSession calls
	// against the same worktree. Both are touched only from Run's goroutine -
	// poll dispatches and drains, and Run's select loop drains between polls -
	// so, like clients, neither needs a mutex. Lazily initialized so a
	// zero-valued Server built directly in a test (with Detector and Effects
	// still set) does not need to know about them.
	inFlightEffects map[string]struct{}
	effectDone      chan string

	// pollFailing is only read and written from poll, which Run only ever
	// calls from its own goroutine, so it needs no mutex.
	pollFailing bool
}

func New(cfg *config.Config, cmd fetch.Commander) *Server {
	interval := cfg.GetSettingDuration("tmux_interval")
	if interval <= 0 {
		interval = defaultInterval
	}
	logger := log.New(os.Stderr, "vigil: ", log.LstdFlags)
	return &Server{
		Collector:  collect.New(cfg, cmd),
		Interval:   interval,
		SocketPath: protocol.SocketPath(),
		CachePath:  cache.CachePath(),
		Log:        logger,
		Detector:   transition.NewDetector(),
		Effects: transition.Runner{
			Cfg:  cfg,
			Cmd:  cmd,
			Logf: logger.Printf,
		},
		inFlightEffects: make(map[string]struct{}),
		effectDone:      make(chan string, effectDoneBuffer),
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
			s.pendingEffects.Wait()
			// A completion racing the ctx.Done() case in this same select
			// may already be sitting in effectDone, unread; drain it so
			// inFlightEffects does not report it as still running. Nothing
			// depends on this once Run is returning - it's tidiness, not
			// correctness. effectDoneBuffer is what keeps the sends
			// themselves from ever blocking.
			s.drainEffectDone()
			return nil
		case conn := <-incoming:
			s.addClient(conn)
		case <-ticker.C:
			s.poll(ctx)
		case name := <-s.effectDone:
			delete(s.inFlightEffects, name)
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

	if s.Detector == nil || s.Effects == nil {
		return
	}
	if s.inFlightEffects == nil {
		s.inFlightEffects = make(map[string]struct{})
	}
	if s.effectDone == nil {
		s.effectDone = make(chan string, effectDoneBuffer)
	}
	s.drainEffectDone()
	for _, ev := range s.Detector.Detect(sessions) {
		if _, running := s.inFlightEffects[ev.Session]; running {
			s.logf("skipping effect for %s: a previous effect for this session has not finished", ev.Session)
			continue
		}
		s.inFlightEffects[ev.Session] = struct{}{}
		ev := ev
		s.pendingEffects.Add(1)
		go func() {
			defer s.pendingEffects.Done()
			s.Effects.Run(ctx, ev)
			s.effectDone <- ev.Session
		}()
	}
}

// drainEffectDone empties effectDone into inFlightEffects deletions without
// blocking. poll calls it before dispatching so a session whose effect
// finished between ticks is no longer treated as in-flight; Run's shutdown
// path calls it once more after pendingEffects.Wait() to sweep up anything
// still sitting in the buffer (see the ctx.Done() case for why that is safe
// rather than a race).
func (s *Server) drainEffectDone() {
	for {
		select {
		case name := <-s.effectDone:
			delete(s.inFlightEffects, name)
		default:
			return
		}
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

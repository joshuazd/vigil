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
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/transition"
)

var ErrAlreadyRunning = errors.New("daemon already running")

const defaultInterval = 1 * time.Second

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

	// effectsMu guards inFlightEffects, which serializes only the effect that
	// is actually destructive: a bell flip on a merged session yields two
	// New == session.Done events, and without this, both could start a
	// CleanupSession against the same worktree concurrently. Every other
	// transition dispatches ungated so the notify hook fires; Done transitions
	// are gated, and a repeat Done during cleanup is skipped entirely. A
	// dedicated mutex rather than mu: mu is about a different piece of state
	// (latest), and the dispatching goroutine, not just poll, needs to take
	// this one (to delete on completion).
	effectsMu       sync.Mutex
	inFlightEffects map[string]struct{}

	// pollFailing is only read and written from poll, which Run only ever
	// calls from its own goroutine, so it needs no mutex.
	pollFailing bool

	// jobs is the dispatch queue. Nil disables submission, which is what a
	// Server literal in a test gets unless it builds one.
	jobs *jobs

	// requests carries client submissions to Run's goroutine, which owns the
	// job table's only writer besides a running job.
	requests chan *protocol.Request
}

func New(cfg *config.Config, cmd fetch.Commander) *Server {
	interval := cfg.GetSettingDuration("tmux_interval")
	if interval <= 0 {
		interval = defaultInterval
	}
	logger := log.New(os.Stderr, "vigil: ", log.LstdFlags)
	srv := &Server{
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
		requests:        make(chan *protocol.Request, queueDepth),
	}
	// The job table exists even when the commander cannot stream. It refuses
	// every submission in that case, which is the point: a nil table left the
	// request read off the wire and dropped with nothing registered, and a
	// silent drop is the one outcome the whole refusal mechanism exists to
	// eliminate - it is indistinguishable, from the client, from a daemon
	// that never read the frame.
	stream, _ := cmd.(fetch.StreamCommander)
	srv.jobs = newJobs(cfg, stream, cmd, logger.Printf)
	return srv
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

	if s.jobs != nil {
		s.pendingEffects.Add(1)
		go func() {
			defer s.pendingEffects.Done()
			s.jobs.work(ctx)
		}()
	}

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
			return nil
		case conn := <-incoming:
			s.addClient(ctx, conn)
		case req := <-s.requests:
			s.handleRequest(req)
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
	// Taken before the collector's error return: snapshot() prunes expired
	// jobs as a side effect, and that pruning must not stall for as long as
	// collection is failing.
	var jobList []protocol.Job
	if s.jobs != nil {
		jobList = s.jobs.snapshot()
	}

	sessions, err := s.Collector.Snapshot(ctx)
	if err != nil {
		if !s.pollFailing {
			s.pollFailing = true
			s.logf("poll failed: %v", err)
		}
		// Publication belongs above this return for the same reason the
		// pruning does. A failing collector says nothing about the jobs: with
		// no snapshot going out, no panel shows a job line and every
		// vigil dispatch times out unacknowledged for as long as collection
		// is broken.
		s.publishJobs(jobList)
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
		Jobs:      jobList,
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
	for _, ev := range s.Detector.Detect(sessions) {
		ev := ev
		if ev.New != session.Done {
			// Every other transition dispatches ungated: the notify hook
			// must fire once per real transition, and only cleanup is
			// destructive enough to need serializing.
			s.pendingEffects.Add(1)
			go func() {
				defer s.pendingEffects.Done()
				s.Effects.Run(ctx, ev)
			}()
			continue
		}

		s.effectsMu.Lock()
		if s.inFlightEffects == nil {
			s.inFlightEffects = make(map[string]struct{})
		}
		if _, running := s.inFlightEffects[ev.Session]; running {
			s.effectsMu.Unlock()
			s.logf("transition effects for %s still running, skipping a repeat Done", ev.Session)
			continue
		}
		s.inFlightEffects[ev.Session] = struct{}{}
		s.effectsMu.Unlock()

		s.pendingEffects.Add(1)
		go func() {
			defer s.pendingEffects.Done()
			s.Effects.Run(ctx, ev)
			s.effectsMu.Lock()
			delete(s.inFlightEffects, ev.Session)
			s.effectsMu.Unlock()
		}()
	}
}

// publishJobs re-broadcasts the latest snapshot with jobs attached, off the
// tick. Called when a submission is accepted and when a poll fails, the two
// moments a job's state changes without a snapshot of its own.
//
// It never invents a snapshot. A frame with nil Sessions would blank every
// client's table, which is a far worse outcome than a job line arriving one
// tick late, so before the first successful poll this does nothing at all and
// the submitting client waits.
//
// The timestamp is deliberately carried over rather than refreshed: it is
// what the status bar's "daemon stale Ns" reads, and these sessions are
// exactly as old as they were. Refreshing it would make a failing collector
// look healthy.
//
// Run's goroutine is the only caller, which is what makes touching clients
// (through broadcast) safe.
func (s *Server) publishJobs(jobs []protocol.Job) {
	s.mu.Lock()
	latest := s.latest
	s.mu.Unlock()
	if latest == nil {
		return
	}
	updated := *latest
	updated.Jobs = jobs

	s.mu.Lock()
	s.latest = &updated
	s.mu.Unlock()

	s.broadcast(&updated)
}

// handleRequest routes one client frame. The default arm stays submit rather
// than becoming a refusal: submit's reason switch already produces the
// unsupported-type refusal, and that behaviour must not move.
func (s *Server) handleRequest(req *protocol.Request) {
	if s.jobs == nil || req == nil {
		return
	}
	switch req.Type {
	case protocol.RequestDismiss:
		if !s.jobs.dismissTerminal() {
			return
		}
	default:
		s.jobs.submit(req)
	}
	// Immediately, not on the next tick. The submitting CLI waits to see its
	// id in a snapshot, and on a cold daemon the next tick is behind a first
	// poll that runs git and gh across every session.
	s.publishJobs(s.jobs.snapshot())
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
func (s *Server) addClient(ctx context.Context, conn net.Conn) {
	c := newClient(conn)
	s.clients = append(s.clients, c)
	s.writers.Add(1)
	go func() {
		defer s.writers.Done()
		c.writeLoop(s.logf)
	}()
	if s.requests != nil {
		go c.readLoop(ctx, s.requests, s.logf)
	}

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

package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/transition"
)

type recordingEffects struct {
	mu     sync.Mutex
	events []transition.Event
}

func (r *recordingEffects) Run(_ context.Context, ev transition.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEffects) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// bellSwitch returns a Commander whose bell flag flips the second time tmux is
// asked, which is a state change from idle to attention.
func bellSwitch() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	var windowCalls int
	var mu sync.Mutex
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"tmux": func(_ context.Context, _ string, args []string) (string, error) {
			if len(args) > 1 && args[1] == "-a" && args[0] == "list-panes" {
				return "1700000000|alpha|/tmp/alpha", nil
			}
			if len(args) > 0 && args[0] == "list-windows" {
				mu.Lock()
				windowCalls++
				n := windowCalls
				mu.Unlock()
				if n == 1 {
					return "alpha|0", nil
				}
				return "alpha|1", nil
			}
			return "", nil
		},
	}
	cmd.On("git", "", nil)
	cmd.On("gh", "", nil)
	return cmd
}

// TestPollRunsEffectsOncePerEvent is the point of moving them here: the count
// is a property of the event, not of how many clients happen to be attached.
// It also proves the fixture is capable of producing an effect at all, which
// is what makes TestEffectsDoNotScaleWithClients's "still exactly one" a
// meaningful assertion rather than a vacuous "still zero."
func TestPollRunsEffectsOncePerEvent(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	ctx := context.Background()
	s.poll(ctx)
	if got := effects.count(); got != 0 {
		t.Fatalf("got %d effect runs on the priming poll, want 0", got)
	}
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs for one transition, want 1", got)
	}
	if ev := effects.events[0]; ev.Session != "alpha" {
		t.Errorf("got session %q, want alpha", ev.Session)
	}
}

func TestPollWithoutADetectorDoesNotPanic(t *testing.T) {
	s := &Server{Collector: collect.New(&config.Config{}, bellSwitch())}
	s.poll(context.Background())
}

// TestEffectsDoNotScaleWithClients pins the property phase 3 depends on: three
// panels attached to one daemon still produce one effect run per event.
// newBlockingConn (client_test.go) is reused here rather than wiring net.Pipe
// by hand: closing release up front makes its Write a no-op that still
// exercises addClient and the writer goroutine that poll's broadcast drives.
func TestEffectsDoNotScaleWithClients(t *testing.T) {
	cmd := bellSwitch()
	effects := &recordingEffects{}
	s := &Server{
		Collector: collect.New(&config.Config{}, cmd),
		Detector:  transition.NewDetector(),
		Effects:   effects,
	}

	for i := 0; i < 3; i++ {
		conn := newBlockingConn()
		close(conn.release)
		t.Cleanup(func() { _ = conn.Close() })
		s.addClient(conn)
	}

	ctx := context.Background()
	s.poll(ctx)
	s.poll(ctx)
	s.pendingEffects.Wait()

	if got := effects.count(); got != 1 {
		t.Fatalf("got %d effect runs with three clients attached, want 1", got)
	}
	s.closeClients()
}

// blockingEffectRunner signals started, then blocks until release is closed,
// standing in for a notify hook that has not returned yet.
type blockingEffectRunner struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingEffectRunner) Run(context.Context, transition.Event) {
	close(b.started)
	<-b.release
}

// TestRunWaitsForPendingEffects pins the shutdown handshake for effect
// goroutines: Run must not return while a hook or cleanup it started is still
// in flight, the same guarantee TestRunWaitsForWriters pins for writers.
func TestRunWaitsForPendingEffects(t *testing.T) {
	cmd := bellSwitch()
	started := make(chan struct{})
	release := make(chan struct{})
	effects := &blockingEffectRunner{started: started, release: release}

	sockPath := filepath.Join(shortTempDir(t), "test.sock")
	s := &Server{
		Collector:  collect.New(&config.Config{}, cmd),
		Interval:   5 * time.Millisecond,
		SocketPath: sockPath,
		CachePath:  filepath.Join(t.TempDir(), "cache.json"),
		Detector:   transition.NewDetector(),
		Effects:    effects,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	waitForSocket(t, s.SocketPath)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("effect never started")
	}

	cancel()

	select {
	case <-done:
		t.Fatal("Run returned before its in-flight effect finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the effect finished")
	}
}

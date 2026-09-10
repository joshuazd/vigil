package collect

import (
	"context"
	"sync"
)

// A poller owns one class of data fetched off the publication path: its own
// store, its own locking, and its own idea of what is due. Nothing it does can
// block Snapshot.
//
// pass runs one fetch cycle and returns immediately when nothing is due.
// invalidate drops due-ness so the next pass refetches.
type poller interface {
	pass(ctx context.Context)
	invalidate()
}

// scheduler runs pollers, one goroutine each so a slow poller cannot delay
// another.
//
// It has no ticker, and that is load-bearing rather than a simplification.
// Cadence comes from whoever calls Snapshot, which nudges at the end of every
// call. A daemon-fed client never calls Snapshot - startPoll refuses while a
// daemon is connected - so its workers block for the life of the process and
// spend nothing. On the remote scheduler that is what "one daemon means one gh
// budget" rests on. On the local one it is what stops every open panel running
// its own git status loop against the monorepo, which is the worse of the two
// because no rate limit would ever notice it happening.
type scheduler struct {
	pollers []poller
	wakes   []chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
}

func newScheduler(pollers ...poller) *scheduler {
	s := &scheduler{pollers: pollers}
	for range pollers {
		s.wakes = append(s.wakes, make(chan struct{}, 1))
	}
	return s
}

// start is idempotent rather than fatal on a second call: a client that loses
// and regains a daemon can reach it more than once, and a second set of
// workers would double the fetch rate for one collector. sync.Once, not a
// bool: that path is reachable from more than one goroutine, and an
// unsynchronized bool would race the read/write on itself.
//
// The Once makes ctx sticky: the first caller's context is the only one the
// workers ever select on, so it must outlive the process's use of this
// scheduler. Starting with an already-cancelled one leaves the workers
// permanently stopped and every later start a no-op.
func (s *scheduler) start(ctx context.Context) {
	s.once.Do(func() {
		for i, p := range s.pollers {
			p, wake := p, s.wakes[i]
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-wake:
					}
					p.pass(ctx)
				}
			}()
		}
	})
}

func (s *scheduler) wait() { s.wg.Wait() }

// nudge wakes every worker without blocking. The channels are cap-1, so a
// nudge that arrives while a pass is running coalesces into the one already
// queued and the worker re-checks the moment it finishes.
func (s *scheduler) nudge() {
	for _, wake := range s.wakes {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *scheduler) invalidate() {
	for _, p := range s.pollers {
		p.invalidate()
	}
}

// refresh runs one pass of every poller on the caller's goroutine. The workers
// are a scheduler over this, and it is the seam a test drives so it never has
// to race a goroutine. Production reaches a pass only through start.
func (s *scheduler) refresh(ctx context.Context) {
	for _, p := range s.pollers {
		p.pass(ctx)
	}
}

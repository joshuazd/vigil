package collect

import (
	"context"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// queueStore is the half of a queue poller that is identical between the two.
// Unlike prPoller there is no track/fill: these are global lists, not
// per-branch data grafted onto sessions, so there is no working set to post
// and nothing to write onto a session.
type queueStore struct {
	// passMu makes a pass single-flight. The scheduler gives one goroutine
	// per poller, but refresh can be called from another, and two concurrent
	// passes would spend two subprocesses for one result.
	passMu sync.Mutex

	mu        sync.Mutex
	items     []session.QueueItem
	fetchedAt time.Time
	gen       uint64
}

func (s *queueStore) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.fetchedAt = time.Time{}
}

func (s *queueStore) list() []session.QueueItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.QueueItem(nil), s.items...)
}

func (s *queueStore) begin(now time.Time, interval time.Duration) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fetchedAt.IsZero() && now.Sub(s.fetchedAt) < interval {
		return s.gen, false
	}
	return s.gen, true
}

// commit writes a completed fetch back. A failed fetch keeps the last known
// list rather than blanking the section, matching prPoller, but still advances
// fetchedAt so a rate-limited subprocess is not retried on every nudge.
//
// If gen moved, an invalidate landed while the fetch was in flight and its
// answer may predate it, so the entry stays due.
func (s *queueStore) commit(startGen uint64, now time.Time, items []session.QueueItem, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.items = items
	}
	if s.gen != startGen {
		s.fetchedAt = time.Time{}
		return
	}
	s.fetchedAt = now
}

type reviewPoller struct {
	c *Collector
	queueStore
}

func newReviewPoller(c *Collector) *reviewPoller { return &reviewPoller{c: c} }

func (p *reviewPoller) pass(ctx context.Context) {
	p.passMu.Lock()
	defer p.passMu.Unlock()

	now := p.c.now()
	startGen, due := p.begin(now, p.c.QueueInterval)
	if !due {
		return
	}
	items, err := fetch.SearchReviewRequests(ctx, p.c.Cmd, p.c.QueuePRQuery, p.c.QueuePRAgeDays, p.c.QueueLimit, now)
	p.commit(startGen, now, items, err)
}

type storyPoller struct {
	c *Collector
	queueStore
}

func newStoryPoller(c *Collector) *storyPoller { return &storyPoller{c: c} }

func (p *storyPoller) pass(ctx context.Context) {
	p.passMu.Lock()
	defer p.passMu.Unlock()

	now := p.c.now()
	startGen, due := p.begin(now, p.c.QueueInterval)
	if !due {
		return
	}
	items, err := fetch.SearchStories(ctx, p.c.Cmd, p.c.QueueStoryQuery, p.c.QueueLimit)
	p.commit(startGen, now, items, err)
}

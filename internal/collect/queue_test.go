package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// queueCommander answers both queue subprocesses and nothing else.
func queueCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.On("gh", `[{"number":34967,"repository":{"name":"portal"},"title":"Timeline tab",
		"updatedAt":"2026-07-31T18:54:14Z","url":"https://github.com/huntresslabs/portal/pull/34967"}]`, nil)
	cmd.On("short", `{"data":[{"id":223480,"name":"Backfill audit rows",
		"app_url":"https://app.shortcut.com/huntress/story/223480","updated_at":"2026-07-31T10:00:00Z"}]}`, nil)
	return cmd
}

func TestQueuePollersFetchOnFirstPass(t *testing.T) {
	cmd := queueCommander()
	c := New(&config.Config{}, cmd)

	c.RefreshRemote(context.Background())

	if got := len(c.reviews.list()); got != 1 {
		t.Errorf("reviews: got %d items, want 1", got)
	}
	if got := len(c.stories.list()); got != 1 {
		t.Errorf("stories: got %d items, want 1", got)
	}
}

func TestQueuePollerSkipsWithinInterval(t *testing.T) {
	cmd := queueCommander()
	c := New(&config.Config{}, cmd)
	now := time.Unix(1700000000, 0)
	c.clock = func() time.Time { return now }

	c.RefreshRemote(context.Background())
	first := cmd.CallCount("gh")

	now = now.Add(10 * time.Second) // < queue_interval (60s)
	c.RefreshRemote(context.Background())

	if got := cmd.CallCount("gh"); got != first {
		t.Errorf("gh called %d times, want %d: a pass inside queue_interval must not fetch", got, first)
	}
}

func TestQueuePollerFetchesAgainAfterInterval(t *testing.T) {
	cmd := queueCommander()
	c := New(&config.Config{}, cmd)
	now := time.Unix(1700000000, 0)
	c.clock = func() time.Time { return now }

	c.RefreshRemote(context.Background())
	first := cmd.CallCount("gh")

	now = now.Add(61 * time.Second)
	c.RefreshRemote(context.Background())

	if got := cmd.CallCount("gh"); got <= first {
		t.Errorf("gh called %d times, want more than %d after the interval elapsed", got, first)
	}
}

// TestQueuePollerInvalidateMakesItDueImmediately goes through Collector.Invalidate
// rather than the poller's own method, because the wiring is the thing under
// test: remote.invalidate iterates r.pollers, so a poller left out of newRemote
// would still pass a test that called p.invalidate() directly.
func TestQueuePollerInvalidateMakesItDueImmediately(t *testing.T) {
	cmd := queueCommander()
	c := New(&config.Config{}, cmd)
	now := time.Unix(1700000000, 0)
	c.clock = func() time.Time { return now }

	c.RefreshRemote(context.Background())
	ghFirst, shortFirst := cmd.CallCount("gh"), cmd.CallCount("short")

	c.Invalidate()
	c.RefreshRemote(context.Background())

	if got := cmd.CallCount("gh"); got <= ghFirst {
		t.Errorf("gh called %d times, want more than %d after Invalidate", got, ghFirst)
	}
	if got := cmd.CallCount("short"); got <= shortFirst {
		t.Errorf("short called %d times, want more than %d after Invalidate", got, shortFirst)
	}
}

// TestQueuePollerKeepsLastListOnFailure mirrors prPoller: a failed fetch must
// not blank the section, and fetchedAt must still advance so a rate-limited gh
// is not retried on every nudge.
func TestQueuePollerKeepsLastListOnFailure(t *testing.T) {
	cmd := queueCommander()
	c := New(&config.Config{}, cmd)
	now := time.Unix(1700000000, 0)
	c.clock = func() time.Time { return now }

	c.RefreshRemote(context.Background())
	if len(c.reviews.list()) != 1 {
		t.Fatalf("setup: want 1 review, got %d", len(c.reviews.list()))
	}

	cmd.On("gh", "", errors.New("rate limited"))
	now = now.Add(61 * time.Second)
	c.RefreshRemote(context.Background())

	if got := len(c.reviews.list()); got != 1 {
		t.Errorf("after a failed fetch: got %d items, want the last known 1", got)
	}

	failedAt := cmd.CallCount("gh")
	now = now.Add(10 * time.Second)
	c.RefreshRemote(context.Background())
	if got := cmd.CallCount("gh"); got != failedAt {
		t.Errorf("gh called %d times, want %d: a failed fetch must still advance fetchedAt", got, failedAt)
	}
}

// TestQueueDisabledConstructsNoPollers is an absence assertion, so it carries
// its own positive control: the same fixture with the setting on must call
// both subprocesses, or a drift in MockCommander's key resolution would make
// the "off" half pass for the wrong reason.
func TestQueueDisabledConstructsNoPollers(t *testing.T) {
	on := queueCommander()
	cOn := New(&config.Config{}, on)
	cOn.RefreshRemote(context.Background())
	if on.CallCount("gh") == 0 || on.CallCount("short") == 0 {
		t.Fatalf("positive control failed: gh=%d short=%d, want both > 0",
			on.CallCount("gh"), on.CallCount("short"))
	}

	off := queueCommander()
	cOff := New(&config.Config{Settings: map[string]any{"queue_enabled": "false"}}, off)
	if cOff.reviews != nil || cOff.stories != nil {
		t.Error("queue_enabled=false must construct no pollers at all")
	}
	cOff.RefreshRemote(context.Background())
	if got := off.CallCount("gh"); got != 0 {
		t.Errorf("gh called %d times with queue_enabled=false, want 0", got)
	}
	if got := off.CallCount("short"); got != 0 {
		t.Errorf("short called %d times with queue_enabled=false, want 0", got)
	}
}

func sortTestCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	// Two stories and two reviews with UpdatedAt values interleaved across kinds.
	// Story B is newest among stories but second-newest overall.
	// Review C is newest overall. Story A is oldest. Review D is second-oldest.
	// Correct order (Kind, then UpdatedAt descending): B, A, C, D.
	cmd.On("gh", `[{"number":34967,"repository":{"name":"portal"},"title":"Timeline tab",
		"updatedAt":"2026-07-31T20:00:00Z","url":"https://github.com/huntresslabs/portal/pull/34967"},
		{"number":34966,"repository":{"name":"portal"},"title":"Date picker",
		"updatedAt":"2026-07-31T14:00:00Z","url":"https://github.com/huntresslabs/portal/pull/34966"}]`, nil)
	cmd.On("short", `{"data":[{"id":223480,"name":"Backfill audit rows",
		"app_url":"https://app.shortcut.com/huntress/story/223480","updated_at":"2026-07-31T16:00:00Z"},
		{"id":223479,"name":"Add caching","app_url":"https://app.shortcut.com/huntress/story/223479",
		"updated_at":"2026-07-31T08:00:00Z"}]}`, nil)
	return cmd
}

func seededCollector(t *testing.T) *Collector {
	t.Helper()
	c := New(&config.Config{}, queueCommander())
	c.RefreshRemote(context.Background())
	return c
}

func TestQueueReturnsBothKinds(t *testing.T) {
	c := seededCollector(t)

	items, hidden := c.Queue(nil)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if hidden != 0 {
		t.Errorf("hidden = %d, want 0", hidden)
	}
}

// TestQueueSortsByKindThenUpdatedAt verifies sort order: stories first, then by
// UpdatedAt descending. With interleaved timestamps across kinds, a broken
// Kind branch or a missing UpdatedAt comparator produces a visibly different
// sequence.
func TestQueueSortsByKindThenUpdatedAt(t *testing.T) {
	cmd := sortTestCommander()
	c := New(&config.Config{}, cmd)
	c.RefreshRemote(context.Background())

	items, hidden := c.Queue(nil)
	if hidden != 0 {
		t.Errorf("hidden = %d, want 0", hidden)
	}

	// Expected order: story 223480 (B, 16:00), story 223479 (A, 08:00),
	// review 34967 (C, 20:00), review 34966 (D, 14:00).
	// A broken Kind branch (sorts purely by UpdatedAt) gives: 34967, 223480, 34966, 223479.
	expected := []string{"sc-223480", "sc-223479", "portal#34967", "portal#34966"}
	if len(items) != len(expected) {
		t.Fatalf("got %d items, want %d", len(items), len(expected))
	}
	for i, exp := range expected {
		if items[i].Label() != exp {
			t.Errorf("items[%d].Label() = %q, want %q", i, items[i].Label(), exp)
		}
	}
}

// TestQueueHidesItemsWithASessionByName covers the primary dedup key on its
// own: a review whose session exists but whose PR data has not been fetched
// yet is still hidden.
func TestQueueHidesItemsWithASessionByName(t *testing.T) {
	c := seededCollector(t)
	sessions := []*session.Session{
		{Name: "PR-34967 Timeline tab"},
		{Name: "SC-223480 Backfill audit rows"},
	}

	items, hidden := c.Queue(sessions)
	if len(items) != 0 {
		t.Errorf("got %d items, want 0: both have sessions", len(items))
	}
	if hidden != 2 {
		t.Errorf("hidden = %d, want 2", hidden)
	}
}

// TestQueueHidesReviewsByPRNumber covers the secondary key on its own. The
// session is named nothing like the convention, so only the PR.Number match
// can hide it. Tested separately from the name key so neither can carry the
// other: if the name rule silently stopped working, this test would still
// pass and the previous one would fail, which is the point.
func TestQueueHidesReviewsByPRNumber(t *testing.T) {
	c := seededCollector(t)
	sessions := []*session.Session{
		{Name: "some-unrelated-name", PR: &session.PRStatus{Number: 34967}},
	}

	items, hidden := c.Queue(sessions)
	if hidden != 1 {
		t.Errorf("hidden = %d, want 1", hidden)
	}
	for _, it := range items {
		if it.Kind == session.QueueReview {
			t.Errorf("review %s survived a session whose PR.Number matches", it.Label())
		}
	}
}

func TestQueueAppliesTheLimit(t *testing.T) {
	cmd := sortTestCommander()
	c := New(&config.Config{}, cmd)
	c.RefreshRemote(context.Background())
	c.QueueLimit = 1

	items, _ := c.Queue(nil)
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
	}
	// The first item after sorting must be story 223480, not any other item.
	if items[0].Label() != "sc-223480" {
		t.Errorf("items[0].Label() = %q, want sc-223480 (the first after sorting)", items[0].Label())
	}
}

func TestQueueIsNilWhenDisabled(t *testing.T) {
	c := New(&config.Config{Settings: map[string]any{"queue_enabled": "false"}}, queueCommander())

	items, hidden := c.Queue(nil)
	if items != nil {
		t.Errorf("items = %v, want nil", items)
	}
	if hidden != 0 {
		t.Errorf("hidden = %d, want 0", hidden)
	}
}

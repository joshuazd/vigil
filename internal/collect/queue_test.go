package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
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

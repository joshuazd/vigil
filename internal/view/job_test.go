package view

import (
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/protocol"
)

func TestRenderJobLineShowsInputAndStatus(t *testing.T) {
	got := RenderJobLine([]protocol.Job{{
		ID: "a", Input: "sc-12345", State: protocol.JobRunning,
		Status: "classifying story for model routing",
	}}, 80)
	if !strings.Contains(got, "sc-12345") {
		t.Errorf("got %q, want the input", got)
	}
	if !strings.Contains(got, "classifying story for model routing") {
		t.Errorf("got %q, want the status", got)
	}
}

func TestRenderJobLineIsEmptyWithNoJobs(t *testing.T) {
	if got := RenderJobLine(nil, 80); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRenderJobLineFitsTheWidth(t *testing.T) {
	got := RenderJobLine([]protocol.Job{{
		ID: "a", Input: "https://app.shortcut.com/workspace/story/12345/a-long-title",
		State: protocol.JobRunning, Status: "creating the worktree and the tmux session",
	}}, 40)
	for _, line := range strings.Split(got, "\n") {
		if w := visibleLen(line); w > 40 {
			t.Errorf("line is %d wide, want <= 40: %q", w, line)
		}
	}
}

func TestRenderJobLineShowsAQueuedJobsPosition(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "sc-1", State: protocol.JobRunning, Status: "working"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 80)
	if !strings.Contains(got, "+1") {
		t.Errorf("got %q, want a queued count", got)
	}
}

func TestRenderJobLinePrefersAFailureOverAQueuedJob(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "sc-1", State: protocol.JobFailed, Status: "no branch for story 1"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 80)
	if !strings.Contains(got, "no branch for story 1") {
		t.Errorf("got %q, want the failure reason", got)
	}
}

// JobRefused was added during Task 7: an accepted job that fails at runtime and
// a submission the daemon never accepted are different things, and conflating
// them made `vigil dispatch` exit non-zero for work it had actually started.
// The distinction is real on the wire, but it is not a distinction a glance at a
// panel needs - both are "this did not happen, here is why" - so it renders the
// same as a failure and outranks a queued job the same way.
func TestRenderJobLineTreatsARefusalLikeAFailure(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "sc-1", State: protocol.JobRefused, Status: "duplicate of an in-flight dispatch"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 80)
	if !strings.Contains(got, "duplicate of an in-flight dispatch") {
		t.Errorf("got %q, want the refusal reason", got)
	}
}

// TestRenderJobLineKeepsTheQueuedCountWhenTheInputIsLong pins the decision on
// what gives way first when a job line does not fit: a Shortcut URL alone
// already exceeds a 40-column panel, so the marker, input, and queued count
// - what a glance needs - must survive, and the status - a free-text
// progress note, the most expendable of the three - is what gets dropped.
func TestRenderJobLineKeepsTheQueuedCountWhenTheInputIsLong(t *testing.T) {
	got := RenderJobLine([]protocol.Job{
		{ID: "a", Input: "https://app.shortcut.com/workspace/story/12345/a-long-title",
			State: protocol.JobRunning, Status: "creating the worktree and the tmux session"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}, 40)
	if !strings.Contains(got, "+1") {
		t.Errorf("got %q, want the queued count to survive truncation", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := visibleLen(line); w > 40 {
			t.Errorf("line is %d wide, want <= 40: %q", w, line)
		}
	}
}

// TestRenderJobLineNeverExceedsWidth sweeps every width from 1 to 40 with a
// long input and a queued job present - the combination that reservation
// alone cannot keep in bounds once width drops below the suffix's own
// width (tierBare, a real frozen tier, is 4 columns). The reservation makes
// the suffix survive; this is the separate guarantee that the line itself
// never overflows the tier it is rendered into.
func TestRenderJobLineNeverExceedsWidth(t *testing.T) {
	jobs := []protocol.Job{
		{ID: "a", Input: "https://app.shortcut.com/workspace/story/12345/a-long-title",
			State: protocol.JobRunning, Status: "creating the worktree and the tmux session"},
		{ID: "b", Input: "sc-2", State: protocol.JobQueued},
	}
	for width := 1; width <= 40; width++ {
		got := RenderJobLine(jobs, width)
		for _, line := range strings.Split(got, "\n") {
			if w := visibleLen(line); w > width {
				t.Errorf("width %d: line is %d wide, want <= %d: %q", width, w, width, line)
			}
		}
	}
}

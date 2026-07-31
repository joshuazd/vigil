package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

const ghSearchOutput = `[
  {"number":34967,"repository":{"name":"portal","nameWithOwner":"huntresslabs/portal"},
   "title":"Partner facing incident report Timeline tab",
   "updatedAt":"2026-07-31T18:54:14Z",
   "url":"https://github.com/huntresslabs/portal/pull/34967"},
  {"number":205,"repository":{"name":"soc-workflows","nameWithOwner":"huntresslabs/soc-workflows"},
   "title":"Resurrect test_variety_of_tasks",
   "updatedAt":"2026-07-09T10:00:00Z",
   "url":"https://github.com/huntresslabs/soc-workflows/pull/205"}
]`

func TestSearchReviewRequestsParsesItems(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", ghSearchOutput, nil)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	items, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me", 14, 20, now)
	if err != nil {
		t.Fatalf("SearchReviewRequests: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	want := session.QueueItem{
		Kind:      session.QueueReview,
		ID:        "34967",
		Title:     "Partner facing incident report Timeline tab",
		Input:     "https://github.com/huntresslabs/portal/pull/34967",
		Repo:      "portal",
		UpdatedAt: time.Date(2026, 7, 31, 18, 54, 14, 0, time.UTC).Unix(),
	}
	if items[0] != want {
		t.Errorf("items[0] = %+v, want %+v", items[0], want)
	}
	if items[1].Repo != "soc-workflows" {
		t.Errorf("items[1].Repo = %q, want soc-workflows", items[1].Repo)
	}
}

// TestSearchReviewRequestsArgvPutsQueryAfterDoubleDash is the whole point of
// this wrapper. `gh search prs "review-requested:@me" "-is:draft"` fails with
// `unknown shorthand flag: 'i' in -is:draft`, because cobra parses the leading
// dash as a flag. Every gh flag must precede `--` and every config-supplied
// token must follow it. Verified against real gh on 2026-07-31.
func TestSearchReviewRequestsArgvPutsQueryAfterDoubleDash(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", "[]", nil)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me -is:draft", 14, 20, now); err != nil {
		t.Fatalf("SearchReviewRequests: %v", err)
	}
	if len(cmd.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(cmd.Calls))
	}
	args := cmd.Calls[0].Args

	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		t.Fatalf("argv has no -- separator: %v", args)
	}
	for _, a := range args[:sep] {
		if a == "-is:draft" || a == "review-requested:@me" {
			t.Errorf("query token %q appears before --: %v", a, args)
		}
	}
	after := strings.Join(args[sep+1:], " ")
	if !strings.Contains(after, "-is:draft") {
		t.Errorf("-is:draft missing after --: %v", args)
	}
	if !strings.Contains(after, "review-requested:@me") {
		t.Errorf("review-requested:@me missing after --: %v", args)
	}
	// Fixed flags must precede the separator, not follow it.
	before := strings.Join(args[:sep], " ")
	if !strings.Contains(before, "--state=open") {
		t.Errorf("--state=open missing before --: %v", args)
	}
	if !strings.Contains(before, "--limit") {
		t.Errorf("--limit missing before --: %v", args)
	}
	if !strings.Contains(before, "--json") {
		t.Errorf("--json missing before --: %v", args)
	}
}

func TestSearchReviewRequestsAppendsAgeWindow(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", "[]", nil)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me", 14, 20, now); err != nil {
		t.Fatalf("SearchReviewRequests: %v", err)
	}
	joined := strings.Join(cmd.Calls[0].Args, " ")
	if !strings.Contains(joined, "updated:>=2026-07-17") {
		t.Errorf("argv missing computed age window: %v", cmd.Calls[0].Args)
	}
}

func TestSearchReviewRequestsOmitsAgeWindowWhenZero(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", "[]", nil)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me", 0, 20, now); err != nil {
		t.Fatalf("SearchReviewRequests: %v", err)
	}
	joined := strings.Join(cmd.Calls[0].Args, " ")
	if strings.Contains(joined, "updated:") {
		t.Errorf("argv has an age window with ageDays=0: %v", cmd.Calls[0].Args)
	}
}

func TestSearchReviewRequestsReturnsErrorOnFailure(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", "", errors.New("rate limited"))
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me", 14, 20, now); err == nil {
		t.Fatal("want an error when gh fails, got nil")
	}
}

func TestSearchReviewRequestsReturnsErrorOnBadJSON(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("gh", "not json", nil)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	if _, err := SearchReviewRequests(context.Background(), cmd, "review-requested:@me", 14, 20, now); err == nil {
		t.Fatal("want an error on unparseable output, got nil")
	}
}

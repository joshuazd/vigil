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

const shortAPIOutput = `{
  "next": null,
  "data": [
    {"id":223477,
     "name":"Bug: investigation status does not revert to Open when draft report is deleted",
     "app_url":"https://app.shortcut.com/huntress/story/223477",
     "updated_at":"2026-07-31T19:18:13Z"},
    {"id":223453,
     "name":"Control Plane - expose EDR Athena Redfig controls",
     "app_url":"https://app.shortcut.com/huntress/story/223453",
     "updated_at":"2026-07-31T14:25:54Z"}
  ]
}`

func TestSearchStoriesParsesItems(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", shortAPIOutput, nil)

	items, err := SearchStories(context.Background(), cmd, "owner:someone !is:done", 20)
	if err != nil {
		t.Fatalf("SearchStories: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	want := session.QueueItem{
		Kind:      session.QueueStory,
		ID:        "223477",
		Title:     "Bug: investigation status does not revert to Open when draft report is deleted",
		Input:     "https://app.shortcut.com/huntress/story/223477",
		UpdatedAt: time.Date(2026, 7, 31, 19, 18, 13, 0, time.UTC).Unix(),
	}
	if items[0] != want {
		t.Errorf("items[0] = %+v, want %+v", items[0], want)
	}
}

// TestSearchStoriesURLEncodesTheQuery pins that the query reaches short as one
// encoded path argument. This query has a space, a percent and a bang;
// unencoded, the space alone truncates the query server-side and the queue
// silently returns the wrong stories. It deliberately avoids %self% - that
// substitution is covered separately below - so this stays a pure encoding
// test regardless of how %self% is resolved.
func TestSearchStoriesURLEncodesTheQuery(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", `{"data":[]}`, nil)

	if _, err := SearchStories(context.Background(), cmd, "custom:%tag% !is:done", 20); err != nil {
		t.Fatalf("SearchStories: %v", err)
	}
	if len(cmd.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(cmd.Calls))
	}
	call := cmd.Calls[0]
	if call.Name != "short" {
		t.Errorf("ran %q, want short", call.Name)
	}
	want := []string{"api", "/search/stories?query=custom%3A%25tag%25+%21is%3Adone"}
	if len(call.Args) != len(want) {
		t.Fatalf("args = %v, want %v", call.Args, want)
	}
	for i := range want {
		if call.Args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, call.Args[i], want[i])
		}
	}
}

// TestSearchStoriesSubstitutesSelf is the fix for the production bug: `short
// api` does no templating of its own, so %self% - the operator `short
// search --help` tells users to write for their own mention name - must be
// replaced with the authenticated member's mention name before the query is
// URL-encoded and sent. Without the substitution, the literal "%self%" text
// (URL-encoded to "%25self%25") reaches Shortcut, which matches it against
// nobody and returns zero stories forever.
func TestSearchStoriesSubstitutesSelf(t *testing.T) {
	selfCache.Delete(selfCacheKey)
	defer selfCache.Delete(selfCacheKey)

	cmd := NewMockCommander()
	cmd.OnArgs("short api /member", `{"id":"1","mention_name":"joshuazd","name":"Joshua Zink-Duda"}`, nil)
	cmd.On("short", shortAPIOutput, nil)

	if _, err := SearchStories(context.Background(), cmd, "owner:%self% !is:done", 20); err != nil {
		t.Fatalf("SearchStories: %v", err)
	}

	var searchArgs []string
	for _, c := range cmd.Calls {
		if len(c.Args) > 0 && strings.Contains(c.Args[len(c.Args)-1], "/search/stories") {
			searchArgs = c.Args
		}
	}
	if searchArgs == nil {
		t.Fatal("no /search/stories call was made")
	}
	want := "api /search/stories?query=owner%3Ajoshuazd+%21is%3Adone"
	if got := strings.Join(searchArgs, " "); got != want {
		t.Errorf("search call args = %q, want %q", got, want)
	}
}

// TestSearchStoriesCachesSelfLookup pins that /member is fetched at most once
// per process, matching nwoCache's shape (internal/fetch/pr.go). SearchStories
// runs on every queue_interval; a lookup on every pass would spend a
// subprocess a poller has no need for.
func TestSearchStoriesCachesSelfLookup(t *testing.T) {
	selfCache.Delete(selfCacheKey)
	defer selfCache.Delete(selfCacheKey)

	cmd := NewMockCommander()
	cmd.OnArgs("short api /member", `{"id":"1","mention_name":"joshuazd","name":"Joshua Zink-Duda"}`, nil)
	cmd.On("short", shortAPIOutput, nil)

	if _, err := SearchStories(context.Background(), cmd, "owner:%self%", 20); err != nil {
		t.Fatalf("first SearchStories: %v", err)
	}
	if _, err := SearchStories(context.Background(), cmd, "owner:%self%", 20); err != nil {
		t.Fatalf("second SearchStories: %v", err)
	}

	memberCalls := cmd.CallCountFunc(func(c MockCall) bool {
		return c.Name == "short" && len(c.Args) > 0 && c.Args[len(c.Args)-1] == "/member"
	})
	if memberCalls != 1 {
		t.Errorf("got %d /member calls across two SearchStories calls, want 1", memberCalls)
	}
}

// TestSearchStoriesSkipsSelfLookupForLiteralQuery pins that a query with no
// %self% never pays for the /member lookup: a user who wrote a literal owner
// name should not spend a subprocess call they have no use for.
func TestSearchStoriesSkipsSelfLookupForLiteralQuery(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", shortAPIOutput, nil)

	if _, err := SearchStories(context.Background(), cmd, "owner:jsmith !is:done", 20); err != nil {
		t.Fatalf("SearchStories: %v", err)
	}

	memberCalls := cmd.CallCountFunc(func(c MockCall) bool {
		return c.Name == "short" && len(c.Args) > 0 && c.Args[len(c.Args)-1] == "/member"
	})
	if memberCalls != 0 {
		t.Errorf("got %d /member calls for a query without %%self%%, want 0", memberCalls)
	}
	if len(cmd.Calls) != 1 {
		t.Errorf("got %d total calls, want 1 (search only)", len(cmd.Calls))
	}
}

// TestSearchStoriesReturnsErrorWhenSelfLookupFails pins that a failed /member
// lookup fails the whole call rather than issuing a search with "%self%"
// still in it (Shortcut matches that against nobody) or with an empty owner
// (a malformed query). Either would silently return zero stories forever;
// an error lets queueStore.commit keep the last known list instead.
func TestSearchStoriesReturnsErrorWhenSelfLookupFails(t *testing.T) {
	selfCache.Delete(selfCacheKey)
	defer selfCache.Delete(selfCacheKey)

	cmd := NewMockCommander()
	cmd.OnArgs("short api /member", "", errors.New("not authenticated"))

	if _, err := SearchStories(context.Background(), cmd, "owner:%self%", 20); err == nil {
		t.Fatal("want an error when the /member lookup fails, got nil")
	}
	if len(cmd.Calls) != 1 {
		t.Errorf("got %d calls, want 1: no search should be issued after a failed lookup", len(cmd.Calls))
	}
}

// TestSearchStoriesReturnsErrorWhenMentionNameEmpty covers the other failure
// shape: short api /member can exit 0 with JSON that has no mention_name
// (an unexpected account state, or a future response shape change). An empty
// substitution would build "owner:" - malformed - so this must also fail
// closed rather than search with an empty owner.
func TestSearchStoriesReturnsErrorWhenMentionNameEmpty(t *testing.T) {
	selfCache.Delete(selfCacheKey)
	defer selfCache.Delete(selfCacheKey)

	cmd := NewMockCommander()
	cmd.OnArgs("short api /member", `{"id":"1","name":"Nobody"}`, nil)

	if _, err := SearchStories(context.Background(), cmd, "owner:%self%", 20); err == nil {
		t.Fatal("want an error when mention_name is empty, got nil")
	}
	if len(cmd.Calls) != 1 {
		t.Errorf("got %d calls, want 1: no search should be issued after an empty mention_name", len(cmd.Calls))
	}
}

func TestSearchStoriesAppliesLimit(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", shortAPIOutput, nil)

	items, err := SearchStories(context.Background(), cmd, "owner:someone", 1)
	if err != nil {
		t.Fatalf("SearchStories: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
}

func TestSearchStoriesReturnsErrorOnFailure(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", "", errors.New("short: command not found"))

	if _, err := SearchStories(context.Background(), cmd, "owner:someone", 20); err == nil {
		t.Fatal("want an error when short fails, got nil")
	}
}

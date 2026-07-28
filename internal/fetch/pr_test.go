package fetch

import (
	"context"
	"strings"
	"testing"
)

// TestPollingQueryDoesNotAskForCommentBodies is the budget fix. The bodies are
// read only by the detail panel, for one session at a time, but every poll
// requested five per thread for every open PR.
func TestPollingQueryDoesNotAskForCommentBodies(t *testing.T) {
	if strings.Contains(reviewThreadsQuery, "comments(") {
		t.Error("the polling query still requests comment bodies")
	}
	for _, field := range []string{"isResolved", "isOutdated"} {
		if !strings.Contains(reviewThreadsQuery, field) {
			t.Errorf("the polling query dropped %s, which the unresolved count needs", field)
		}
	}
}

// TestFetchPRStatusStillCountsUnresolvedThreads pins the load-bearing half.
// UnresolvedComments drives session.State() == Unresolved, the badge and the
// transition notifications, so it must survive the trim.
func TestFetchPRStatusStillCountsUnresolvedThreads(t *testing.T) {
	cmd := NewMockCommander()
	cmd.OnArgs("gh pr view feature/x --json number,state,isDraft,url,title,body,statusCheckRollup,reviewDecision,latestReviews,mergeable,reviewRequests",
		`{"number":42,"state":"OPEN","url":"u","title":"t"}`, nil)
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
				{"isResolved":false,"isOutdated":false,"path":"a.go"},
				{"isResolved":true,"isOutdated":false,"path":"b.go"},
				{"isResolved":false,"isOutdated":true,"path":"c.go"}
			]}}}}}`, nil
		},
	}

	pr := FetchPRStatus(context.Background(), cmd, "feature/x", "/repo")
	if pr == nil {
		t.Fatal("got nil PRStatus")
		return
	}
	if pr.UnresolvedComments != 1 {
		t.Errorf("got %d unresolved, want 1: resolved and outdated threads do not count", pr.UnresolvedComments)
	}
	if len(pr.ReviewComments) != 0 {
		t.Errorf("got %d comments from a poll, want 0", len(pr.ReviewComments))
	}
}

// TestFetchPRStatusSendsOnlyThePollingQuery asserts on the recorded invocation
// rather than on the constant, so aiming the poll at the wrong query is caught.
func TestFetchPRStatusSendsOnlyThePollingQuery(t *testing.T) {
	cmd := NewMockCommander()
	cmd.OnArgs("gh pr view feature/x --json number,state,isDraft,url,title,body,statusCheckRollup,reviewDecision,latestReviews,mergeable,reviewRequests",
		`{"number":42,"state":"OPEN","url":"u","title":"t"}`, nil)
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`, nil
		},
	}

	FetchPRStatus(context.Background(), cmd, "feature/x", "/repo")

	var sent []string
	for _, c := range cmd.Calls {
		if c.Name == "gh" && len(c.Args) > 1 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			sent = append(sent, strings.Join(c.Args, " "))
		}
	}
	if len(sent) != 1 {
		t.Fatalf("got %d graphql calls from one poll, want 1", len(sent))
	}
	if strings.Contains(sent[0], "comments(") {
		t.Errorf("the poll asked for comment bodies:\n%s", sent[0])
	}
}

func TestFetchReviewCommentsReturnsBodies(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("git", "git@github.com:owner/repo.git", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"gh api": func(_ context.Context, _ string, _ []string) (string, error) {
			return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
				{"isResolved":false,"isOutdated":false,"path":"a.go","comments":{"nodes":[
					{"author":{"login":"reviewer"},"body":"this needs a test"}
				]}}
			]}}}}}`, nil
		},
	}

	comments := FetchReviewComments(context.Background(), cmd, "/repo", 42)
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}
	if comments[0].Author != "reviewer" || comments[0].Body != "this needs a test" {
		t.Errorf("got %+v, want the reviewer's comment", comments[0])
	}
	if comments[0].Path != "a.go" {
		t.Errorf("got path %q, want a.go", comments[0].Path)
	}
}

func TestParseChecksPass(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "SUCCESS"},
	}
	if parseChecks(rollup) != "pass" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksFail(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "FAILURE"},
	}
	if parseChecks(rollup) != "fail" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksPending(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"status": "IN_PROGRESS"},
	}
	if parseChecks(rollup) != "pending" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksEmpty(t *testing.T) {
	if parseChecks(nil) != "" {
		t.Errorf("got %q, want empty", parseChecks(nil))
	}
}

func TestGetNWOSSH(t *testing.T) {
	// Clear cache
	nwoCache.Delete("/repo")

	mock := NewMockCommander()
	mock.OnArgs("git remote get-url origin", "git@github.com:owner/repo.git", nil)

	nwo := getNWO(context.Background(), mock, "/repo")
	if nwo[0] != "owner" || nwo[1] != "repo" {
		t.Errorf("got %v, want [owner repo]", nwo)
	}

	nwoCache.Delete("/repo")
}

func TestGetNWOHTTPS(t *testing.T) {
	nwoCache.Delete("/repo2")

	mock := NewMockCommander()
	mock.OnArgs("git remote get-url origin", "https://github.com/myorg/myrepo.git", nil)

	nwo := getNWO(context.Background(), mock, "/repo2")
	if nwo[0] != "myorg" || nwo[1] != "myrepo" {
		t.Errorf("got %v, want [myorg myrepo]", nwo)
	}

	nwoCache.Delete("/repo2")
}

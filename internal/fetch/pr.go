package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

var nwoCache sync.Map // map[string][2]string

// reviewThreadsQuery is the polling query. It asks only what the unresolved
// count needs. The comment bodies it used to fetch are read by one detail panel
// for one session, but this runs for every open PR every pr_interval, and
// GitHub scores the GraphQL limit on nodes requested.
const reviewThreadsQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes { isResolved isOutdated }
      }
    }
  }
}
`

const reviewCommentsQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          isOutdated
          path
          comments(first: 5) {
            nodes { author { login } body }
          }
        }
      }
    }
  }
}
`

// FetchPRStatus fetches PR status for a branch via gh CLI.
func FetchPRStatus(ctx context.Context, cmd Commander, branch, gitRoot string) *session.PRStatus {
	out, err := runWithRetry(ctx, cmd, gitRoot, "gh", "pr", "view", branch,
		"--json", "number,state,isDraft,url,title,body,statusCheckRollup,reviewDecision,latestReviews,mergeable,reviewRequests")
	if err != nil {
		return nil
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil
	}

	number := jsonInt(data, "number")
	state := jsonStr(data, "state")
	isDraft := jsonBool(data, "isDraft")
	url := jsonStr(data, "url")
	title := jsonStr(data, "title")
	body := jsonStr(data, "body")
	reviewDecision := jsonStr(data, "reviewDecision")
	hasConflicts := jsonStr(data, "mergeable") == "CONFLICTING"

	checks := parseChecks(jsonArray(data, "statusCheckRollup"))

	approvals := 0
	for _, r := range jsonArray(data, "latestReviews") {
		if rm, ok := r.(map[string]any); ok {
			if jsonStr(rm, "state") == "APPROVED" {
				approvals++
			}
		}
	}

	reviewersRequested := len(jsonArray(data, "reviewRequests"))

	var unresolved int
	if state == "OPEN" {
		unresolved = fetchReviewThreads(ctx, cmd, gitRoot, number)
	}

	return &session.PRStatus{
		Number:             number,
		State:              state,
		IsDraft:            isDraft,
		URL:                url,
		Checks:             checks,
		ReviewDecision:     reviewDecision,
		Approvals:          approvals,
		UnresolvedComments: unresolved,
		HasConflicts:       hasConflicts,
		ReviewersRequested: reviewersRequested,
		Title:              title,
		Body:               body,
	}
}

func parseChecks(rollup []any) string {
	if len(rollup) == 0 {
		return ""
	}
	var statuses []string
	for _, item := range rollup {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s := jsonStr(m, "conclusion")
		if s == "" {
			s = jsonStr(m, "status")
		}
		if s == "" {
			s = jsonStr(m, "state")
		}
		if s != "" {
			statuses = append(statuses, strings.ToUpper(s))
		}
	}
	if len(statuses) == 0 {
		return ""
	}
	for _, s := range statuses {
		if s == "FAILURE" || s == "ERROR" {
			return "fail"
		}
	}
	for _, s := range statuses {
		if s == "PENDING" || s == "QUEUED" || s == "IN_PROGRESS" || s == "WAITING" {
			return "pending"
		}
	}
	return "pass"
}

// fetchReviewThreads returns the number of threads that are neither resolved
// nor outdated. That count drives session.State() == Unresolved, so it is
// polled for every open PR.
func fetchReviewThreads(ctx context.Context, cmd Commander, gitRoot string, prNumber int) int {
	nodes := reviewThreadNodes(ctx, cmd, gitRoot, prNumber, reviewThreadsQuery)
	unresolved := 0
	for _, t := range nodes {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if !jsonBool(tm, "isResolved") && !jsonBool(tm, "isOutdated") {
			unresolved++
		}
	}
	return unresolved
}

// FetchReviewComments fetches the review comment bodies for one PR. Called for
// the selected session when the detail panel is showing comments, never from a
// poll.
func FetchReviewComments(ctx context.Context, cmd Commander, gitRoot string, prNumber int) []session.ReviewComment {
	var comments []session.ReviewComment
	for _, t := range reviewThreadNodes(ctx, cmd, gitRoot, prNumber, reviewCommentsQuery) {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		resolved := jsonBool(tm, "isResolved")
		path := jsonStr(tm, "path")
		nodes, ok := jsonPath(tm, "comments", "nodes").([]any)
		if !ok {
			continue
		}
		for _, c := range nodes {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			author := ""
			if a, ok := cm["author"].(map[string]any); ok {
				author = jsonStr(a, "login")
			}
			comments = append(comments, session.ReviewComment{
				Author:   author,
				Body:     jsonStr(cm, "body"),
				Path:     path,
				Resolved: resolved,
			})
		}
	}
	return comments
}

func reviewThreadNodes(ctx context.Context, cmd Commander, gitRoot string, prNumber int, query string) []any {
	nwo := getNWO(ctx, cmd, gitRoot)
	if nwo == [2]string{} {
		return nil
	}
	out, err := runWithRetry(ctx, cmd, gitRoot, "gh", "api", "graphql",
		"-f", "query="+query,
		"-F", "owner="+nwo[0],
		"-F", "repo="+nwo[1],
		"-F", fmt.Sprintf("number=%d", prNumber))
	if err != nil {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil
	}
	nodes, _ := jsonPath(data, "data", "repository", "pullRequest", "reviewThreads", "nodes").([]any)
	return nodes
}

func getNWO(ctx context.Context, cmd Commander, gitRoot string) [2]string {
	if cached, ok := nwoCache.Load(gitRoot); ok {
		return cached.([2]string)
	}
	out, err := cmd.Run(ctx, gitRoot, "git", "remote", "get-url", "origin")
	if err != nil {
		return [2]string{}
	}
	url := strings.TrimSuffix(strings.TrimSpace(out), ".git")
	var path string
	if idx := strings.Index(url, "github.com:"); idx >= 0 {
		path = url[idx+len("github.com:"):]
	} else if idx := strings.Index(url, "github.com/"); idx >= 0 {
		path = url[idx+len("github.com/"):]
	} else {
		return [2]string{}
	}
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return [2]string{}
	}
	nwo := [2]string{parts[0], parts[1]}
	nwoCache.Store(gitRoot, nwo)
	return nwo
}

// noPRMarker is how gh says a branch has no pull request. It exits 1 to say
// it, which is indistinguishable from a transient failure unless the message
// is read.
var noPRMarker = []byte("no pull requests found")

// definitiveAnswer reports whether an error is gh telling us something true
// rather than gh failing. Retrying a true answer is what made a freshly
// dispatched session take 5 to 10 seconds to appear: its branch has no PR yet,
// so every poll spent three gh calls and 3s of backoff to be told the same
// thing three times, all of it inside a synchronous Snapshot that publishes
// nothing until it returns.
func definitiveAnswer(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	return bytes.Contains(exit.Stderr, noPRMarker)
}

func runWithRetry(ctx context.Context, cmd Commander, dir string, name string, args ...string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		out, err := cmd.Run(ctx, dir, name, args...)
		if err == nil {
			return out, nil
		}
		if definitiveAnswer(err) {
			return "", err
		}
		lastErr = err
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return "", lastErr
}

// JSON helpers
func jsonStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func jsonInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func jsonBool(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func jsonArray(m map[string]any, key string) []any {
	v, _ := m[key].([]any)
	return v
}

func jsonPath(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = cm[key]
	}
	return current
}

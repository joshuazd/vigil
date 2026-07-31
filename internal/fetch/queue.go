package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

// selfCache holds the authenticated Shortcut member's mention name, resolved
// at most once per process. Matches nwoCache's shape (internal/fetch/pr.go):
// a package-level sync.Map consulted before the subprocess it would otherwise
// repeat every queue_interval. There is only ever one key, because the
// mention name is not per-argument the way a repo's owner/name is.
var selfCache sync.Map // map[string]string

const selfCacheKey = "self"

// selfPlaceholder is the operator `short search --help` tells users to write
// for "my own mention name". `short api` performs no such templating -
// nothing does, until this function - so this substitutes it before the
// query reaches the endpoint.
const selfPlaceholder = "%self%"

// resolveSelf substitutes %self% in query with the authenticated member's
// mention name. An empty or unresolved mention name would build
// "owner:%self%" (which Shortcut matches against nobody) or "owner:" (which
// is malformed), so a failed lookup is returned as an error rather than
// silently producing either.
func resolveSelf(ctx context.Context, cmd Commander, query string) (string, error) {
	if !strings.Contains(query, selfPlaceholder) {
		return query, nil
	}
	self, err := getSelf(ctx, cmd)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(query, selfPlaceholder, self), nil
}

func getSelf(ctx context.Context, cmd Commander) (string, error) {
	if cached, ok := selfCache.Load(selfCacheKey); ok {
		return cached.(string), nil
	}

	out, err := cmd.Run(ctx, "", "short", "api", "/member")
	if err != nil {
		return "", fmt.Errorf("short api /member: %w", err)
	}

	var payload struct {
		MentionName string `json:"mention_name"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return "", fmt.Errorf("parsing short api /member output: %w", err)
	}
	if payload.MentionName == "" {
		return "", fmt.Errorf("short api /member: empty mention_name")
	}

	selfCache.Store(selfCacheKey, payload.MentionName)
	return payload.MentionName, nil
}

// SearchReviewRequests lists PRs whose review is requested, per the
// configured query.
//
// Every gh flag precedes `--` and every config-supplied token follows it.
// Without the separator, cobra parses a leading-dash qualifier as a flag and
// `-is:draft` fails with "unknown shorthand flag: 'i'".
//
// GitHub search has no relative dates, so the age window is computed here and
// appended rather than living in the query string.
//
// query is split on whitespace, so a qualifier containing a space is not
// supported. No GitHub PR qualifier needs one.
func SearchReviewRequests(ctx context.Context, cmd Commander, query string, ageDays, limit int, now time.Time) ([]session.QueueItem, error) {
	args := []string{
		"search", "prs",
		"--state=open",
		"--limit", strconv.Itoa(limit),
		"--json", "number,repository,title,url,updatedAt",
		"--",
	}
	args = append(args, strings.Fields(query)...)
	if ageDays > 0 {
		args = append(args, "updated:>="+now.AddDate(0, 0, -ageDays).Format("2006-01-02"))
	}

	out, err := cmd.Run(ctx, "", "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh search prs: %w", err)
	}

	var raw []struct {
		Number     int    `json:"number"`
		Title      string `json:"title"`
		URL        string `json:"url"`
		UpdatedAt  string `json:"updatedAt"`
		Repository struct {
			Name string `json:"name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parsing gh search prs output: %w", err)
	}

	items := make([]session.QueueItem, 0, len(raw))
	for _, r := range raw {
		items = append(items, session.QueueItem{
			Kind:      session.QueueReview,
			ID:        strconv.Itoa(r.Number),
			Title:     r.Title,
			Input:     r.URL,
			Repo:      r.Repository.Name,
			UpdatedAt: parseTimestamp(r.UpdatedAt),
		})
	}
	return items, nil
}

func parseTimestamp(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// SearchStories lists Shortcut stories per the configured query.
//
// `short api` is used rather than `short search`: `short search --format`
// templates do not interpolate, and `short api` returns clean JSON on stdout
// with its progress spinner on stderr, which Commander.Run discards.
func SearchStories(ctx context.Context, cmd Commander, query string, limit int) ([]session.QueueItem, error) {
	query, err := resolveSelf(ctx, cmd, query)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", selfPlaceholder, err)
	}

	path := "/search/stories?query=" + url.QueryEscape(query)

	out, err := cmd.Run(ctx, "", "short", "api", path)
	if err != nil {
		return nil, fmt.Errorf("short api: %w", err)
	}

	var payload struct {
		Data []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			AppURL    string `json:"app_url"`
			UpdatedAt string `json:"updated_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, fmt.Errorf("parsing short api output: %w", err)
	}

	if limit > 0 && len(payload.Data) > limit {
		payload.Data = payload.Data[:limit]
	}

	items := make([]session.QueueItem, 0, len(payload.Data))
	for _, d := range payload.Data {
		items = append(items, session.QueueItem{
			Kind:      session.QueueStory,
			ID:        strconv.Itoa(d.ID),
			Title:     d.Name,
			Input:     d.AppURL,
			UpdatedAt: parseTimestamp(d.UpdatedAt),
		})
	}
	return items, nil
}

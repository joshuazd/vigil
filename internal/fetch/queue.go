package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

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

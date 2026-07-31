# Phase 5: Work Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** vigild polls assigned Shortcut stories and review-requested PRs, the dashboard renders them as a QUEUE section below the session list, the panel shows a count badge, and `enter` on a queue row dispatches it detached.

**Architecture:** Two new `poller` implementations behind the seam `7b89c0e` landed in `internal/collect/remote.go`, each on its own goroutine. `Collector.Snapshot` is **not touched**. A new `Collector.Queue(sessions)` merges the two stores, hides anything a live tmux session already covers, and the daemon publishes the result as an additive `Snapshot.Queue`. Detach reaches the workflow scripts through a new unquoted `{flags}` placeholder in the `dispatch` hook.

**Tech Stack:** Go, Bubble Tea, lipgloss. Subprocesses via `fetch.Commander` (`gh` and `short`). No new dependencies.

Spec: `docs/superpowers/specs/2026-07-31-phase-5-work-queue-design.md`.

## Global Constraints

- `make test` is `go test -race ./...`. **`-race` is not optional**; the daemon's design is a concurrency claim.
- `make lint` (golangci-lint) must pass. `gofmt -l internal/` lists 7 pre-existing files - leave them; do not drive-by format.
- **`protocol.Version` stays `1`.** Every protocol change in this plan is additive with `omitempty`.
- **`Collector.Snapshot`'s signature and body do not change.** No task may add a network call to it.
- **No ticker in `remote`.** Workers are woken only by `Snapshot`'s nudge. Adding one silently restores per-panel polling for every open panel.
- Comments only where the meaning cannot be inferred from the code (project convention).
- No em dashes in any prose or comment. Use a plain dash.
- Every subprocess goes through `fetch.Commander`. The only direct `exec` sites are `internal/fetch/cmd.go` and `internal/model/client.go`.
- Any test reaching `config.Load(config.ConfigPath())` must set its own `HOME`.
- **Watch every test fail before writing its subject, and work out what mutation it catches.** Across two plans on this repo, ten briefs shipped tests that would have passed with their subject deleted. If a test passes before its subject exists, that is a defect in this plan - stop and report it.

## File Structure

| File | Responsibility |
|---|---|
| `internal/session/queue.go` (new) | `QueueItem` type, its display label, and the dotfiles session-name convention |
| `internal/session/queue_test.go` (new) | Label and session-name matching |
| `internal/fetch/queue.go` (new) | `gh search prs` and `short api` subprocess wrappers and their JSON parsing |
| `internal/fetch/queue_test.go` (new) | Exact argv, `--` placement, JSON parsing |
| `internal/collect/queue.go` (new) | `queueStore`, `reviewPoller`, `storyPoller` |
| `internal/collect/queue_test.go` (new) | Due-ness, invalidate, failed-pass behaviour |
| `internal/collect/collect.go` (modify) | New `Collector` fields, `New` wiring, `Queue()` |
| `internal/config/config.go` (modify) | Six settings; unquoted `{flags}` placeholder |
| `internal/protocol/protocol.go` (modify) | `Snapshot.Queue`, `Snapshot.QueueHidden`, `Request.Detached` |
| `internal/daemon/daemon.go` (modify) | `poll` publishes the queue |
| `internal/daemon/jobs.go` (modify) | Per-request detached flag, `{flags}` hook var |
| `internal/dispatch/submit.go` (modify) | `Options.Detached` |
| `internal/view/queue.go` (new) | `RenderQueue` |
| `internal/view/queue_test.go` (new) | Golden render |
| `internal/view/statusbar.go` (modify) | `⚡N` badge segment |
| `internal/model/model.go`, `client.go`, `messages.go` (modify) | Queue state, cursor, `enter` |
| `main.go` (modify) | `{flags}` migration warning |
| `README.md` (modify) | Settings table, hook migration |

---

### Task 1: `session.QueueItem` and the six config settings

The vocabulary every later task uses. Both halves are pure and independently testable.

**Files:**
- Create: `internal/session/queue.go`
- Create: `internal/session/queue_test.go`
- Modify: `internal/config/config.go:32-46` (`settingDefaults`)
- Modify: `internal/config/config_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: `session.QueueStory` / `session.QueueReview` constants; `session.QueueItem{Kind, ID, Title, Input, Repo string; UpdatedAt int64}`; methods `Label() string`, `SessionPrefix() string`, `MatchesSessionName(string) bool`. Settings `queue_enabled`, `queue_pr_query`, `queue_pr_age_days`, `queue_story_query`, `queue_interval`, `queue_limit`.

- [ ] **Step 1: Write the failing tests**

Create `internal/session/queue_test.go`:

```go
package session

import "testing"

func TestQueueItemLabel(t *testing.T) {
	tests := []struct {
		name string
		item QueueItem
		want string
	}{
		{"story", QueueItem{Kind: QueueStory, ID: "223480"}, "sc-223480"},
		{"review with repo", QueueItem{Kind: QueueReview, ID: "34967", Repo: "portal"}, "portal#34967"},
		{"review without repo", QueueItem{Kind: QueueReview, ID: "34967"}, "#34967"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.Label(); got != tt.want {
				t.Errorf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestQueueItemMatchesSessionName pins the exact format dotfiles'
// session_name_from_title produces. It is the only tripwire on this side of a
// cross-repository convention: if that helper changes shape, dedup degrades
// silently and the queue starts advertising work already in flight.
func TestQueueItemMatchesSessionName(t *testing.T) {
	story := QueueItem{Kind: QueueStory, ID: "223477"}
	review := QueueItem{Kind: QueueReview, ID: "34930", Repo: "portal"}

	tests := []struct {
		name    string
		item    QueueItem
		session string
		want    bool
	}{
		{"story with title", story, "SC-223477 Bug investigation status", true},
		{"story bare", story, "SC-223477", true},
		{"review with title", review, "PR-34930 Add agentic IoC extraction", true},
		{"review bare", review, "PR-34930", true},
		{"different id", story, "SC-223478 Something else", false},
		{"id is a prefix of another", story, "SC-2234770 Something else", false},
		{"wrong kind prefix", story, "PR-223477 Something", false},
		{"unrelated", story, "main", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.MatchesSessionName(tt.session); got != tt.want {
				t.Errorf("MatchesSessionName(%q) = %v, want %v", tt.session, got, tt.want)
			}
		})
	}
}
```

The `"id is a prefix of another"` case is the one that matters: it is what forces the trailing space into `SessionPrefix` and rules out a naive `strings.HasPrefix(name, "SC-"+id)`.

Add to `internal/config/config_test.go`:

```go
func TestQueueSettingDefaults(t *testing.T) {
	cfg := &Config{}
	tests := []struct{ key, want string }{
		{"queue_enabled", "true"},
		{"queue_pr_query", "review-requested:@me -is:draft"},
		{"queue_pr_age_days", "14"},
		{"queue_story_query", "owner:%self% !is:done !is:archived"},
		{"queue_interval", "60"},
		{"queue_limit", "20"},
	}
	for _, tt := range tests {
		if got := cfg.GetSetting(tt.key); got != tt.want {
			t.Errorf("GetSetting(%q) = %q, want %q", tt.key, got, tt.want)
		}
		if !IsSetting(tt.key) {
			t.Errorf("IsSetting(%q) = false, want true", tt.key)
		}
	}
}
```

`IsSetting` is asserted because `vigil config get` uses it to tell an unknown key from a legitimately-empty one, and a setting missing from that map is invisible to bash callers.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/session/ -run TestQueueItem -v
go test ./internal/config/ -run TestQueueSettingDefaults -v
```

Expected: `internal/session` fails to build (`undefined: QueueItem`, `undefined: QueueStory`). `internal/config` compiles and fails with six `GetSetting(...) = "", want ...` errors plus six `IsSetting` errors.

- [ ] **Step 3: Write `internal/session/queue.go`**

```go
package session

import "strings"

const (
	QueueStory  = "story"
	QueueReview = "review"
)

// QueueItem is one piece of work waiting to be started: an assigned Shortcut
// story or a PR that has requested this user's review.
//
// Input is stored rather than reconstructed at dispatch time. The `dispatch`
// script routes on the shape of its argument, and the poller that fetched the
// item is the only thing that knows for certain which shape it needs.
type QueueItem struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Input     string `json:"input"`
	Repo      string `json:"repo,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// Label is the id column. Repo-qualified for reviews because
// soc-workflows#205 and portal#205 are otherwise the same string.
func (q QueueItem) Label() string {
	if q.Kind == QueueStory {
		return "sc-" + q.ID
	}
	if q.Repo == "" {
		return "#" + q.ID
	}
	return q.Repo + "#" + q.ID
}

// SessionPrefix is the tmux session name a dispatch of this item produces, up
// to and including the separating space. dotfiles' session_name_from_title
// builds "SC-<id> <title>" and "PR-<number> <title>"; this is the vigil-side
// half of that convention and the only thing tying the two repositories
// together. The trailing space is load-bearing: without it SC-223477 matches
// a session for SC-2234770.
func (q QueueItem) SessionPrefix() string {
	if q.Kind == QueueStory {
		return "SC-" + q.ID + " "
	}
	return "PR-" + q.ID + " "
}

func (q QueueItem) MatchesSessionName(name string) bool {
	prefix := q.SessionPrefix()
	return name == strings.TrimSuffix(prefix, " ") || strings.HasPrefix(name, prefix)
}
```

- [ ] **Step 4: Add the six settings**

In `internal/config/config.go`, inside `settingDefaults`, after the `dispatch_timeout` line:

```go
	"queue_enabled":     {"VIGIL_QUEUE_ENABLED", "true"},
	"queue_pr_query":    {"VIGIL_QUEUE_PR_QUERY", "review-requested:@me -is:draft"},
	"queue_pr_age_days": {"VIGIL_QUEUE_PR_AGE_DAYS", "14"},
	"queue_story_query": {"VIGIL_QUEUE_STORY_QUERY", "owner:%self% !is:done !is:archived"},
	"queue_interval":    {"VIGIL_QUEUE_INTERVAL", "60"},
	"queue_limit":       {"VIGIL_QUEUE_LIMIT", "20"},
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/session/ ./internal/config/ -race
```

Expected: PASS.

- [ ] **Step 6: Document the settings in the README**

Add to the settings table in `README.md`:

```markdown
| `queue_enabled` | `true` | Poll for assigned stories and review-requested PRs. `false` constructs no pollers at all. |
| `queue_pr_query` | `review-requested:@me -is:draft` | Passed to `gh search prs` after `--`, split on whitespace. A qualifier containing a space is not supported. |
| `queue_pr_age_days` | `14` | Appended as `updated:>=<date>`, recomputed each poll. GitHub search has no relative dates, which is why this is a separate setting rather than part of the query. `0` disables the window. |
| `queue_story_query` | `owner:%self% !is:done !is:archived` | Passed to `short api /search/stories?query=`. Names no workflow state on purpose: state names are workspace-specific. |
| `queue_interval` | `60` | Seconds between queue polls. |
| `queue_limit` | `20` | Caps each fetch and the merged list. |
```

- [ ] **Step 7: Commit**

```bash
git add internal/session/queue.go internal/session/queue_test.go internal/config/config.go internal/config/config_test.go README.md
git commit -m "feat(session): add QueueItem and the six queue settings"
```

---

### Task 2: `fetch.SearchReviewRequests`

**Files:**
- Create: `internal/fetch/queue.go`
- Create: `internal/fetch/queue_test.go`

**Interfaces:**
- Consumes: `session.QueueItem` (Task 1).
- Produces: `fetch.SearchReviewRequests(ctx context.Context, cmd Commander, query string, ageDays, limit int, now time.Time) ([]session.QueueItem, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/fetch/queue_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/fetch/ -run TestSearchReviewRequests -v
```

Expected: build failure, `undefined: SearchReviewRequests`.

- [ ] **Step 3: Write `internal/fetch/queue.go`**

```go
package fetch

import (
	"context"
	"encoding/json"
	"fmt"
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/fetch/ -run TestSearchReviewRequests -race -v
```

Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/fetch/queue.go internal/fetch/queue_test.go
git commit -m "feat(fetch): search review-requested PRs, with query tokens after --"
```

---

### Task 3: `fetch.SearchStories`

**Files:**
- Modify: `internal/fetch/queue.go`
- Modify: `internal/fetch/queue_test.go`

**Interfaces:**
- Produces: `fetch.SearchStories(ctx context.Context, cmd Commander, query string, limit int) ([]session.QueueItem, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/fetch/queue_test.go`:

```go
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

	items, err := SearchStories(context.Background(), cmd, "owner:%self% !is:done", 20)
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
// encoded path argument. The default query contains a space, a percent and a
// bang; unencoded, the space alone truncates the query server-side and the
// queue silently returns the wrong stories.
func TestSearchStoriesURLEncodesTheQuery(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", `{"data":[]}`, nil)

	if _, err := SearchStories(context.Background(), cmd, "owner:%self% !is:done", 20); err != nil {
		t.Fatalf("SearchStories: %v", err)
	}
	if len(cmd.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(cmd.Calls))
	}
	call := cmd.Calls[0]
	if call.Name != "short" {
		t.Errorf("ran %q, want short", call.Name)
	}
	want := []string{"api", "/search/stories?query=owner%3A%25self%25+%21is%3Adone"}
	if len(call.Args) != len(want) {
		t.Fatalf("args = %v, want %v", call.Args, want)
	}
	for i := range want {
		if call.Args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, call.Args[i], want[i])
		}
	}
}

func TestSearchStoriesAppliesLimit(t *testing.T) {
	cmd := NewMockCommander()
	cmd.On("short", shortAPIOutput, nil)

	items, err := SearchStories(context.Background(), cmd, "owner:%self%", 1)
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

	if _, err := SearchStories(context.Background(), cmd, "owner:%self%", 20); err == nil {
		t.Fatal("want an error when short fails, got nil")
	}
}
```

Add to `main_test.go`, because this is where the `short` dependency enters the codebase:

```go
// TestShortIsNotAStartupDependency pins the deliberate asymmetry: vigil must
// keep working for anyone without Shortcut installed. A missing short leaves
// the story half of the queue empty, which is a degraded feature; adding it to
// this list would make it a refusal to start.
func TestShortIsNotAStartupDependency(t *testing.T) {
	for _, dep := range startupDependencies {
		if dep == "short" {
			t.Fatal("short must not be a startup dependency: vigil has to run without Shortcut")
		}
	}
}
```

This needs `main.go:80`'s inline `[]string{"tmux", "git", "gh"}` lifted to a package-level `var startupDependencies = []string{"tmux", "git", "gh"}` so the test can see it. Make that change in Step 3.

If the encoded string in `TestSearchStoriesURLEncodesTheQuery` does not match what `url.QueryEscape` produces, fix the **expectation**, not the implementation - the point of the test is that encoding happens at all and that it is one argument, not two.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/fetch/ -run TestSearchStories -v
```

Expected: build failure, `undefined: SearchStories`.

- [ ] **Step 3: Implement**

In `main.go`, lift the dependency list out of the loop so the test can reach it:

```go
// startupDependencies are the binaries vigil refuses to start without.
// `short` is deliberately absent: a missing Shortcut CLI leaves the story half
// of the work queue empty, which is a degraded feature rather than a reason to
// refuse to run.
var startupDependencies = []string{"tmux", "git", "gh"}
```

and replace the literal at `main.go:80` with `startupDependencies`.

Add to `internal/fetch/queue.go` (and add `"net/url"` to the imports):

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/fetch/ -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fetch/queue.go internal/fetch/queue_test.go
git commit -m "feat(fetch): search assigned Shortcut stories via short api"
```

---

### Task 4: The two pollers and their wiring

**Files:**
- Create: `internal/collect/queue.go`
- Create: `internal/collect/queue_test.go`
- Modify: `internal/collect/collect.go:13-17` (constants), `:19-47` (`Collector`), `:54-71` (`New`)

**Interfaces:**
- Consumes: `fetch.SearchReviewRequests`, `fetch.SearchStories` (Tasks 2-3).
- Produces: unexported `queueStore`, `reviewPoller`, `storyPoller`, both satisfying the existing `poller` interface. `Collector` fields `QueueInterval time.Duration`, `QueuePRQuery, QueueStoryQuery string`, `QueuePRAgeDays, QueueLimit int`, and unexported `stories *storyPoller`, `reviews *reviewPoller`.

- [ ] **Step 1: Write the failing tests**

Create `internal/collect/queue_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/collect/ -run TestQueue -v
```

Expected: build failure, `c.reviews undefined`, `c.stories undefined`.

- [ ] **Step 3: Write `internal/collect/queue.go`**

```go
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
```

- [ ] **Step 4: Wire the Collector**

In `internal/collect/collect.go`, add to the constant block:

```go
	defaultQueueInterval = 60 * time.Second
	defaultQueueLimit    = 20
```

Add to the `Collector` struct, below `PRInterval` and inside the same read-only-after-New contract (extend that comment's "These four" to "These eight"):

```go
	QueueInterval   time.Duration
	QueuePRQuery    string
	QueueStoryQuery string
	QueuePRAgeDays  int
	QueueLimit      int
```

and below `prs`:

```go
	// stories and reviews are nil when queue_enabled is false. Nil rather
	// than constructed-and-skipped: there is then no code path that can spend
	// budget by accident.
	stories *storyPoller
	reviews *reviewPoller
```

In `New`, replace the tail (`c := &Collector{...}` onward) with:

```go
	queueInterval := cfg.GetSettingDuration("queue_interval")
	if queueInterval <= 0 {
		queueInterval = defaultQueueInterval
	}
	queueLimit := cfg.GetSettingInt("queue_limit")
	if queueLimit <= 0 {
		queueLimit = defaultQueueLimit
	}

	c := &Collector{
		Cmd:             cmd,
		GitWorkers:      workers,
		GitInterval:     gitInterval,
		PRInterval:      prInterval,
		QueueInterval:   queueInterval,
		QueuePRQuery:    cfg.GetSetting("queue_pr_query"),
		QueueStoryQuery: cfg.GetSetting("queue_story_query"),
		QueuePRAgeDays:  cfg.GetSettingInt("queue_pr_age_days"),
		QueueLimit:      queueLimit,
	}
	c.prs = newPRPoller(c)

	pollers := []poller{c.prs}
	if cfg.GetSettingBool("queue_enabled") {
		c.stories = newStoryPoller(c)
		c.reviews = newReviewPoller(c)
		pollers = append(pollers, c.stories, c.reviews)
	}
	c.remote = newRemote(pollers...)
	return c
}
```

- [ ] **Step 5: Strengthen the daemon-fed budget test**

`TestADaemonFedClientSpendsNoGhBudget` (`internal/model/collect_cmd_test.go:1061`) is the test that proves a daemon-fed client's workers stay parked. It counts `gh` calls only, so the two new pollers are outside its assertion even though they sit behind the same nudge. Add `short` to it:

```go
	if got := cmd.CallCount("short"); got != 0 {
		t.Errorf("short called %d times by a daemon-fed client, want 0", got)
	}
```

**Do not otherwise weaken this test.** Per the collector async remote handoff it is one of only three that go through a real `Collector.Start`, and the only ones that would catch a nudge that never reaches a worker.

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./internal/collect/ ./internal/model/ -race
```

Expected: PASS. If any pre-existing collect test now fails, it is because the fixture's Commander has no `gh`/`short` handler for the new pollers - fix the fixture, not the production code.

- [ ] **Step 7: Commit**

```bash
git add internal/collect/queue.go internal/collect/queue_test.go internal/collect/collect.go
git commit -m "feat(collect): add the story and review pollers behind the remote seam"
```

---

### Task 5: `Collector.Queue`

**Files:**
- Modify: `internal/collect/collect.go`
- Modify: `internal/collect/queue_test.go`

**Interfaces:**
- Produces: `func (c *Collector) Queue(sessions []*session.Session) (items []session.QueueItem, hidden int)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/collect/queue_test.go`:

```go
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
	if items[0].Kind != session.QueueStory {
		t.Errorf("items[0].Kind = %q, want story: stories sort before reviews", items[0].Kind)
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
	c := seededCollector(t)
	c.QueueLimit = 1

	items, _ := c.Queue(nil)
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
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
```

Add `"github.com/jzinkduda/vigil/internal/session"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/collect/ -run "TestQueueReturns|TestQueueHides|TestQueueApplies|TestQueueIsNil" -v
```

Expected: build failure, `c.Queue undefined`.

- [ ] **Step 3: Implement**

Add to `internal/collect/collect.go` (add `"sort"` and `"strconv"` to imports):

```go
// Queue merges the two queue stores, drops anything a live tmux session
// already covers, sorts and caps. hidden is what this call removed, which is
// the only number vigil can honestly report: the queries filter server-side
// and vigil cannot see what they dropped.
//
// Pure over the stores plus sessions. Snapshot does not call it; the daemon
// and the self-polling client each call it once per poll, and a daemon-fed
// client never calls it at all.
func (c *Collector) Queue(sessions []*session.Session) ([]session.QueueItem, int) {
	if c.stories == nil && c.reviews == nil {
		return nil, 0
	}

	var all []session.QueueItem
	if c.stories != nil {
		all = append(all, c.stories.list()...)
	}
	if c.reviews != nil {
		all = append(all, c.reviews.list()...)
	}

	items := make([]session.QueueItem, 0, len(all))
	hidden := 0
	for _, it := range all {
		if coveredBySession(it, sessions) {
			hidden++
			continue
		}
		items = append(items, it)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == session.QueueStory
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})

	if c.QueueLimit > 0 && len(items) > c.QueueLimit {
		items = items[:c.QueueLimit]
	}
	if len(items) == 0 {
		return nil, hidden
	}
	return items, hidden
}

func coveredBySession(it session.QueueItem, sessions []*session.Session) bool {
	for _, s := range sessions {
		if it.MatchesSessionName(s.Name) {
			return true
		}
		if it.Kind == session.QueueReview && s.PR != nil && strconv.Itoa(s.PR.Number) == it.ID {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/collect/ -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/collect/collect.go internal/collect/queue_test.go
git commit -m "feat(collect): merge and dedup the queue against live sessions"
```

---

### Task 6: Protocol fields and daemon publication

**Files:**
- Modify: `internal/protocol/protocol.go:72-80` (`Snapshot`)
- Modify: `internal/daemon/daemon.go:240-280` (`poll`)
- Modify: `internal/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `Collector.Queue` (Task 5).
- Produces: `protocol.Snapshot.Queue []session.QueueItem`, `protocol.Snapshot.QueueHidden int`.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/daemon_test.go`:

```go
// TestPollPublishesTheQueue is the only thing tying Collector.Queue to what a
// client receives. Deleting the two lines in poll leaves every collect test
// green.
func TestPollPublishesTheQueue(t *testing.T) {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "alpha|0", nil)
	cmd.On("gh", `[{"number":34967,"repository":{"name":"portal"},"title":"Timeline tab",
		"updatedAt":"2026-07-31T18:54:14Z","url":"https://github.com/huntresslabs/portal/pull/34967"}]`, nil)
	cmd.On("short", `{"data":[]}`, nil)

	c := collect.New(&config.Config{}, cmd)
	s := &Server{Collector: c}

	ctx := context.Background()
	c.RefreshRemote(ctx)
	s.poll(ctx)

	s.mu.Lock()
	snap := s.latest
	s.mu.Unlock()

	if snap == nil {
		t.Fatal("poll published no snapshot")
	}
	if len(snap.Queue) != 1 {
		t.Fatalf("snapshot Queue has %d items, want 1", len(snap.Queue))
	}
	if snap.Queue[0].ID != "34967" {
		t.Errorf("Queue[0].ID = %q, want 34967", snap.Queue[0].ID)
	}
}

// TestSnapshotQueueIsOmittedWhenEmpty pins the additive-field contract that
// keeps protocol.Version at 1: an old client must see no key at all.
func TestSnapshotQueueIsOmittedWhenEmpty(t *testing.T) {
	data, err := json.Marshal(&protocol.Snapshot{Version: protocol.Version})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "queue") {
		t.Errorf("empty snapshot carries a queue key: %s", data)
	}
}
```

Add whatever imports the file is missing (`encoding/json`, `strings`, `collect`, `config`, `fetch`, `protocol`).

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/daemon/ -run "TestPollPublishesTheQueue|TestSnapshotQueueIsOmitted" -v
```

Expected: build failure, `snap.Queue undefined`.

- [ ] **Step 3: Add the protocol fields**

In `internal/protocol/protocol.go`, inside `Snapshot`, after `Jobs`:

```go
	// Queue is work waiting to be started: assigned stories and
	// review-requested PRs, already deduped against live sessions. Additive
	// for the same reason Jobs is, and Version stays 1.
	Queue []session.QueueItem `json:"queue,omitempty"`

	// QueueHidden counts items suppressed because a session already covers
	// them. Only what vigil itself dropped; the queries filter server-side
	// and their removals are not visible from here.
	QueueHidden int `json:"queue_hidden,omitempty"`
```

- [ ] **Step 4: Publish from `poll`**

In `internal/daemon/daemon.go`, in `poll`, between the `pollFailing` reset and the `snap :=` literal:

```go
	queue, queueHidden := s.Collector.Queue(sessions)
```

and add to the `protocol.Snapshot` literal:

```go
		Queue:       queue,
		QueueHidden: queueHidden,
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/daemon/ ./internal/protocol/ -race
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/protocol/protocol.go internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(protocol): publish the work queue in Snapshot, additively"
```

---

### Task 7: Detached dispatch end to end

The `{flags}` placeholder, `Request.Detached`, the daemon plumbing, and the migration warning. One task because a reviewer cannot sensibly approve any part of it without the rest.

**Files:**
- Modify: `internal/config/config.go:137-158` (`ExpandHook`), `:48-52` (`hookDefaults`)
- Modify: `internal/config/config_test.go`
- Modify: `internal/protocol/protocol.go:131-137` (`Request`)
- Modify: `internal/daemon/jobs.go:52-70`, `:92-137`, `:203-231`
- Modify: `internal/daemon/jobs_test.go`
- Modify: `internal/dispatch/submit.go:47-103`
- Modify: `main.go:114-136`
- Modify: `main_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: `config.ExpandHook` leaves `{flags}` unquoted; `protocol.Request.Detached bool`; `dispatch.Options.Detached bool`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
// TestExpandHookLeavesFlagsUnquoted pins the one placeholder that skips
// shellQuote. Quoted, an empty {flags} becomes '' and passes a stray empty
// argument to the hook.
func TestExpandHookLeavesFlagsUnquoted(t *testing.T) {
	tmpl := "dispatch --non-interactive {flags} {input}"

	got, err := ExpandHook(tmpl, map[string]string{"flags": "--detached", "input": "sc-1"})
	if err != nil {
		t.Fatalf("ExpandHook: %v", err)
	}
	if !strings.Contains(got, " --detached ") {
		t.Errorf("expanded = %q, want an unquoted --detached", got)
	}

	empty, err := ExpandHook(tmpl, map[string]string{"flags": "", "input": "sc-1"})
	if err != nil {
		t.Fatalf("ExpandHook: %v", err)
	}
	if strings.Contains(empty, "''") {
		t.Errorf("expanded = %q, want no empty quoted argument", empty)
	}
}

// TestExpandHookStillQuotesEverythingElse is the safety half. {flags} is safe
// unquoted because vigil chooses it from two constants; every other
// placeholder carries a value from tmux, git, gh or the user.
func TestExpandHookStillQuotesEverythingElse(t *testing.T) {
	got, err := ExpandHook("dispatch {input}", map[string]string{"input": "; rm -rf /"})
	if err != nil {
		t.Fatalf("ExpandHook: %v", err)
	}
	if !strings.Contains(got, `'; rm -rf /'`) {
		t.Errorf("expanded = %q, want the input shell-quoted", got)
	}
}
```

Add to `internal/daemon/jobs_test.go`:

```go
// TestDetachedJobPassesTheFlag and its sibling are what stop the daemon
// silently teleporting a queue selection.
func TestDetachedJobPassesTheFlag(t *testing.T) {
	for _, tt := range []struct {
		name     string
		detached bool
		want     string
	}{
		{"detached", true, "--detached"},
		{"attached", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream := newRecordingStream()
			cfg := &config.Config{Hooks: map[string]any{
				"dispatch": "echo {flags} {input}",
			}}
			j := newJobs(cfg, stream, fetch.NewMockCommander(), func(string, ...any) {})

			j.submit(&protocol.Request{
				Version:  protocol.Version,
				Type:     protocol.RequestDispatch,
				ID:       "job1",
				Input:    "sc-1",
				Detached: tt.detached,
			})
			j.run(context.Background(), "job1")

			script := stream.lastScript()
			if tt.want == "" {
				if strings.Contains(script, "--detached") {
					t.Errorf("script = %q, want no --detached", script)
				}
				return
			}
			if !strings.Contains(script, tt.want) {
				t.Errorf("script = %q, want it to contain %q", script, tt.want)
			}
		})
	}
}
```

**Use the existing `recordingStream` at `internal/daemon/jobs_test.go:504`** rather than writing a new fake. Read what it records first; if it does not already keep the argv, extend it to store the last `args` slice and add an accessor. The script body is the final argv element, because `hookArgv` builds `sh -c 'exec 2>&1; <hook>'`.

Add to `main_test.go`:

```go
func TestWarnsWhenTheDispatchHookHasNoFlagsPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{Hooks: map[string]any{
		"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {input}",
	}}

	warnAboutAnUnmigratedDispatchHook(cfg, &buf)

	if !strings.Contains(buf.String(), "{flags}") {
		t.Errorf("stderr = %q, want a warning naming {flags}", buf.String())
	}
}

func TestNoWarningWhenTheDispatchHookHasFlags(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{Hooks: map[string]any{
		"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}",
	}}

	warnAboutAnUnmigratedDispatchHook(cfg, &buf)

	if buf.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", buf.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/config/ -run TestExpandHook -v
go test ./internal/daemon/ -run TestDetachedJob -v
go test . -run TestWarns -v
```

Expected: config tests fail on the `''` assertion; daemon fails to build (`Detached` unknown field); main fails with no warning emitted.

- [ ] **Step 3: Implement the unquoted placeholder**

In `internal/config/config.go`, above `ExpandHook`:

```go
// rawPlaceholders are substituted without shell quoting. Every other
// placeholder carries a value from tmux, git, gh or the user, and quoting is
// what stops it reaching sh as syntax. {flags} carries one of exactly two
// constants chosen by vigil - "" or "--detached" - and quoting it would pass
// a stray empty argument to the hook.
var rawPlaceholders = map[string]bool{"flags": true}
```

and in the loop body replace the substitution line:

```go
		sub := val
		if !rawPlaceholders[key] {
			sub = shellQuote(val)
		}
		result = result[:start] + sub + result[end+1:]
```

Update `hookDefaults` - there is no `dispatch` default today, so add one:

```go
	"dispatch": "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}",
```

- [ ] **Step 4: Add `Request.Detached` and plumb it**

`internal/protocol/protocol.go`, in `Request`:

```go
	// Detached asks the workflow scripts to skip the teleport. Additive, so
	// an old daemon ignores it and dispatches attached - degrading to exactly
	// today's behaviour rather than erroring.
	Detached bool `json:"detached,omitempty"`
```

`internal/daemon/jobs.go`, in the `jobs` struct beside `cwds` (same rationale - a property of the request, not of the published job):

```go
	// detached holds each job's teleport preference, keyed by ID. Off
	// protocol.Job for the same reason cwds is.
	detached map[string]bool
```

In `newJobs`, add `detached: make(map[string]bool),`.

In `submit`, beside `j.cwds[req.ID] = req.Cwd`:

```go
	j.detached[req.ID] = req.Detached
```

In `run`, extend the state read and the hook vars:

```go
	input, cwd, detached := job.Input, j.cwds[id], j.detached[id]
	j.mu.Unlock()

	flags := ""
	if detached {
		flags = "--detached"
	}
	...
	err := j.cfg.RunHookStream(ctx, j.stream, "dispatch",
		map[string]string{"input": input, "flags": flags},
```

`internal/dispatch/submit.go`: add `Detached bool` to `Options` and `Detached: opts.Detached` to the `protocol.Request` literal.

- [ ] **Step 5: Extend the migration warning**

In `main.go`, at the end of `warnAboutAnUnmigratedDispatchHook`, after the existing loop:

```go
	if !strings.Contains(hook, "{flags}") {
		_, _ = fmt.Fprintf(stderr,
			"vigil: the dispatch hook has no {flags} placeholder, so selecting a queue "+
				"item will teleport instead of dispatching in the background. The hook "+
				"should be DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}\n")
	}
```

The existing `--detached`/`DISPATCH_IN_POPUP` loop returns early on a match, so a hook with a literal `--detached` gets that warning and not this one. That ordering is deliberate: a literal `--detached` is the phase 4 defect and is the more urgent of the two.

- [ ] **Step 6: Run the tests to verify they pass**

```bash
make test
```

Expected: PASS across all packages. `internal/dispatch` tests may need `Detached: false` added to expected request literals.

- [ ] **Step 7: Document the migration**

In `README.md`'s dispatch section, replace the recommended hook with:

```markdown
    dispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}"
```

and add:

> `{flags}` expands to `--detached` when the dispatch came from the work queue and to nothing otherwise. It is the one placeholder vigil does not shell-quote, because it carries a vigil-chosen constant rather than external data. A hook without it still works - queue selections just teleport - and vigil warns at startup.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/protocol/protocol.go internal/daemon/jobs.go internal/daemon/jobs_test.go internal/dispatch/submit.go main.go main_test.go README.md
git commit -m "feat(dispatch): detached jobs via an unquoted {flags} hook placeholder"
```

---

### Task 8: Rendering

**Files:**
- Create: `internal/view/queue.go`
- Create: `internal/view/queue_test.go`
- Modify: `internal/view/statusbar.go:13-14`, `:52-57`
- Modify: `internal/view/statusbar_test.go`

**Interfaces:**
- Produces: `view.RenderQueue(items []session.QueueItem, hidden, cursor, width int, now time.Time) string`; `view.RenderStatusBar` gains a trailing `queueCount int` parameter.

- [ ] **Step 1: Write the failing tests**

Create `internal/view/queue_test.go`:

```go
package view

import (
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

func queueFixture() []session.QueueItem {
	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	return []session.QueueItem{
		{Kind: session.QueueStory, ID: "223480", Title: "Backfill remediation audit rows",
			UpdatedAt: base.Add(-2 * time.Hour).Unix()},
		{Kind: session.QueueReview, ID: "34967", Repo: "portal", Title: "Partner facing incident report Timeline tab",
			UpdatedAt: base.Add(-26 * time.Hour).Unix()},
	}
}

func TestRenderQueueIsEmptyWithNoItems(t *testing.T) {
	if got := RenderQueue(nil, 0, -1, 120, time.Now()); got != "" {
		t.Errorf("RenderQueue(nil) = %q, want empty", got)
	}
}

func TestRenderQueueShowsLabelsAndTitles(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	got := RenderQueue(queueFixture(), 0, -1, 120, now)

	for _, want := range []string{"QUEUE", "sc-223480", "portal#34967", "Backfill remediation audit rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
}

func TestRenderQueueReportsHiddenCount(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	got := RenderQueue(queueFixture(), 3, -1, 120, now)
	if !strings.Contains(got, "3 in progress") {
		t.Errorf("render missing the hidden count:\n%s", got)
	}

	none := RenderQueue(queueFixture(), 0, -1, 120, now)
	if strings.Contains(none, "in progress") {
		t.Errorf("render claims a hidden count with hidden=0:\n%s", none)
	}
}

func TestRenderQueueShowsAge(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	got := RenderQueue(queueFixture(), 0, -1, 120, now)

	if !strings.Contains(got, "2h") {
		t.Errorf("render missing the 2h age:\n%s", got)
	}
	if !strings.Contains(got, "1d") {
		t.Errorf("render missing the 1d age:\n%s", got)
	}
}

// TestRenderQueueMarksTheCursor pins that the cursor is visible at all. Without
// it the section renders identically whether or not a row is selected, and the
// user cannot tell what enter would dispatch.
func TestRenderQueueMarksTheCursor(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	first := RenderQueue(queueFixture(), 0, 0, 120, now)
	second := RenderQueue(queueFixture(), 0, 1, 120, now)
	none := RenderQueue(queueFixture(), 0, -1, 120, now)

	if first == second {
		t.Error("cursor 0 and cursor 1 render identically")
	}
	if first == none {
		t.Error("cursor 0 and no cursor render identically")
	}
}
```

Add to `internal/view/statusbar_test.go`:

```go
func TestStatusBarShowsTheQueueBadge(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 120, "", 4)
	if !strings.Contains(got, "4") {
		t.Errorf("status bar missing the queue count:\n%s", got)
	}
}

func TestStatusBarOmitsTheBadgeWhenTheQueueIsEmpty(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 120, "", 0)
	if strings.Contains(got, "⚡") {
		t.Errorf("status bar shows a badge with an empty queue:\n%s", got)
	}
}

// TestStatusBarDropsTheBadgeWhenItDoesNotFit relies on addSegment's existing
// budget behaviour rather than a new guard.
func TestStatusBarDropsTheBadgeWhenItDoesNotFit(t *testing.T) {
	got := RenderStatusBar(nil, nil, session.SortCreated, 8, "", 4)
	if strings.Contains(got, "⚡") {
		t.Errorf("status bar kept the badge at width 8:\n%s", got)
	}
}
```

Every existing `RenderStatusBar` call in the test files needs a trailing `, 0`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/view/ -v
```

Expected: build failure, `undefined: RenderQueue` and wrong arity on `RenderStatusBar`.

- [ ] **Step 3: Write `internal/view/queue.go`**

```go
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// queueLabelWidth caps the id column. portal#34967 is 12; the cap keeps a
// pathological repo name from eating the title.
const queueLabelWidth = 20

// RenderQueue renders the work-waiting section below the session table.
// cursor is an index into items, or -1 when the cursor is on a session row -
// the Model owns the single cursor and does that translation, so this never
// has to know how many sessions precede it.
//
// hidden is what Collector.Queue removed because a session already covers it.
// It is deliberately not "N filtered": the queries filter server-side and
// those removals are not visible from here.
func RenderQueue(items []session.QueueItem, hidden, cursor, width int, now time.Time) string {
	if len(items) == 0 {
		return ""
	}

	header := fmt.Sprintf("QUEUE  %d", len(items))
	if hidden > 0 {
		header += fmt.Sprintf(" · %d in progress", hidden)
	}

	lines := []string{FaintOnBar().Render(header)}
	for i, it := range items {
		marker := "  "
		if i == cursor {
			marker = "> "
		}
		label := truncateVisible(it.Label(), queueLabelWidth)
		age := queueAge(it.UpdatedAt, now)

		fixed := len(marker) + queueLabelWidth + 2 + len(age) + 1
		titleWidth := width - fixed
		if titleWidth < 1 {
			titleWidth = 1
		}
		row := fmt.Sprintf("%s%-*s  %-*s %s",
			marker, queueLabelWidth, label, titleWidth, truncateVisible(it.Title, titleWidth), age)

		if i == cursor {
			row = CursorStyle.Render(row)
		}
		lines = append(lines, row)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func queueAge(updated int64, now time.Time) string {
	if updated == 0 {
		return ""
	}
	d := now.Sub(time.Unix(updated, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

```

`truncateVisible` (`internal/view/detail.go:131`) and `CursorStyle`
(`internal/view/styles.go:64`, the same `BarBg` background `renderRow` uses for the
cursor row) already exist. Use them; do not add a second truncation helper or a new
style. `visibleLen` lives in `internal/view/table.go:111` if you need it.

- [ ] **Step 4: Add the badge to the status bar**

In `internal/view/statusbar.go`, change the signature to end with `health string, queueCount int` and add, immediately after the `health` segment:

```go
	// Above the state counts: at a narrow width, "work is waiting" is worth
	// more than a per-state breakdown of what is already running.
	if queueCount > 0 {
		text := fmt.Sprintf("⚡%d", queueCount)
		addSegment(text, OnBar(BrightYellow).Render(text))
	}
```

Update the doc comment to name the new parameter.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/view/ -race
```

Expected: PASS. `internal/model` will not build yet; that is Task 9.

- [ ] **Step 6: Commit**

```bash
git add internal/view/
git commit -m "feat(view): render the queue section and the panel badge"
```

---

### Task 9: Model wiring

**Files:**
- Modify: `internal/model/messages.go:72-91` (`SnapshotMsg`)
- Modify: `internal/model/client.go:80-97` (`collectCmd`)
- Modify: `internal/model/model.go` - fields, `applySnapshot`, `View`, `panelView`, `handleSelect`, `dispatchCmd`, the j/k handlers
- Modify: `internal/model/model_test.go`

**Interfaces:**
- Consumes: `Collector.Queue`, `protocol.Snapshot.Queue`, `view.RenderQueue`, `dispatch.Options.Detached`.
- Produces: `Model.queue`, `Model.queueHidden`, `Model.queueCursor() int`, `Model.rowCount() int`; `dispatchCmd(input string, detached bool)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/model/model_test.go`:

```go
func modelWithQueue(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.sessions = []*session.Session{{Name: "alpha", PanePath: "/repo/alpha"}}
	m.queue = []session.QueueItem{
		{Kind: session.QueueStory, ID: "223480", Title: "Backfill", Input: "https://app.shortcut.com/huntress/story/223480"},
		{Kind: session.QueueReview, ID: "34967", Repo: "portal", Title: "Timeline", Input: "https://github.com/huntresslabs/portal/pull/34967"},
	}
	m.width, m.height = 120, 40
	return m
}

func TestRowCountSpansSessionsAndQueue(t *testing.T) {
	m := modelWithQueue(t)
	if got := m.rowCount(); got != 3 {
		t.Errorf("rowCount() = %d, want 3 (1 session + 2 queue)", got)
	}
}

func TestQueueCursorTranslatesFromTheGlobalCursor(t *testing.T) {
	m := modelWithQueue(t)
	tests := []struct{ cursor, want int }{
		{0, -1}, // the session row
		{1, 0},  // first queue row
		{2, 1},  // second queue row
	}
	for _, tt := range tests {
		m.cursor = tt.cursor
		if got := m.queueCursor(); got != tt.want {
			t.Errorf("cursor=%d: queueCursor() = %d, want %d", tt.cursor, got, tt.want)
		}
	}
}

// TestSelectedSessionIsNilOnAQueueRow is why session actions need no new
// guard: selectedSession already bounds-checks against visibleSessions, and
// every action handler goes through it or through batchSessions.
func TestSelectedSessionIsNilOnAQueueRow(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 1
	if s := m.selectedSession(); s != nil {
		t.Errorf("selectedSession() = %v on a queue row, want nil", s.Name)
	}
}

func TestBatchSessionsNeverIncludesQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.selected = map[string]bool{"sc-223480": true, "portal#34967": true, "alpha": true}
	batch := m.batchSessions()
	if len(batch) != 1 || batch[0].Name != "alpha" {
		t.Errorf("batchSessions() = %v, want just alpha", batch)
	}
}

// TestCursorWrapsOverQueueRows is what makes j/k reach the queue at all.
func TestCursorWrapsOverQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got := next.(Model).cursor; got != 0 {
		t.Errorf("cursor after j from the last queue row = %d, want 0", got)
	}

	m.cursor = 0
	prev, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got := prev.(Model).cursor; got != 2 {
		t.Errorf("cursor after k from the first session = %d, want 2", got)
	}
}

func TestViewRendersTheQueueSection(t *testing.T) {
	m := modelWithQueue(t)
	out := m.View()
	if !strings.Contains(out, "QUEUE") {
		t.Errorf("dashboard view has no QUEUE section:\n%s", out)
	}
	if !strings.Contains(out, "sc-223480") {
		t.Errorf("dashboard view missing a queue row:\n%s", out)
	}
}

// TestPanelShowsTheBadgeAndNoQueueRows is the measured constraint made
// executable: the panel is 9 rows with sessions already in them.
func TestPanelShowsTheBadgeAndNoQueueRows(t *testing.T) {
	m := modelWithQueue(t)
	m.panelMode = true
	m.width, m.height = 152, 9

	out := m.View()
	if !strings.Contains(out, "⚡2") {
		t.Errorf("panel missing the queue badge:\n%s", out)
	}
	if strings.Contains(out, "QUEUE") || strings.Contains(out, "sc-223480") {
		t.Errorf("panel rendered queue rows:\n%s", out)
	}
}

func TestApplySnapshotStoresTheQueue(t *testing.T) {
	m := newTestModel(t)
	m.applySnapshot(nil)
	if m.queue != nil {
		t.Errorf("queue = %v after a snapshot with none, want nil", m.queue)
	}
}
```

Add a test that `enter` on a queue row dispatches detached. `dispatchCmd` returns a `tea.Cmd` that dials a socket, so assert on the arguments rather than running it - extract the decision into a helper the test can call:

```go
// TestEnterOnAQueueRowDispatchesDetached pins both halves: the right input and
// the detached flag. Without the flag assertion, a regression to
// dispatchCmd(input, false) is silent and the user gets teleported.
func TestEnterOnAQueueRowDispatchesDetached(t *testing.T) {
	m := modelWithQueue(t)
	m.cursor = 2 // portal#34967

	input, detached, ok := m.queueDispatchTarget()
	if !ok {
		t.Fatal("queueDispatchTarget() reported no target on a queue row")
	}
	if input != "https://github.com/huntresslabs/portal/pull/34967" {
		t.Errorf("input = %q, want the item's URL", input)
	}
	if !detached {
		t.Error("detached = false, want true: a queue selection must not teleport")
	}

	m.cursor = 0
	if _, _, ok := m.queueDispatchTarget(); ok {
		t.Error("queueDispatchTarget() reported a target on a session row")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/model/ -v 2>&1 | head -30
```

Expected: build failure, `m.queue undefined`, `m.rowCount undefined`, `m.queueCursor undefined`, `m.queueDispatchTarget undefined`.

- [ ] **Step 3: Add the model state**

In `internal/model/model.go`'s `Model` struct, beside `sessions`:

```go
	queue       []session.QueueItem
	queueHidden int
```

In `internal/model/messages.go`, add to `SnapshotMsg`:

```go
	// Queue is work waiting to be started. Populated on both paths: from the
	// wire when a daemon feeds this client, from Collector.Queue when it
	// self-polls.
	Queue       []session.QueueItem
	QueueHidden int
```

In `internal/model/client.go`'s `collectCmd`, replace the success return:

```go
		annotateClientFlags(ctx, cmd, sessions, fallbackCurrent)
		queue, hidden := collector.Queue(sessions)
		return SnapshotMsg{Sessions: sessions, Epoch: epoch, Local: true, Queue: queue, QueueHidden: hidden}
```

Wherever the daemon-fed path builds a `SnapshotMsg` from a `protocol.Snapshot`, carry `Queue: snap.Queue, QueueHidden: snap.QueueHidden`.

In `handleSnapshot`, before or alongside `applySnapshot`:

```go
	m.queue = msg.Queue
	m.queueHidden = msg.QueueHidden
```

- [ ] **Step 4: Add the cursor helpers**

```go
// rowCount is the number of selectable rows: sessions first, then queue
// items. The cursor indexes this space, and selectedSession's existing bounds
// check against visibleSessions is what makes every session action a no-op on
// a queue row - no new guard, and batchSessions cannot reach one at all.
func (m Model) rowCount() int {
	return len(m.visibleSessions()) + len(m.queue)
}

// queueCursor is the cursor's index into m.queue, or -1 when it is on a
// session row.
func (m Model) queueCursor() int {
	i := m.cursor - len(m.visibleSessions())
	if i < 0 || i >= len(m.queue) {
		return -1
	}
	return i
}

// queueDispatchTarget reports what enter should dispatch, if anything.
func (m Model) queueDispatchTarget() (input string, detached bool, ok bool) {
	i := m.queueCursor()
	if i < 0 {
		return "", false, false
	}
	return m.queue[i].Input, true, true
}
```

- [ ] **Step 5: Extend the cursor movement and `enter`**

At `internal/model/model.go:643` and `:652` and `:744`, replace every `len(visible)` used as the cursor's modulus with `m.rowCount()`, guarding against zero:

```go
	if n := m.rowCount(); n > 0 {
		m.cursor = (m.cursor + 1) % n
	}
```

and the `k` arm:

```go
	if n := m.rowCount(); n > 0 {
		m.cursor = (m.cursor - 1 + n) % n
	}
```

Leave the `0`-`9` number-key handler indexing `visibleSessions()` - those keys switch to a session by index and have no meaning for a queue row.

In `handleSelect`, before the existing `s := m.selectedSession()`:

```go
	if input, detached, ok := m.queueDispatchTarget(); ok {
		return m, m.dispatchCmd(input, detached)
	}
```

- [ ] **Step 6: Thread `detached` through `dispatchCmd`**

Change the signature to `func (m Model) dispatchCmd(input string, detached bool) tea.Cmd`, add `Detached: detached,` to the `dispatch.Options` literal, and update the existing call in `handleDispatchKey` to `m.dispatchCmd(input, false)`.

- [ ] **Step 7: Render the section and the badge**

In `View`, after the `table :=` line:

```go
	queueSection := view.RenderQueue(m.queue, m.queueHidden, m.queueCursor(), m.width, time.Now())
```

add it to `parts` after `table` (before `jobLine`), and subtract its height when computing `tableHeight` so the footer still pins to the bottom:

```go
	tableHeight := m.tableHeight(jobLine != "") - lipgloss.Height(queueSection)
```

Guard `tableHeight` at a minimum of 1.

Pass `0` as the new `RenderStatusBar` argument in `View` (the section already reports it) and `len(m.queue)` in `panelView`.

- [ ] **Step 8: Run the tests to verify they pass**

```bash
make test
```

Expected: PASS across all 14 packages.

- [ ] **Step 9: Run the linter**

```bash
make lint
```

Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/model/
git commit -m "feat(model): render the queue, and dispatch a selection detached"
```

---

### Task 10: Real-machine verification

Every phase in this repo lands with measured evidence. This task produces it. **Do not skip it and do not report the phase complete without it.**

**Files:**
- Create: `.superpowers/sdd/2026-07-31-phase-5-work-queue/verification-results.md`

- [ ] **Step 1: Build an isolated daemon**

Run every daemon with its own `HOME` and `XDG_RUNTIME_DIR`, so it gets its own socket and config and the user's real daemon is never stopped and their workspace never touched.

```bash
make build
export VDIR=$(mktemp -d)
mkdir -p "$VDIR/home/.config/vigil" "$VDIR/run"
printf '[settings]\nqueue_enabled = "true"\n[hooks]\ndispatch = "DISPATCH_INLINE=1 dispatch --non-interactive {flags} {input}"\n' \
  > "$VDIR/home/.config/vigil/config.toml"
HOME="$VDIR/home" XDG_RUNTIME_DIR="$VDIR/run" GH_TOKEN="$(gh auth token)" \
  ./vigil daemon > "$VDIR/daemon.log" 2>&1 &
echo $! > "$VDIR/pid"
```

- [ ] **Step 2: Record what the queue actually contains**

Attach a client and capture one snapshot's queue. Record, in the results file:

- the number of items, split by kind
- which review-requested PRs were dropped by `queue_pr_age_days`, checked against `gh search prs --state=open --json number,updatedAt -- "review-requested:@me"`
- which items were hidden by dedup, checked against `tmux list-sessions -F '#{session_name}'`

The dedup number is the one that matters: at design time all three assigned stories had sessions. If `hidden` is 0 while sessions named `SC-*` exist, the cross-repo naming convention has drifted and the landmine has fired.

- [ ] **Step 3: Measure that the queue did not slow publication**

Time the first frame a client receives, the way the collector async remote work did. Expected: unchanged from `b722c73`, because the pollers are off `Snapshot` entirely. Record both numbers. A regression here means something was added to `Snapshot`, which this plan forbids.

- [ ] **Step 4: Confirm a daemon-fed panel spends no queue budget**

With the isolated daemon running, start `./vigil --panel` against the same `XDG_RUNTIME_DIR`, leave it for 90s (> `queue_interval`), and confirm the panel process issued no `gh search` or `short api` calls of its own. This is the property the whole no-ticker design rests on.

- [ ] **Step 5: Verify detached dispatch on a real item**

This creates a real worktree and a real tmux session in the user's workspace. **Ask the user before running it**, and let them pick the item.

With the cursor on a queue row, press `enter` and record:
- whether the tmux client stayed where it was (the whole point)
- how long until the new session appeared in the session list
- that the new item disappeared from the queue on the following poll, which is dedup working end to end

- [ ] **Step 6: Tear down**

```bash
kill "$(cat "$VDIR/pid")"
```

Confirm the daemon released its lock and unlinked its socket. Leave the user's real daemon alone throughout.

- [ ] **Step 7: Write the results and commit**

Record every number, and explicitly record **what was not verified**. Then:

```bash
git add .superpowers/sdd/2026-07-31-phase-5-work-queue/verification-results.md
git commit -m "docs: record the phase 5 verification results"
```

---

## Notes for whoever executes this

- **`main.go` already warns about a hook containing a literal `--detached`.** Phase 4 removed it deliberately. Task 7 adds a second, separate warning about a missing `{flags}` and must not merge the two conditions.
- **The user's `~/.config/vigil/config.toml` pins the `dispatch` hook**, so the new default in `hookDefaults` will not reach them. The startup warning is the whole migration path.
- **`internal/collect` fixtures will break in Task 4.** Any existing test whose `MockCommander` has a bare `cmd.On("gh", ...)` handler now also answers `gh search prs`. Where a test counts `gh` calls, that count changes. Fix the fixture; do not make the pollers conditional on something the test can turn off beyond `queue_enabled`.
- **Do not weaken `TestRunStartsTheRemoteWorkers`, `TestNewStartsTheRemoteWorkers` or `TestADaemonFedClientSpendsNoGhBudget`.** They are the only three tests that go through a real `Start`, and per the collector async remote handoff they are the only ones that would catch a nudge that never reaches a worker.
- If a test in this plan passes before its subject exists, **stop and report it** rather than moving on. That has happened ten times across two plans on this repo, and every instance was found by review rather than by writing the test.

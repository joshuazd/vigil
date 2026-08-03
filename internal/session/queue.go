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

	// Author is the PR's author login, so a review row says whose work it is.
	// Empty for stories: Shortcut carries a requester, but resolving it costs
	// a lookup per story and the column exists to answer "whose PR is this".
	Author string `json:"author,omitempty"`
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

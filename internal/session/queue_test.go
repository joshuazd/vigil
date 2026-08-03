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

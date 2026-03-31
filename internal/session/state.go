package session

// SessionState represents the derived state of a session.
type SessionState int

const (
	Attention  SessionState = iota // Bell active
	Blocked                        // CI fail or changes requested
	Unresolved                     // Unresolved review comments
	Mergeable                      // Approved + CI passing
	Approved                       // Approved + CI pending
	Pending                        // CI running, no review yet
	Review                         // PR open, CI passing, awaiting review
	Done                           // PR merged
	Idle                           // No PR / fallback
)

var stateNames = [...]string{
	Attention:  "attention",
	Blocked:    "blocked",
	Unresolved: "unresolved",
	Mergeable:  "mergeable",
	Approved:   "approved",
	Pending:    "pending",
	Review:     "review",
	Done:       "done",
	Idle:       "idle",
}

func (s SessionState) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// AllStates returns all SessionState values in priority order.
func AllStates() []SessionState {
	return []SessionState{
		Attention, Blocked, Unresolved, Mergeable,
		Approved, Pending, Review, Done, Idle,
	}
}

// StateStyle holds the display attributes for a session state.
type StateStyle struct {
	Color string
	Dot   string
}

var StateStyles = map[SessionState]StateStyle{
	Attention:  {"bright_yellow", "●"},
	Blocked:    {"bright_red", "●"},
	Unresolved: {"bright_red", "○"},
	Mergeable:  {"bright_green", "●"},
	Approved:   {"bright_green", "○"},
	Pending:    {"bright_yellow", "○"},
	Review:     {"cyan", "●"},
	Done:       {"dim", "●"},
	Idle:       {"", "·"},
}

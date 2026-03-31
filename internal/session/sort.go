package session

import "strings"

// SortMode determines how sessions are ordered.
type SortMode int

const (
	SortCreated SortMode = iota
	SortState
	SortAlpha
)

var sortNames = [...]string{
	SortCreated: "created",
	SortState:   "state",
	SortAlpha:   "alpha",
}

func (m SortMode) String() string {
	if int(m) < len(sortNames) {
		return sortNames[m]
	}
	return "unknown"
}

// AllSortModes returns all sort modes in cycle order.
func AllSortModes() []SortMode {
	return []SortMode{SortCreated, SortState, SortAlpha}
}

// SortSessions sorts a slice of sessions in place by the given mode.
func SortSessions(sessions []*Session, mode SortMode) {
	switch mode {
	case SortCreated:
		sortBy(sessions, func(a, b *Session) bool {
			return a.Created < b.Created
		})
	case SortState:
		sortBy(sessions, func(a, b *Session) bool {
			return a.State() < b.State()
		})
	case SortAlpha:
		sortBy(sessions, func(a, b *Session) bool {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
	}
}

// insertion sort — session lists are small (typically <30)
func sortBy(s []*Session, less func(a, b *Session) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

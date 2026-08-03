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
		// (Created, ID), not ID alone. #{session_created} is whole seconds, so
		// ties are common and used to fall through the stable sort to
		// ListSessions' alphabetical order - while ~/dotfiles' M-j/M-k order by
		// session_id, which is why they disagreed. Session ids are issued in
		// increasing order, so (Created, ID) is the same total order as pure
		// ID, and unlike pure ID it degrades to Created when ID is 0 rather
		// than hoisting every cache-hydrated session to the front.
		sortBy(sessions, func(a, b *Session) bool {
			if a.Created != b.Created {
				return a.Created < b.Created
			}
			return a.ID < b.ID
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

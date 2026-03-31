package model

import (
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

// TmuxTickMsg triggers a tmux metadata polling cycle.
type TmuxTickMsg time.Time

// GitTickMsg triggers a git polling cycle.
type GitTickMsg time.Time

// PRTickMsg triggers a PR polling cycle.
type PRTickMsg time.Time

// TmuxUpdatedMsg carries tmux session metadata (fast, no git/PR).
type TmuxUpdatedMsg struct {
	Sessions []*session.Session
}

// GitUpdatedMsg carries git status data for sessions.
type GitUpdatedMsg struct {
	GitData map[string]session.GitStatus
}

// PRUpdatedMsg carries refreshed PR data.
type PRUpdatedMsg struct {
	PRData map[string]*session.PRStatus
}

// PaneCapturedMsg carries captured pane output.
type PaneCapturedMsg struct {
	SessionName string
	Content     string
}

// ActionResultMsg reports the outcome of a single action.
type ActionResultMsg struct {
	Action  string
	Session string
	OK      bool
	Message string
}

// BatchResultMsg reports the outcome of a batch operation.
type BatchResultMsg struct {
	Action string
	OK     int
	Failed int
}

// NotifyMsg is a transient notification.
type NotifyMsg struct {
	Text     string
	Severity string // "info", "warning", "error"
}

// DetailRefreshMsg triggers a detail panel content refresh.
type DetailRefreshMsg struct{}

// Notification is a displayed toast message with expiry.
type Notification struct {
	Text     string
	Severity string
	Expires  time.Time
}

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

// RenderTickMsg triggers a repaint with no fetch work. The daemon path uses
// it to get the same 1s render cadence self-polling gets for free from
// TmuxTickMsg, so time-based rendering (like notification expiry) behaves
// the same whether or not a daemon is connected.
type RenderTickMsg time.Time

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

// DelayedPRRefreshMsg triggers a follow-up PR fetch after a short delay
// to catch GitHub API updates that lag behind the action.
type DelayedPRRefreshMsg struct{}

// DetailRefreshMsg triggers a detail panel content refresh.
type DetailRefreshMsg struct{}

// SnapshotMsg carries a full session snapshot received from the daemon,
// with per-client flags already resolved.
type SnapshotMsg struct {
	Sessions []*session.Session
}

// DaemonLostMsg reports that the daemon stream ended, so the TUI should
// resume self-polling.
type DaemonLostMsg struct{}

// ConfirmAction represents a pending destructive action awaiting confirmation.
type ConfirmAction int

const (
	ConfirmNone ConfirmAction = iota
	ConfirmMerge
	ConfirmBatchMerge
	ConfirmCleanup
	ConfirmBatchCleanup
)

// Notification is a displayed toast message with expiry.
type Notification struct {
	Text     string
	Severity string
	Expires  time.Time
}

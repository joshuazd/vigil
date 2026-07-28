package model

import (
	"net"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
)

// RenderTickMsg triggers a repaint with no fetch work. The daemon path uses
// it to get the same render cadence self-polling gets for free from
// CollectTickMsg, so time-based rendering (like notification expiry) behaves
// the same whether or not a daemon is connected.
type RenderTickMsg struct {
	Time  time.Time
	Epoch int
}

// CollectTickMsg paces the self-polling loop. One is scheduled per completed
// poll, so there is never a free-running ticker and never two polls at once.
type CollectTickMsg struct {
	Epoch int
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

// DelayedPRRefreshMsg triggers a follow-up forced poll after a short delay
// to catch GitHub API updates that lag behind the action.
type DelayedPRRefreshMsg struct{}

// DetailRefreshMsg triggers a detail panel content refresh.
type DetailRefreshMsg struct{}

// SnapshotMsg carries a full session snapshot, with per-client flags already
// resolved. Local says this client collected it itself rather than receiving it
// from a daemon, which is what makes this client the owner of the poll loop and
// therefore responsible for state-transition side effects.
type SnapshotMsg struct {
	Sessions []*session.Session
	Epoch    int
	Local    bool

	// Forced marks a poll that was requested out of band - the r key, or an
	// action result - rather than by the ambient tick chain. Only an ambient
	// poll's completion schedules the next tick: a forced poll that scheduled
	// one too would fork the chain and permanently double the poll rate.
	Forced bool
}

// DaemonLostMsg reports that the daemon stream ended, so the TUI should
// resume self-polling.
type DaemonLostMsg struct {
	Epoch int
}

// ProbeTickMsg schedules the next attempt to reach the daemon. It only fires
// while self-polling: reaching a daemon that came up is what keeps one poller
// serving many clients instead of N clients each spending the gh budget.
type ProbeTickMsg struct {
	Epoch int
}

// DaemonProbeResultMsg reports one dial attempt. A nil Conn means the dial
// failed and probing should continue.
type DaemonProbeResultMsg struct {
	Epoch   int
	Conn    net.Conn
	Decoder *protocol.Decoder
}

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

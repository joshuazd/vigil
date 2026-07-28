package model

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jzinkduda/vigil/internal/action"
	"github.com/jzinkduda/vigil/internal/cache"
	"github.com/jzinkduda/vigil/internal/collect"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/view"
)

const autoFocusCooldown = 15 * time.Second

// spawnCooldown is the floor between two attempts by one panel to start a
// daemon.
const spawnCooldown = 15 * time.Second

type Model struct {
	// Data
	sessions   []*session.Session
	prCache    map[string]*session.PRStatus
	prevStates map[string]session.SessionState

	// UI state
	cursor          int
	filterState     *session.SessionState
	sortMode        session.SortMode
	selected        map[string]bool
	detailOpen      bool
	detailMode      *view.DetailMode // nil = auto
	lastSessionName string
	paneContent     string

	// Confirmation
	confirmAction ConfirmAction
	confirmName   string

	// Dispatch
	dispatchActive bool
	dispatchInput  textinput.Model

	// Modes
	//
	// insideTmux says only that: vigil is running inside a tmux client. It is
	// not a mode by itself. Two very different surfaces set it - the `prefix v`
	// popup and a session panel - and conflating them is what made Enter close
	// the panel. When the question is "should acting here end the process?",
	// ask exitsAfterAction instead of testing this directly.
	insideTmux   bool
	initialLoad  bool
	cursorPlaced bool

	// Current session (detected at startup)
	currentSessionName string

	// Auto-focus
	lastManualNav time.Time

	// Layout
	width, height int

	// Config
	cfg *config.Config

	// Commander for subprocess calls
	cmd fetch.Commander

	// Daemon connection (nil when self-polling)
	daemonConn    net.Conn
	daemonDecoder *protocol.Decoder
	daemonReady   bool

	// lastSnapshot is when the most recent daemon snapshot was applied. A
	// daemon that is connected but silent is invisible without it.
	lastSnapshot time.Time

	// panelMode renders the compact per-session panel instead of the full
	// dashboard. Set by NewPanel.
	panelMode bool

	// lastSpawn is when this panel last tried to start a daemon.
	lastSpawn time.Time

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Notifications
	notifications []Notification

	// Help
	help help.Model

	// epoch identifies the current polling generation. Every switch between
	// daemon snapshots and self-polling bumps it, which retires the previous
	// generation's ticks and any snapshot or loss still in flight from it.
	epoch int

	// collector runs the self-polling path. Only collectCmd touches it, and
	// only one collectCmd is ever in flight, which is what keeps its memos
	// single-goroutine: Collector.Snapshot is not safe against reentry.
	collector *collect.Collector

	// pollInFlight is true from the moment startPoll issues a collectCmd
	// until that poll's SnapshotMsg is processed by handleSnapshot's Local
	// branch. startPoll is the only issuer of collectCmd, and it refuses to
	// issue a second one while this is true, which is what makes a forced
	// refresh (a user action, the Refresh key) safe to offer alongside the
	// ambient self-poll loop without ever reentering Collector.Snapshot.
	pollInFlight bool
}

// New creates a Model for the full dashboard.
func New(cfg *config.Config, cmd fetch.Commander) Model {
	return newModel(cfg, cmd, false)
}

// NewPanel creates a Model for a session's panel: a compact, always-on
// session list in a tmux pane. A panel starts the daemon if none is running,
// because a panel per session self-polling would multiply the gh budget by
// the number of open sessions. Startup races between panels are safe: the
// daemon serializes on an flock and every loser exits immediately.
func NewPanel(cfg *config.Config, cmd fetch.Commander) Model {
	return newModel(cfg, cmd, true)
}

func newModel(cfg *config.Config, cmd fetch.Commander, panel bool) Model {
	ctx, cancel := context.WithCancel(context.Background())

	// Detect current session eagerly so cursor placement doesn't jump
	currentSession := fetch.CurrentSession(ctx, cmd)

	ti := textinput.New()
	ti.Placeholder = "URL or identifier..."
	ti.CharLimit = 500

	insideTmux := os.Getenv("TMUX") != ""

	m := Model{
		currentSessionName: currentSession,
		prCache:            make(map[string]*session.PRStatus),
		prevStates:         make(map[string]session.SessionState),
		selected:           make(map[string]bool),

		insideTmux:  insideTmux,
		initialLoad: true,
		detailOpen:  !panel,
		panelMode:   panel,

		cfg:       cfg,
		cmd:       cmd,
		ctx:       ctx,
		cancel:    cancel,
		collector: collect.New(cfg, cmd),

		dispatchInput: ti,
		help:          help.New(),
	}

	// Load the cache synchronously, on both the daemon and self-polling
	// paths, so the first paint is never blank: the daemon may not have
	// completed a successful poll yet. Doing it here rather than as a command
	// keeps a stale cache out of handleTmuxUpdated, where it would rebuild
	// sessions from cached data and reset HasBell.
	if cached := cache.Load(cache.CachePath(), cfg.GetSettingDuration("cache_ttl")); cached != nil {
		m.sessions = cached
		for _, s := range cached {
			if s.Name == currentSession {
				s.IsCurrent = true
				break
			}
		}
		m.warmCaches()
		// The cache is written in tmux order, and nothing else sorts before the
		// first render, so without this the first frame ignores the configured
		// sort. placeCursor indexes into visibleSessions, so it sorts first.
		session.SortSessions(m.sessions, m.sortMode)
		m.placeCursor()
	}

	if conn, err := dialDaemon(protocol.SocketPath()); err == nil {
		// Bound the wait for the first snapshot: a daemon whose very first
		// poll failed has nothing to send and would otherwise leave a
		// connected client blocked in Next() forever. handleSnapshot clears
		// this deadline once the first snapshot arrives.
		_ = conn.SetReadDeadline(time.Now().Add(firstSnapshotTimeout))
		m.daemonConn = conn
		m.daemonDecoder = protocol.NewDecoder(conn)
	} else if panel {
		m.spawnDaemonOnce()
	}

	if m.daemonDecoder == nil {
		// Init is about to issue the first collectCmd directly (see below),
		// not through startPoll: Init has a value receiver and returns no
		// Model, so a mutation startPoll made inside it would land on a copy
		// the Bubble Tea runtime never sees, leaving pollInFlight false on
		// the model it actually holds. Set it here instead, before Init ever
		// runs, so a key press or action result arriving before the first
		// SnapshotMsg cannot slip a second collectCmd past startPoll's guard.
		m.pollInFlight = true
	}

	return m
}

// spawnDaemonOnce starts a daemon at most once every spawnCooldown, so a
// daemon that refuses to stay up cannot turn a panel into a fork loop.
func (m *Model) spawnDaemonOnce() {
	if time.Since(m.lastSpawn) < spawnCooldown {
		return
	}
	m.lastSpawn = time.Now()
	if err := daemonSpawner(); err != nil {
		m.addNotification("could not start daemon: "+err.Error(), "warning")
	}
}

// startPoll is the only place that issues a collectCmd. It refuses to issue a
// second one while pollInFlight is already true, which is what makes it safe
// for a forced refresh (an action result, the Refresh key) to ask for a poll
// alongside the ambient self-poll loop: at most one of them ever wins, and
// the loser gets nil rather than a second concurrent Collector.Snapshot call.
//
// It also refuses while a daemon is connected: that client has nothing of
// its own to poll for, since the daemon already polls at tmux_interval and
// pushes every snapshot, and this client cannot reach the daemon's memos to
// invalidate them anyway. Checking it here, once, replaces the daemon guard
// that used to be duplicated at every call site.
func (m *Model) startPoll(force bool) tea.Cmd {
	if m.daemonConn != nil || m.pollInFlight {
		return nil
	}
	m.pollInFlight = true
	return m.collectCmd(force)
}

// pollInterval paces the self-poll loop between one CollectTickMsg and the
// next, mirroring the daemon's own floor logic (internal/daemon/daemon.go)
// so a self-polling client refreshes tmux state no less often than a
// daemon-fed one would.
func (m Model) pollInterval() time.Duration {
	if d := m.cfg.GetSettingDuration("tmux_interval"); d > 0 {
		return d
	}
	return defaultPollInterval
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd

	if m.daemonDecoder != nil {
		// renderTickCmd keeps the 1s repaint heartbeat self-polling gets for
		// free from tmuxTickCmd, without triggering any fetch work, so
		// notification expiry (evaluated at render time) behaves the same on
		// both paths.
		cmds = append(cmds,
			listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch),
			renderTickCmd(1*time.Second, m.epoch),
		)
		return tea.Batch(cmds...)
	}

	// Not startPoll: newModel already set pollInFlight for this branch, since
	// Init cannot report its own mutation back to the runtime (see there).
	// Calling startPoll here would see that flag and refuse to poll at all.
	cmds = append(cmds, m.collectCmd(false), probeTickCmd(m.epoch))
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.dispatchActive {
			return m.handleDispatchKey(msg)
		}
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width
		return m, nil

	case SnapshotMsg:
		return m.handleSnapshot(msg)

	case CollectTickMsg:
		// Paces the self-poll loop: one is scheduled per completed local
		// poll, never a free-running ticker. Not redundant with startPoll's
		// own daemon check: an epoch mismatch means this tick belongs to a
		// retired generation and must not touch the current one at all,
		// which startPoll has no way to know on its own.
		if msg.Epoch != m.epoch {
			return m, nil
		}
		c := m.startPoll(false)
		return m, c

	case DaemonLostMsg:
		return m.handleDaemonLost(msg)

	case ProbeTickMsg:
		if msg.Epoch != m.epoch || m.daemonConn != nil {
			return m, nil
		}
		return m, dialDaemonCmd(protocol.SocketPath(), m.epoch)

	case DaemonProbeResultMsg:
		return m.handleProbeResult(msg)

	case RenderTickMsg:
		// Render-only heartbeat for the daemon path: Bubble Tea always
		// calls View() after Update, so this does nothing but keep the
		// screen repainting at the same 1s cadence self-polling gets from
		// tmuxTickCmd, which is what lets notification expiry (evaluated
		// in View against a 3s TTL) behave the same on both paths.
		//
		// Stop rescheduling once the daemon is gone (same nil check
		// handleDaemonLost leaves behind): self-polling already runs its
		// own tmuxTickCmd(1*time.Second) after falling back, and without
		// this guard the render tick would keep rescheduling itself
		// forever, leaving two independent 1s tickers running side by side
		// for the rest of the process's life. The epoch check retires it
		// when the daemon connection it belonged to has since been
		// replaced or torn down.
		if msg.Epoch != m.epoch || m.daemonDecoder == nil {
			return m, nil
		}
		return m, renderTickCmd(1*time.Second, m.epoch)

	case PaneCapturedMsg:
		if s := m.selectedSession(); s != nil && s.Name == msg.SessionName {
			m.paneContent = msg.Content
		}
		return m, nil

	case ActionResultMsg:
		severity := "info"
		if !msg.OK {
			severity = "error"
		}
		m.addNotification(msg.Message, severity)
		// startPoll's own daemon check would make this guard redundant on
		// its own, but it also gates delayedPRRefreshCmd: a daemon-fed
		// client has nothing to force now and nothing to follow up on in
		// 3s either, so the guard stays to skip both, not just the poll.
		if m.daemonConn != nil {
			return m, nil
		}
		c := tea.Batch(m.startPoll(true), delayedPRRefreshCmd())
		return m, c

	case BatchResultMsg:
		m.selected = make(map[string]bool)
		m.addNotification(fmt.Sprintf("%s: %d ok, %d failed", msg.Action, msg.OK, msg.Failed), "info")
		if m.daemonConn != nil {
			return m, nil
		}
		c := tea.Batch(m.startPoll(true), delayedPRRefreshCmd())
		return m, c

	case DelayedPRRefreshMsg:
		// Catches GitHub API updates that lagged behind the action's own
		// forced poll above. No explicit daemon guard needed: startPoll's
		// own check is the only thing this case would otherwise skip.
		c := m.startPoll(true)
		return m, c

	case NotifyMsg:
		m.addNotification(msg.Text, msg.Severity)
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.panelMode {
		return m.panelView()
	}

	// Status bar
	statusBar := view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width, m.daemonHealth())

	notif := m.activeNotification()

	// Table
	visible := m.visibleSessions()
	tableHeight := m.tableHeight()
	staleThreshold := m.cfg.GetSettingInt("stale_threshold")
	table := view.RenderTable(visible, m.cursor, m.selected, staleThreshold, m.width, tableHeight, notif)

	// Detail panel
	var detail string
	if m.detailOpen {
		detailHeight := m.detailHeight()
		s := m.selectedSession()
		mode := m.activeDetailMode()
		detail = view.RenderDetail(s, mode, m.paneContent, staleThreshold, m.width, detailHeight)
	}

	// Footer
	footer := m.renderFooter()

	// Dispatch input
	var dispatch string
	if m.dispatchActive {
		dispatch = m.dispatchInput.View()
	}

	// Compose — pin footer to bottom by padding with blank lines
	parts := []string{statusBar, table}
	if detail != "" {
		parts = append(parts, detail)
	}
	if dispatch != "" {
		parts = append(parts, dispatch)
	}

	// Count lines used so far
	usedLines := 0
	for _, p := range parts {
		usedLines += lipgloss.Height(p)
	}
	footerLines := 1
	gap := m.height - usedLines - footerLines
	if gap > 0 {
		parts = append(parts, strings.Repeat("\n", gap-1))
	}

	parts = append(parts, footer)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// panelView renders the compact panel: a status bar and as many session rows
// as the pane has left. No footer and no detail panel - the rows are what the
// pane is for, and the detail panel's pane captures would run once per panel
// per tick.
func (m Model) panelView() string {
	statusBar := view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width, m.daemonHealth())
	table := view.RenderTable(
		m.visibleSessions(),
		m.cursor,
		m.selected,
		m.cfg.GetSettingInt("stale_threshold"),
		m.width,
		max(1, m.height-1),
		m.activeNotification(),
	)
	return lipgloss.JoinVertical(lipgloss.Left, statusBar, table)
}

// activeNotification returns the newest unexpired notification, styled, or
// "". Expiry is evaluated at render time, which is why both paths keep a 1s
// repaint cadence.
func (m Model) activeNotification() string {
	now := time.Now()
	for i := len(m.notifications) - 1; i >= 0; i-- {
		n := m.notifications[i]
		if now.Before(n.Expires) {
			style := lipgloss.NewStyle().Padding(0, 1)
			switch n.Severity {
			case "error":
				style = style.Foreground(view.BrightRed)
			case "warning":
				style = style.Foreground(view.BrightYellow)
			default:
				// default foreground — no explicit color
			}
			return style.Render(n.Text)
		}
	}
	return ""
}

// --- Key handling ---

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		m.cancel()
		return m, tea.Quit

	case key.Matches(msg, keys.Down):
		m.lastManualNav = time.Now()
		visible := m.visibleSessions()
		if len(visible) > 0 {
			m.cursor = (m.cursor + 1) % len(visible)
		}
		m.resetDetailModeIfSessionChanged()
		return m, m.refreshDetailCmd()

	case key.Matches(msg, keys.Up):
		m.lastManualNav = time.Now()
		visible := m.visibleSessions()
		if len(visible) > 0 {
			m.cursor = (m.cursor - 1 + len(visible)) % len(visible)
		}
		m.resetDetailModeIfSessionChanged()
		return m, m.refreshDetailCmd()

	case key.Matches(msg, keys.Select):
		return m.handleSelect()

	case key.Matches(msg, keys.OpenPR):
		return m.handleOpenPR()

	case key.Matches(msg, keys.MergePR):
		return m.handleMerge()

	case key.Matches(msg, keys.ApprovePR):
		return m.handleApprove()

	case key.Matches(msg, keys.Cleanup):
		return m.handleCleanup()

	case key.Matches(msg, keys.ToggleDraft):
		return m.handleToggleDraft()

	case key.Matches(msg, keys.RebasePush):
		return m.handleRebasePush()

	case key.Matches(msg, keys.Dispatch):
		m.dispatchActive = true
		m.dispatchInput.Focus()
		return m, textinput.Blink

	case key.Matches(msg, keys.Refresh):
		// startPoll's daemon check covers "nothing to force while a daemon
		// is connected"; its single-flight guard covers "already polling" -
		// either wins and invalidates the memos before the next Snapshot, or
		// loses silently to a poll already in flight.
		c := m.startPoll(true)
		return m, c

	case key.Matches(msg, keys.ToggleDetail):
		if m.panelMode {
			return m, nil
		}
		m.detailOpen = !m.detailOpen
		if m.detailOpen {
			return m, m.refreshDetailCmd()
		}
		return m, nil

	case key.Matches(msg, keys.CycleFilter):
		m.cycleFilter(1)
		m.cursor = 0
		return m, nil

	case key.Matches(msg, keys.CycleFilterBack):
		m.cycleFilter(-1)
		m.cursor = 0
		return m, nil

	case key.Matches(msg, keys.CycleDetailMode):
		mode := m.activeDetailMode()
		next := view.NextDetailMode(mode)
		m.detailMode = &next
		if !m.detailOpen {
			m.detailOpen = true
		}
		return m, m.refreshDetailCmd()

	case key.Matches(msg, keys.CycleSort):
		m.cycleSort(1)
		return m, nil

	case key.Matches(msg, keys.CycleSortBack):
		m.cycleSort(-1)
		return m, nil

	case key.Matches(msg, keys.ToggleSelect):
		s := m.selectedSession()
		if s != nil {
			if m.selected[s.Name] {
				delete(m.selected, s.Name)
			} else {
				m.selected[s.Name] = true
			}
			// Move cursor down
			visible := m.visibleSessions()
			if len(visible) > 0 {
				m.cursor = (m.cursor + 1) % len(visible)
			}
		}
		return m, nil

	case key.Matches(msg, keys.Cancel):
		if m.confirmAction != ConfirmNone || len(m.selected) > 0 || m.dispatchActive {
			m.confirmAction = ConfirmNone
			m.confirmName = ""
			m.selected = make(map[string]bool)
			m.dispatchActive = false
			return m, nil
		}
		m.cancel()
		return m, tea.Quit
	}

	// Number keys 0-9: switch to session by index
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '0' && r <= '9' {
			idx := int(r - '0')
			visible := m.visibleSessions()
			if idx < len(visible) {
				m.cursor = idx
				return m.handleSelect()
			}
			return m, nil
		}
	}

	return m, nil
}

func (m Model) handleDispatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		input := m.dispatchInput.Value()
		m.dispatchActive = false
		m.dispatchInput.SetValue("")
		return m, m.dispatchCmd(input)
	case tea.KeyEsc:
		m.dispatchActive = false
		m.dispatchInput.SetValue("")
		return m, nil
	}
	var cmd tea.Cmd
	m.dispatchInput, cmd = m.dispatchInput.Update(msg)
	return m, cmd
}

// --- Action handlers ---

// exitsAfterAction reports whether acting on a session should end this
// process. True only for the `prefix v` popup, which exists to be dismissed:
// you open it, pick something, and it gets out of the way. A panel is the
// opposite - a persistent surface whose pane is set remain-on-exit off, so
// quitting would delete the panel every time it was used for the one thing it
// is for.
func (m Model) exitsAfterAction() bool {
	return m.insideTmux && !m.panelMode
}

func (m Model) handleSelect() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil {
		return m, nil
	}
	if m.insideTmux {
		switchTo := func() tea.Msg {
			_ = fetch.SwitchClient(context.Background(), m.cmd, s.Name)
			return nil
		}
		if !m.exitsAfterAction() {
			// Switching is the whole point of a panel, so it has to survive
			// doing so.
			return m, switchTo
		}
		m.cancel()
		return m, tea.Sequence(switchTo, tea.Quit)
	}
	m.detailOpen = !m.detailOpen
	if m.detailOpen {
		return m, m.refreshDetailCmd()
	}
	return m, nil
}

func (m Model) handleOpenPR() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || s.PR == nil || s.PR.URL == "" {
		return m, nil
	}
	if err := action.OpenPRInBrowser(m.ctx, m.cmd, s.PR.URL); err != nil {
		m.addNotification("open: "+err.Error(), "error")
		return m, nil
	}
	if m.exitsAfterAction() {
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleMerge() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		if m.confirmAction == ConfirmBatchMerge {
			m.confirmAction = ConfirmNone
			return m, m.batchMergeCmd()
		}
		m.confirmAction = ConfirmBatchMerge
		m.addNotification(fmt.Sprintf("Press m again to merge %d PRs", len(m.selected)), "warning")
		return m, nil
	}
	s := m.selectedSession()
	if s == nil || s.PR == nil || s.PR.Number == 0 {
		m.addNotification("No PR for this session", "warning")
		return m, nil
	}
	if m.confirmAction == ConfirmMerge && m.confirmName == s.Name {
		m.confirmAction = ConfirmNone
		m.confirmName = ""
		return m, m.mergeCmd(s)
	}
	m.confirmAction = ConfirmMerge
	m.confirmName = s.Name
	m.addNotification("Press m again to merge", "warning")
	return m, nil
}

func (m Model) handleApprove() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		return m, m.batchApproveCmd()
	}
	s := m.selectedSession()
	if s == nil || s.PR == nil || s.PR.Number == 0 {
		m.addNotification("No PR for this session", "warning")
		return m, nil
	}
	return m, m.approveCmd(s)
}

func (m Model) handleCleanup() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		if m.confirmAction == ConfirmBatchCleanup {
			m.confirmAction = ConfirmNone
			return m, m.batchCleanupCmd()
		}
		m.confirmAction = ConfirmBatchCleanup
		m.addNotification(fmt.Sprintf("Press x again to cleanup %d sessions", len(m.selected)), "warning")
		return m, nil
	}
	s := m.selectedSession()
	if s == nil {
		return m, nil
	}
	if m.confirmAction == ConfirmCleanup && m.confirmName == s.Name {
		m.confirmAction = ConfirmNone
		m.confirmName = ""
		return m, m.cleanupCmd(s)
	}
	m.confirmAction = ConfirmCleanup
	m.confirmName = s.Name
	m.addNotification("Press x again to cleanup", "warning")
	return m, nil
}

func (m Model) handleToggleDraft() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		return m, m.batchToggleDraftCmd()
	}
	s := m.selectedSession()
	if s == nil || s.PR == nil || s.PR.Number == 0 {
		m.addNotification("No PR for this session", "warning")
		return m, nil
	}
	return m, m.toggleDraftCmd(s)
}

func (m Model) handleRebasePush() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		return m, m.batchRebaseCmd()
	}
	s := m.selectedSession()
	if s == nil || s.Git.GitRoot == "" {
		return m, nil
	}
	// Prevent rebasing default branch
	defaultBranch := fetch.DetectDefaultBranch(m.ctx, m.cmd, s.Git.GitRoot)
	if s.Git.Branch == defaultBranch {
		m.addNotification("Cannot rebase default branch", "warning")
		return m, nil
	}
	return m, m.rebaseCmd(s)
}

// --- Data handlers ---

// warmCaches records each session's PR state by branch, and backfills any
// session missing PR data from the last known value for its branch. The
// backfill keeps a single failed gh call from blanking the PR column and
// flipping the session to idle, which would fire a state transition
// notification and the notify hook.
func (m *Model) warmCaches() {
	for _, s := range m.sessions {
		if s.Git.Branch == "" {
			continue
		}
		if s.PR != nil {
			m.prCache[s.Git.Branch] = s.PR
		} else if pr, ok := m.prCache[s.Git.Branch]; ok {
			s.PR = pr
		}
	}
}

// placeCursor points the cursor at the current session, once, in popup mode.
func (m *Model) placeCursor() {
	if m.cursorPlaced || !m.insideTmux || m.currentSessionName == "" {
		return
	}
	for i, s := range m.visibleSessions() {
		if s.Name == m.currentSessionName {
			m.cursor = i
			m.cursorPlaced = true
			break
		}
	}
}

func (m Model) handleSnapshot(msg SnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.Local {
		// The poll goroutine that produced this message has finished,
		// whichever generation issued it - pollInFlight tracks a running
		// goroutine, not a generation, so this must be cleared before the
		// epoch check below, unconditionally. handleDaemonLost and
		// handleProbeResult's epoch bumps deliberately do not touch this
		// flag themselves: an epoch bump does not mean the poll it retires
		// has actually stopped running, and clearing the flag there instead
		// of here let a stale straggler's completion race a wrongly-started
		// second poll against the same Collector.
		m.pollInFlight = false

		if msg.Epoch != m.epoch {
			// A retired generation's data is not applied. But if this
			// client is self-polling now, this straggler was the only thing
			// that could ever set pollInFlight back to false, and
			// handleDaemonLost's startPoll call was refused while it was in
			// flight - so restart the loop for the current generation here,
			// or the fallback is wedged for the rest of the process's life.
			// startPoll's own daemon check makes this a no-op if a daemon
			// has taken over instead.
			c := m.startPoll(false)
			return m, c
		}

		// A failed poll carries no sessions. Leave state alone rather than
		// blanking the table.
		if msg.Sessions != nil {
			m.applySnapshot(msg.Sessions)
		}
		cmds := m.checkStateTransitions()
		if m.detailOpen {
			cmds = append(cmds, m.refreshDetailCmd())
		}
		// Only the ambient chain schedules its own continuation. A forced
		// poll (Refresh key, an action result) is an extra poll layered on
		// top of that chain, not a replacement link in it: the tick that was
		// already pending for the ambient poll still fires on its own, so a
		// forced poll scheduling one too would fork the chain into two
		// self-perpetuating ticks - and every further forced poll would fork
		// it again, permanently doubling the rate each time.
		if !msg.Forced {
			cmds = append(cmds, collectTickCmd(m.pollInterval(), m.epoch))
		}
		return m, tea.Batch(cmds...)
	}

	if msg.Epoch != m.epoch {
		return m, nil
	}
	if m.daemonConn == nil && m.daemonDecoder == nil {
		return m, nil
	}
	if !m.daemonReady {
		if m.daemonConn != nil {
			_ = m.daemonConn.SetReadDeadline(time.Time{})
		}
		m.daemonReady = true
	}
	m.lastSnapshot = time.Now()
	m.applySnapshot(msg.Sessions)

	cmds := m.checkStateTransitions()
	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}
	cmds = append(cmds, listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch))
	return m, tea.Batch(cmds...)
}

// applySnapshot installs a set of sessions and everything derived from them.
// Both paths funnel through here so neither can grow its own idea of what a
// snapshot implies.
func (m *Model) applySnapshot(sessions []*session.Session) {
	m.sessions = sessions
	m.warmCaches()
	session.SortSessions(m.sessions, m.sortMode)
	m.placeCursor()

	snap := make([]*session.Session, len(m.sessions))
	copy(snap, m.sessions)
	go func() { _ = cache.Save(cache.CachePath(), snap) }()
}

func (m Model) handleDaemonLost(msg DaemonLostMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.epoch {
		return m, nil
	}
	if m.daemonConn == nil && m.daemonDecoder == nil {
		// Should be unreachable: exactly one listenDaemonCmd is ever in
		// flight, and it is the one whose error produced this message.
		// Guarded anyway so a future call site cannot silently double
		// every poll loop by re-running the fallback commands below.
		return m, nil
	}
	if m.daemonConn != nil {
		_ = m.daemonConn.Close()
		m.daemonConn = nil
		m.daemonDecoder = nil
	}
	m.epoch++
	// pollInFlight is intentionally left untouched: it tracks whether a
	// Snapshot goroutine is outstanding, not which generation started it, and
	// an epoch bump does not mean one isn't. If a poll from the retired
	// generation is still running, startPoll below correctly refuses to
	// start a second one on top of it; handleSnapshot's Local branch is what
	// restarts the loop once that straggler lands, whichever epoch it was
	// for. See the comment there for why touching this flag here instead
	// used to open a race between two concurrent Collector.Snapshot calls.
	m.daemonReady = false
	m.addNotification("daemon lost, polling directly", "warning")
	c := tea.Batch(m.startPoll(false), probeTickCmd(m.epoch))
	return m, c
}

// handleProbeResult installs a reconnected daemon, or keeps probing. Bumping
// the epoch is what retires the self-poll loops that were running while the
// daemon was away.
func (m Model) handleProbeResult(msg DaemonProbeResultMsg) (tea.Model, tea.Cmd) {
	if msg.Conn == nil {
		if msg.Epoch != m.epoch || m.daemonConn != nil {
			return m, nil
		}
		if m.panelMode {
			m.spawnDaemonOnce()
		}
		return m, probeTickCmd(m.epoch)
	}
	if msg.Epoch != m.epoch || m.daemonConn != nil {
		// Retired generation, or a connection we no longer need. Dropping it
		// on the floor would leak an fd and a daemon-side writer goroutine.
		_ = msg.Conn.Close()
		return m, nil
	}

	// Bound the wait for the first snapshot exactly as New does: a daemon
	// whose poll is failing has nothing to send, and handleSnapshot clears
	// the deadline once something arrives.
	_ = msg.Conn.SetReadDeadline(time.Now().Add(firstSnapshotTimeout))
	m.epoch++
	// pollInFlight is not reset here either, and for the same reason as
	// handleDaemonLost: it tracks a goroutine, not this generation. A
	// self-poll that was in flight when this reconnect happened stays true
	// until it actually lands in handleSnapshot, which is what is now
	// responsible for restarting the loop if this client falls back again
	// before that straggler arrives.
	m.daemonConn = msg.Conn
	m.daemonDecoder = msg.Decoder
	m.daemonReady = false
	m.addNotification("daemon back, streaming snapshots", "info")

	return m, tea.Batch(
		listenDaemonCmd(m.daemonDecoder, m.ctx, m.cmd, m.currentSessionName, m.epoch),
		renderTickCmd(1*time.Second, m.epoch),
	)
}

// daemonHealth describes the state of the data source, for the status bar.
// Empty means nothing worth saying: either the daemon is feeding us or the
// TUI is self-polling, which is a supported mode and already announced by a
// notification when it starts. A panel says so out loud, because N panels
// self-polling is the one arrangement that actually costs something.
func (m Model) daemonHealth() string {
	if m.daemonConn == nil {
		if m.panelMode {
			return "no daemon"
		}
		return ""
	}
	if !m.daemonReady {
		return ""
	}
	if age := time.Since(m.lastSnapshot); age > m.staleAfter() {
		return fmt.Sprintf("daemon stale %ds", int(age.Seconds()))
	}
	return ""
}

// staleAfter is how long a connected daemon may stay silent before the status
// bar says so: three poll cycles, never less than 5s.
func (m Model) staleAfter() time.Duration {
	d := 3 * m.cfg.GetSettingDuration("tmux_interval")
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// --- Commands ---

// renderTickCmd triggers a repaint with no fetch work, so the daemon path
// gets the same render cadence self-polling gets for free from its own
// tight collectCmd loop.
func renderTickCmd(interval time.Duration, epoch int) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return RenderTickMsg{Time: t, Epoch: epoch}
	})
}

// delayedPRRefreshCmd triggers a follow-up forced poll a few seconds after an
// action, to catch GitHub API updates (merge, review state) that lag behind
// the action's own immediate refresh.
func delayedPRRefreshCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return DelayedPRRefreshMsg{}
	})
}

func (m Model) refreshDetailCmd() tea.Cmd {
	s := m.selectedSession()
	if s == nil {
		return nil
	}
	mode := m.activeDetailMode()
	if mode != view.DetailPane {
		return nil
	}
	name := s.Name
	window := m.cfg.GetSetting("capture_window")
	return func() tea.Msg {
		lines := 20
		content := fetch.CapturePane(m.ctx, m.cmd, name, lines, window)
		return PaneCapturedMsg{SessionName: name, Content: content}
	}
}

func (m Model) mergeCmd(s *session.Session) tea.Cmd {
	gitRoot := s.Git.GitRoot
	branch := s.Git.Branch
	name := s.Name
	return func() tea.Msg {
		out, err := action.MergePR(context.Background(), m.cfg, m.cmd, gitRoot, branch)
		if err != nil {
			return ActionResultMsg{Action: "merge", Session: name, OK: false, Message: out}
		}
		return ActionResultMsg{Action: "merge", Session: name, OK: true, Message: out}
	}
}

func (m Model) approveCmd(s *session.Session) tea.Cmd {
	gitRoot := s.Git.GitRoot
	branch := s.Git.Branch
	name := s.Name
	return func() tea.Msg {
		out, err := action.ApprovePR(context.Background(), m.cfg, m.cmd, gitRoot, branch)
		if err != nil {
			return ActionResultMsg{Action: "approve", Session: name, OK: false, Message: out}
		}
		return ActionResultMsg{Action: "approve", Session: name, OK: true, Message: out}
	}
}

func (m Model) cleanupCmd(s *session.Session) tea.Cmd {
	name := s.Name
	path := s.PanePath
	branch := s.Git.Branch
	gitRoot := s.Git.GitRoot
	return func() tea.Msg {
		out, err := action.CleanupSession(context.Background(), m.cfg, m.cmd, name, path, branch, gitRoot)
		if err != nil {
			return ActionResultMsg{Action: "cleanup", Session: name, OK: false, Message: out}
		}
		return ActionResultMsg{Action: "cleanup", Session: name, OK: true, Message: out}
	}
}

func (m Model) rebaseCmd(s *session.Session) tea.Cmd {
	gitRoot := s.Git.GitRoot
	name := s.Name
	return func() tea.Msg {
		out, err := action.RebaseAndPush(context.Background(), m.cmd, gitRoot)
		if err != nil {
			return ActionResultMsg{Action: "rebase", Session: name, OK: false, Message: out}
		}
		return ActionResultMsg{Action: "rebase", Session: name, OK: true, Message: out}
	}
}

func (m Model) toggleDraftCmd(s *session.Session) tea.Cmd {
	gitRoot := s.Git.GitRoot
	branch := s.Git.Branch
	isDraft := s.PR.IsDraft
	name := s.Name
	return func() tea.Msg {
		out, err := action.ToggleDraft(context.Background(), m.cmd, gitRoot, branch, isDraft)
		if err != nil {
			return ActionResultMsg{Action: "draft", Session: name, OK: false, Message: out}
		}
		return ActionResultMsg{Action: "draft", Session: name, OK: true, Message: out}
	}
}

func (m Model) dispatchCmd(input string) tea.Cmd {
	return func() tea.Msg {
		out, err := action.Dispatch(context.Background(), m.cfg, m.cmd, input)
		if err != nil {
			return ActionResultMsg{Action: "dispatch", Session: "", OK: false, Message: out}
		}
		return ActionResultMsg{Action: "dispatch", Session: "", OK: true, Message: out}
	}
}

// --- Batch commands ---

func (m Model) batchMergeCmd() tea.Cmd {
	sessions := m.batchSessions()
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, s := range sessions {
			_, err := action.MergePR(context.Background(), m.cfg, m.cmd, s.Git.GitRoot, s.Git.Branch)
			if err != nil {
				fail++
			} else {
				ok++
			}
		}
		return BatchResultMsg{Action: "merge", OK: ok, Failed: fail}
	}
}

func (m Model) batchApproveCmd() tea.Cmd {
	sessions := m.batchSessions()
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, s := range sessions {
			_, err := action.ApprovePR(context.Background(), m.cfg, m.cmd, s.Git.GitRoot, s.Git.Branch)
			if err != nil {
				fail++
			} else {
				ok++
			}
		}
		return BatchResultMsg{Action: "approve", OK: ok, Failed: fail}
	}
}

func (m Model) batchCleanupCmd() tea.Cmd {
	sessions := m.batchSessions()
	// Put current session last so switch-away finds a surviving session.
	sort.SliceStable(sessions, func(i, j int) bool {
		return !sessions[i].IsCurrent && sessions[j].IsCurrent
	})
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, s := range sessions {
			_, err := action.CleanupSession(context.Background(), m.cfg, m.cmd, s.Name, s.PanePath, s.Git.Branch, s.Git.GitRoot)
			if err != nil {
				fail++
			} else {
				ok++
			}
		}
		return BatchResultMsg{Action: "cleanup", OK: ok, Failed: fail}
	}
}

func (m Model) batchRebaseCmd() tea.Cmd {
	sessions := m.batchSessions()
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, s := range sessions {
			_, err := action.RebaseAndPush(context.Background(), m.cmd, s.Git.GitRoot)
			if err != nil {
				fail++
			} else {
				ok++
			}
		}
		return BatchResultMsg{Action: "rebase", OK: ok, Failed: fail}
	}
}

func (m Model) batchToggleDraftCmd() tea.Cmd {
	sessions := m.batchSessions()
	return func() tea.Msg {
		ok, fail := 0, 0
		for _, s := range sessions {
			if s.PR == nil {
				continue
			}
			_, err := action.ToggleDraft(context.Background(), m.cmd, s.Git.GitRoot, s.Git.Branch, s.PR.IsDraft)
			if err != nil {
				fail++
			} else {
				ok++
			}
		}
		return BatchResultMsg{Action: "draft", OK: ok, Failed: fail}
	}
}

// --- State transitions ---

func (m *Model) checkStateTransitions() []tea.Cmd {
	var cmds []tea.Cmd
	notificationsEnabled := m.cfg.GetSettingBool("notifications_enabled")
	autoCleanup := m.cfg.GetSettingBool("auto_cleanup")

	for _, s := range m.sessions {
		newState := s.State()
		oldState, existed := m.prevStates[s.Name]

		if !existed {
			m.prevStates[s.Name] = newState
			continue
		}

		if oldState == newState {
			continue
		}

		m.prevStates[s.Name] = newState

		if m.initialLoad {
			continue
		}

		// Notification
		if notificationsEnabled {
			m.addNotification(fmt.Sprintf("%s → %s", s.Name, newState), notifSeverity(newState))

			// Notify hook
			hookVars := map[string]string{
				"session":   s.Name,
				"old_state": oldState.String(),
				"new_state": newState.String(),
			}
			cfg := m.cfg
			cmd := m.cmd
			ctx := m.ctx
			cmds = append(cmds, func() tea.Msg {
				_, _ = cfg.RunHook(ctx, cmd, "notify", hookVars, "", 5_000_000_000)
				return nil
			})
		}

		// Auto-cleanup
		if autoCleanup && newState == session.Done && !s.IsCurrent {
			s := s // capture
			cmds = append(cmds, func() tea.Msg {
				out, err := action.CleanupSession(context.Background(), m.cfg, m.cmd, s.Name, s.PanePath, s.Git.Branch, s.Git.GitRoot)
				if err != nil {
					return ActionResultMsg{Action: "auto-cleanup", Session: s.Name, OK: false, Message: out}
				}
				return ActionResultMsg{Action: "auto-cleanup", Session: s.Name, OK: true, Message: out}
			})
		}
	}

	if m.initialLoad {
		m.initialLoad = false
	}

	// Auto-focus
	if !m.insideTmux && m.cfg.GetSettingBool("auto_focus") && time.Since(m.lastManualNav) > autoFocusCooldown {
		m.maybeAutoFocus()
	}

	return cmds
}

func (m *Model) maybeAutoFocus() {
	visible := m.visibleSessions()
	autoFocusStates := map[session.SessionState]bool{
		session.Attention: true, session.Blocked: true,
		session.Unresolved: true, session.Mergeable: true,
	}
	for i, s := range visible {
		if autoFocusStates[s.State()] {
			m.cursor = i
			return
		}
	}
}

func notifSeverity(state session.SessionState) string {
	switch state {
	case session.Blocked, session.Unresolved:
		return "error"
	case session.Mergeable, session.Done:
		return "info"
	default:
		return "warning"
	}
}

// --- Helpers ---

func (m *Model) addNotification(text, severity string) {
	m.notifications = append(m.notifications, Notification{
		Text:     text,
		Severity: severity,
		Expires:  time.Now().Add(3 * time.Second),
	})
	// Keep only last 10
	if len(m.notifications) > 10 {
		m.notifications = m.notifications[len(m.notifications)-10:]
	}
}

func (m Model) visibleSessions() []*session.Session {
	if m.filterState == nil {
		return m.sessions
	}
	var filtered []*session.Session
	for _, s := range m.sessions {
		if s.State() == *m.filterState {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (m Model) selectedSession() *session.Session {
	visible := m.visibleSessions()
	if m.cursor >= 0 && m.cursor < len(visible) {
		return visible[m.cursor]
	}
	return nil
}

func (m Model) batchSessions() []*session.Session {
	visible := m.visibleSessions()
	var batch []*session.Session
	for _, s := range visible {
		if m.selected[s.Name] {
			batch = append(batch, s)
		}
	}
	return batch
}

func (m *Model) resetDetailModeIfSessionChanged() {
	s := m.selectedSession()
	if s == nil {
		return
	}
	if s.Name != m.lastSessionName {
		m.detailMode = nil
		m.lastSessionName = s.Name
	}
}

func (m Model) activeDetailMode() view.DetailMode {
	if m.detailMode != nil {
		return *m.detailMode
	}
	s := m.selectedSession()
	if s == nil {
		return view.DetailPane
	}
	return view.AutoDetailMode(s)
}

func (m *Model) cycleFilter(dir int) {
	states := session.AllStates()
	if m.filterState == nil {
		if dir > 0 {
			m.filterState = &states[0]
		} else {
			last := states[len(states)-1]
			m.filterState = &last
		}
		return
	}
	idx := -1
	for i, s := range states {
		if s == *m.filterState {
			idx = i
			break
		}
	}
	next := idx + dir
	if next < 0 || next >= len(states) {
		m.filterState = nil
	} else {
		m.filterState = &states[next]
	}
}

func (m *Model) cycleSort(dir int) {
	modes := session.AllSortModes()
	idx := 0
	for i, mode := range modes {
		if mode == m.sortMode {
			idx = i
			break
		}
	}
	next := (idx + dir + len(modes)) % len(modes)
	m.sortMode = modes[next]
	session.SortSessions(m.sessions, m.sortMode)
}

func (m Model) tableHeight() int {
	// Status bar (1) + footer (1) + detail (if open)
	used := 2
	if m.detailOpen {
		used += m.detailHeight()
	}
	if m.dispatchActive {
		used += 3
	}
	h := m.height - used
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) detailHeight() int {
	h := m.height / 2
	if h < 4 {
		h = 4
	}
	return h
}

func (m Model) renderFooter() string {
	bindings := keys.ShortHelp()
	var parts []string
	p := view.PlainOnBar()
	for _, b := range bindings {
		help := b.Help()
		parts = append(parts, view.FaintOnBar().Render(help.Key)+p.Render(" "+help.Desc))
	}
	line := strings.Join(parts, p.Render("  "))
	return lipgloss.NewStyle().
		Background(view.BarBg).
		Width(m.width).
		Padding(0, 1).
		Render(line)
}

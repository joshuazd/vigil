package model

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jzinkduda/vigil/internal/action"
	"github.com/jzinkduda/vigil/internal/cache"
	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
	"github.com/jzinkduda/vigil/internal/view"
)

const autoFocusCooldown = 15 * time.Second

type Model struct {
	// Data
	sessions   []*session.Session
	gitCache   map[string]session.GitStatus
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
	confirmAction string
	confirmName   string

	// Dispatch
	dispatchActive bool
	dispatchInput  textinput.Model

	// Modes
	popupMode     bool
	initialLoad   bool
	initialPRDone bool
	cursorPlaced  bool

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

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Notifications
	notifications []Notification

	// Help
	help help.Model
}

// New creates a new Model.
func New(cfg *config.Config, cmd fetch.Commander) Model {
	ctx, cancel := context.WithCancel(context.Background())

	// Detect current session eagerly so cursor placement doesn't jump
	currentSession := fetch.CurrentSession(ctx, cmd)

	ti := textinput.New()
	ti.Placeholder = "URL or identifier..."
	ti.CharLimit = 500

	popupMode := os.Getenv("TMUX") != ""

	m := Model{
		currentSessionName: currentSession,
		gitCache:   make(map[string]session.GitStatus),
		prCache:    make(map[string]*session.PRStatus),
		prevStates: make(map[string]session.SessionState),
		selected:   make(map[string]bool),

		popupMode:   popupMode,
		initialLoad: true,
		detailOpen:  true,

		cfg:    cfg,
		cmd:    cmd,
		ctx:    ctx,
		cancel: cancel,

		dispatchInput: ti,
		help:          help.New(),
	}

	// In popup mode, load cache and place cursor immediately
	if popupMode && currentSession != "" {
		cachePath := cache.CachePath()
		cacheTTL := cfg.GetSettingDuration("cache_ttl")
		if cached := cache.Load(cachePath, cacheTTL); cached != nil {
			m.sessions = cached
			for i, s := range cached {
				if s.Name == currentSession {
					s.IsCurrent = true
					m.cursor = i
					m.cursorPlaced = true
					break
				}
			}
			// Backfill caches
			for _, s := range cached {
				m.gitCache[s.Name] = s.Git
				if s.PR != nil && s.Git.Branch != "" {
					m.prCache[s.Git.Branch] = s.PR
				}
			}
		}
	}

	return m
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd

	// Load cache for instant display (skip if already loaded in New for popup mode)
	if len(m.sessions) == 0 {
		cachePath := cache.CachePath()
		cacheTTL := m.cfg.GetSettingDuration("cache_ttl")
		if cached := cache.Load(cachePath, cacheTTL); cached != nil {
			cmds = append(cmds, func() tea.Msg {
				return TmuxUpdatedMsg{Sessions: cached}
			})
		}
	}

	// Start independent poll cycles: tmux (1s), git (configurable), PR (configurable)
	cmds = append(cmds,
		m.fetchTmuxCmd(),
		m.fetchGitCmd(),
		tmuxTickCmd(1*time.Second),
		gitTickCmd(m.cfg.GetSettingDuration("git_interval")),
		prTickCmd(m.cfg.GetSettingDuration("pr_interval")),
	)

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

	case TmuxTickMsg:
		return m, tea.Batch(m.fetchTmuxCmd(), tmuxTickCmd(1*time.Second))

	case GitTickMsg:
		return m, tea.Batch(m.fetchGitCmd(), gitTickCmd(m.cfg.GetSettingDuration("git_interval")))

	case PRTickMsg:
		return m, tea.Batch(m.fetchPRsCmd(), prTickCmd(m.cfg.GetSettingDuration("pr_interval")))

	case TmuxUpdatedMsg:
		return m.handleTmuxUpdated(msg)

	case GitUpdatedMsg:
		return m.handleGitUpdated(msg)

	case PRUpdatedMsg:
		return m.handlePRUpdated(msg)

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
		return m, tea.Batch(m.fetchTmuxCmd(), m.fetchGitCmd())

	case BatchResultMsg:
		m.selected = make(map[string]bool)
		m.addNotification(fmt.Sprintf("%s: %d ok, %d failed", msg.Action, msg.OK, msg.Failed), "info")
		return m, tea.Batch(m.fetchTmuxCmd(), m.fetchGitCmd())

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

	// Status bar
	statusBar := view.RenderStatusBar(m.sessions, m.filterState, m.sortMode, m.width)

	// Notification (overlaid on last table row)
	var notif string
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
			notif = style.Render(n.Text)
			break
		}
	}

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
		return m, tea.Batch(m.fetchTmuxCmd(), m.fetchGitCmd(), m.fetchPRsCmd())

	case key.Matches(msg, keys.ToggleDetail):
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
		m.confirmAction = ""
		m.confirmName = ""
		m.selected = make(map[string]bool)
		m.dispatchActive = false
		return m, nil
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

func (m Model) handleSelect() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil {
		return m, nil
	}
	if m.popupMode {
		m.cancel()
		return m, tea.Sequence(
			func() tea.Msg {
				_ = fetch.SwitchClient(context.Background(), m.cmd, s.Name)
				return nil
			},
			tea.Quit,
		)
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
	if err := action.OpenPRInBrowser(s.PR.URL); err != nil {
		m.addNotification("open: "+err.Error(), "error")
		return m, nil
	}
	if m.popupMode {
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleMerge() (tea.Model, tea.Cmd) {
	if len(m.selected) > 0 {
		if m.confirmAction == "batch-merge" {
			m.confirmAction = ""
			return m, m.batchMergeCmd()
		}
		m.confirmAction = "batch-merge"
		m.addNotification(fmt.Sprintf("Press m again to merge %d PRs", len(m.selected)), "warning")
		return m, nil
	}
	s := m.selectedSession()
	if s == nil || s.PR == nil || s.PR.Number == 0 {
		m.addNotification("No PR for this session", "warning")
		return m, nil
	}
	if m.confirmAction == "merge" && m.confirmName == s.Name {
		m.confirmAction = ""
		m.confirmName = ""
		return m, m.mergeCmd(s)
	}
	m.confirmAction = "merge"
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
		if m.confirmAction == "batch-cleanup" {
			m.confirmAction = ""
			return m, m.batchCleanupCmd()
		}
		m.confirmAction = "batch-cleanup"
		m.addNotification(fmt.Sprintf("Press x again to cleanup %d sessions", len(m.selected)), "warning")
		return m, nil
	}
	s := m.selectedSession()
	if s == nil {
		return m, nil
	}
	if m.confirmAction == "cleanup" && m.confirmName == s.Name {
		m.confirmAction = ""
		m.confirmName = ""
		return m, m.cleanupCmd(s)
	}
	m.confirmAction = "cleanup"
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

func (m Model) handleTmuxUpdated(msg TmuxUpdatedMsg) (tea.Model, tea.Cmd) {
	// Merge tmux metadata into existing sessions, preserving git/PR data
	newByName := make(map[string]*session.Session)
	for _, s := range msg.Sessions {
		newByName[s.Name] = s
	}

	// Update existing sessions with fresh tmux data, keep git/PR
	var merged []*session.Session
	seen := make(map[string]bool)
	for _, s := range msg.Sessions {
		if old, ok := m.findSession(s.Name); ok {
			old.IsCurrent = s.IsCurrent
			old.IsLast = s.IsLast
			old.HasBell = s.HasBell
			old.PanePath = s.PanePath
			merged = append(merged, old)
		} else {
			// New session — apply cached data
			if git, ok := m.gitCache[s.Name]; ok {
				s.Git = git
			}
			if s.Git.Branch != "" {
				if pr, ok := m.prCache[s.Git.Branch]; ok {
					s.PR = pr
				}
			}
			merged = append(merged, s)
		}
		seen[s.Name] = true
	}
	m.sessions = merged

	// Sort
	session.SortSessions(m.sessions, m.sortMode)

	// Cursor placement (popup mode only)
	if !m.cursorPlaced && m.popupMode && m.currentSessionName != "" {
		for i, s := range m.visibleSessions() {
			if s.Name == m.currentSessionName {
				m.cursor = i
				m.cursorPlaced = true
				break
			}
		}
	}

	// State transitions
	cmds := m.checkStateTransitions()

	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleGitUpdated(msg GitUpdatedMsg) (tea.Model, tea.Cmd) {
	for name, git := range msg.GitData {
		m.gitCache[name] = git
	}
	// Apply to sessions
	for _, s := range m.sessions {
		if git, ok := msg.GitData[s.Name]; ok {
			s.Git = git
		}
		// Apply PR from cache if branch now known
		if s.PR == nil && s.Git.Branch != "" {
			if pr, ok := m.prCache[s.Git.Branch]; ok {
				s.PR = pr
			}
		}
	}

	// Save cache
	go func() { _ = cache.Save(cache.CachePath(), m.sessions) }()

	// Trigger initial PR poll once we have branches
	var cmds []tea.Cmd
	if !m.initialPRDone {
		for _, s := range m.sessions {
			if s.Git.Branch != "" {
				m.initialPRDone = true
				cmds = append(cmds, m.fetchPRsCmd())
				break
			}
		}
	}

	cmds = append(cmds, m.checkStateTransitions()...)

	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}

	return m, tea.Batch(cmds...)
}

func (m Model) findSession(name string) (*session.Session, bool) {
	for _, s := range m.sessions {
		if s.Name == name {
			return s, true
		}
	}
	return nil, false
}

func (m Model) handlePRUpdated(msg PRUpdatedMsg) (tea.Model, tea.Cmd) {
	for branch, pr := range msg.PRData {
		if pr != nil {
			m.prCache[branch] = pr
		}
	}
	// Apply to sessions
	for _, s := range m.sessions {
		if s.Git.Branch != "" {
			if pr, ok := m.prCache[s.Git.Branch]; ok {
				s.PR = pr
			}
		}
	}

	cmds := m.checkStateTransitions()

	if m.detailOpen {
		cmds = append(cmds, m.refreshDetailCmd())
	}

	return m, tea.Batch(cmds...)
}

// --- Commands ---

func tmuxTickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return TmuxTickMsg(t)
	})
}

func gitTickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return GitTickMsg(t)
	})
}

func prTickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return PRTickMsg(t)
	})
}

// fetchTmuxCmd fetches tmux session metadata only (fast, ~15ms).
func (m Model) fetchTmuxCmd() tea.Cmd {
	currentName := m.currentSessionName
	return func() tea.Msg {
		ctx := m.ctx
		raw, err := fetch.ListSessions(ctx, m.cmd)
		if err != nil {
			return nil
		}
		current := fetch.CurrentSession(ctx, m.cmd)
		if current == "" {
			current = currentName
		}
		last := fetch.LastSession(ctx, m.cmd, current)
		bells := fetch.BellFlags(ctx, m.cmd)

		sessions := make([]*session.Session, len(raw))
		for i, r := range raw {
			sessions[i] = &session.Session{
				Name:      r.Name,
				PanePath:  r.PanePath,
				Created:   r.Created,
				IsCurrent: r.Name == current,
				IsLast:    r.Name == last,
				HasBell:   bells[r.Name],
			}
		}
		return TmuxUpdatedMsg{Sessions: sessions}
	}
}

// fetchGitCmd fetches git status for all sessions in parallel.
func (m Model) fetchGitCmd() tea.Cmd {
	// Snapshot current sessions for path lookup
	type sessionInfo struct {
		Name     string
		PanePath string
	}
	var infos []sessionInfo
	for _, s := range m.sessions {
		infos = append(infos, sessionInfo{Name: s.Name, PanePath: s.PanePath})
	}
	return func() tea.Msg {
		ctx := m.ctx
		if len(infos) == 0 {
			return nil
		}
		gitWorkers := m.cfg.GetSettingInt("git_workers")
		if gitWorkers < 1 {
			gitWorkers = 1
		}
		result := make(map[string]session.GitStatus)
		var mu sync.Mutex
		sem := make(chan struct{}, gitWorkers)
		var wg sync.WaitGroup
		for _, info := range infos {
			wg.Add(1)
			go func(name, path string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				git := fetch.FetchGitStatus(ctx, m.cmd, path)
				mu.Lock()
				result[name] = git
				mu.Unlock()
			}(info.Name, info.PanePath)
		}
		wg.Wait()
		return GitUpdatedMsg{GitData: result}
	}
}

func (m Model) fetchPRsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx := m.ctx
		type branchRoot struct {
			branch, gitRoot string
		}
		var branches []branchRoot
		seen := make(map[string]bool)
		for _, s := range m.sessions {
			if s.Git.Branch != "" && s.Git.GitRoot != "" && !seen[s.Git.Branch] {
				seen[s.Git.Branch] = true
				branches = append(branches, branchRoot{s.Git.Branch, s.Git.GitRoot})
			}
		}

		result := make(map[string]*session.PRStatus)
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4) // limit concurrent gh calls
		for _, br := range branches {
			wg.Add(1)
			go func(branch, gitRoot string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				pr := fetch.FetchPRStatus(ctx, m.cmd, branch, gitRoot)
				mu.Lock()
				result[branch] = pr
				mu.Unlock()
			}(br.branch, br.gitRoot)
		}
		wg.Wait()
		return PRUpdatedMsg{PRData: result}
	}
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
			return ActionResultMsg{Action: "merge", Session: name, OK: false, Message: err.Error()}
		}
		return ActionResultMsg{Action: "merge", Session: name, OK: true, Message: out}
	}
}

func (m Model) approveCmd(s *session.Session) tea.Cmd {
	gitRoot := s.Git.GitRoot
	branch := s.Git.Branch
	name := s.Name
	return func() tea.Msg {
		out, err := action.ApprovePR(context.Background(), m.cfg, gitRoot, branch)
		if err != nil {
			return ActionResultMsg{Action: "approve", Session: name, OK: false, Message: err.Error()}
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
			return ActionResultMsg{Action: "cleanup", Session: name, OK: false, Message: err.Error()}
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
			return ActionResultMsg{Action: "rebase", Session: name, OK: false, Message: err.Error()}
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
			return ActionResultMsg{Action: "draft", Session: name, OK: false, Message: err.Error()}
		}
		return ActionResultMsg{Action: "draft", Session: name, OK: true, Message: out}
	}
}

func (m Model) dispatchCmd(input string) tea.Cmd {
	return func() tea.Msg {
		out, err := action.Dispatch(context.Background(), m.cfg, input)
		if err != nil {
			return ActionResultMsg{Action: "dispatch", Session: "", OK: false, Message: err.Error()}
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
			_, err := action.ApprovePR(context.Background(), m.cfg, s.Git.GitRoot, s.Git.Branch)
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
			cmds = append(cmds, func() tea.Msg {
				_, _ = cfg.RunHook("notify", hookVars, "", 5_000_000_000)
				return nil
			})
		}

		// Auto-cleanup
		if autoCleanup && newState == session.Done && !s.IsCurrent {
			s := s // capture
			cmds = append(cmds, func() tea.Msg {
				out, err := action.CleanupSession(context.Background(), m.cfg, m.cmd, s.Name, s.PanePath, s.Git.Branch, s.Git.GitRoot)
				if err != nil {
					return ActionResultMsg{Action: "auto-cleanup", Session: s.Name, OK: false, Message: err.Error()}
				}
				return ActionResultMsg{Action: "auto-cleanup", Session: s.Name, OK: true, Message: out}
			})
		}
	}

	if m.initialLoad {
		m.initialLoad = false
	}

	// Auto-focus
	if !m.popupMode && m.cfg.GetSettingBool("auto_focus") && time.Since(m.lastManualNav) > autoFocusCooldown {
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

package view

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

const (
	colIndicator = 3
	colState     = 2
	colSession   = 52
	colGit       = 18
	colPR        = 22
)

// RenderTable renders the session table rows.
func RenderTable(sessions []*session.Session, cursor int, selected map[string]bool, staleThreshold int, width, height int) string {
	if len(sessions) == 0 {
		return DimStyle.Render("  No sessions")
	}

	var b strings.Builder
	rendered := 0
	for i, s := range sessions {
		if i >= height {
			break
		}
		isCursor := i == cursor
		line := renderRow(s, selected[s.Name], staleThreshold, width, isCursor)
		if rendered > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
		rendered++
	}
	// Pad to fill allocated height
	for rendered < height {
		b.WriteString("\n")
		rendered++
	}
	return b.String()
}

func renderRow(s *session.Session, selected bool, staleThreshold int, width int, isCursor bool) string {
	var bg *lipgloss.Color
	if isCursor {
		bg = &BarBg
	}

	indicator := IndicatorWithBg(s, selected, bg)
	dot := StateIndicatorWithBg(s, bg)
	name := SessionName(s)
	git := GitColWithBg(s, staleThreshold, bg)
	pr := PRColWithBg(s, bg)

	if isCursor {
		p := PlainOnBar()
		name = p.Render(name) + p.Render(strings.Repeat(" ", max(0, colSession-len(name))))
		git = git + p.Render(strings.Repeat(" ", max(0, colGit-visibleLen(git))))
		sep := p.Render(" ")
		line := strings.Join([]string{indicator, dot, name, git, pr}, sep)
		return CursorStyle.Width(width).Render(line)
	}

	name = padRight(name, colSession)
	git = padRight(git, colGit)
	return fmt.Sprintf("%s %s %s %s %s", indicator, dot, name, git, pr)
}

// padRight pads a string with spaces to the given width.
// Accounts for visible width (ignores ANSI escape sequences).
func padRight(s string, width int) string {
	visible := visibleLen(s)
	if visible >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visible)
}

// visibleLen returns the visible length of a string, ignoring ANSI escapes.
func visibleLen(s string) int {
	n := 0
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if s[i] == 'm' {
				inEscape = false
			}
			continue
		}
		// Handle multi-byte UTF-8
		b := s[i]
		if b < 0x80 {
			n++
		} else if b < 0xC0 {
			// continuation byte, skip
		} else {
			n++ // start of multi-byte char counts as 1
		}
	}
	return n
}

// DetailMode determines what the detail panel shows.
type DetailMode int

const (
	DetailPane DetailMode = iota
	DetailPRDesc
	DetailPRComments
)

var detailModeNames = [...]string{"pane", "pr", "comments"}

func (m DetailMode) String() string {
	if int(m) < len(detailModeNames) {
		return detailModeNames[m]
	}
	return "unknown"
}

// NextDetailMode cycles to the next mode.
func NextDetailMode(current DetailMode) DetailMode {
	return (current + 1) % 3
}

// AutoDetailMode selects the detail mode based on session state.
func AutoDetailMode(s *session.Session) DetailMode {
	switch s.State() {
	case session.Unresolved:
		return DetailPRComments
	case session.Review, session.Pending, session.Blocked:
		return DetailPRDesc
	default:
		return DetailPane
	}
}

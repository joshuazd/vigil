package view

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// RenderTable renders the session table rows, dropping columns to fit width.
func RenderTable(sessions []*session.Session, cursor int, selected map[string]bool, staleThreshold int, width, height int, notification string) string {
	if len(sessions) == 0 {
		return DimStyle.Render("  No sessions")
	}

	layout := LayoutForWidth(width)

	var b strings.Builder
	rendered := 0
	for i, s := range sessions {
		if i >= height {
			break
		}
		isCursor := i == cursor
		line := renderRow(s, i, selected[s.Name], staleThreshold, width, isCursor, layout)
		if rendered > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
		rendered++
	}
	// Pad to fill allocated height, placing notification on the last row
	clamped := clampNotification(notification, width)
	for rendered < height {
		b.WriteString("\n")
		rendered++
		if clamped != "" && rendered == height {
			b.WriteString(lipgloss.NewStyle().Width(width).Render(clamped))
		}
	}
	return b.String()
}

func renderRow(s *session.Session, index int, selected bool, staleThreshold int, width int, isCursor bool, layout TableLayout) string {
	var bg *lipgloss.Color
	if isCursor {
		bg = &BarBg
	}

	name := truncateName(s.Name, layout.Name)

	var cells []string
	if layout.Indicator {
		cells = append(cells, IndicatorWithBg(s, selected, bg))
	}
	if layout.Index {
		cells = append(cells, indexCol(index, bg))
	}
	if layout.State {
		cells = append(cells, StateIndicatorWithBg(s, bg))
	}

	if isCursor {
		p := PlainOnBar()
		cells = append(cells, p.Render(name)+p.Render(strings.Repeat(" ", max(0, layout.Name-visibleLen(name)))))
		if layout.Git > 0 {
			git := TruncateVisible(GitColWithBg(s, staleThreshold, bg), layout.Git)
			cells = append(cells, git+p.Render(strings.Repeat(" ", max(0, layout.Git-visibleLen(git)))))
		}
		if layout.PR > 0 {
			cells = append(cells, TruncateVisible(PRColWithBg(s, bg), layout.PR))
		}
		return CursorStyle.Width(width).Render(strings.Join(cells, p.Render(" ")))
	}

	cells = append(cells, padRight(name, layout.Name))
	if layout.Git > 0 {
		cells = append(cells, padRight(TruncateVisible(GitColWithBg(s, staleThreshold, bg), layout.Git), layout.Git))
	}
	if layout.PR > 0 {
		cells = append(cells, TruncateVisible(PRColWithBg(s, bg), layout.PR))
	}
	return strings.Join(cells, " ")
}

// indexCol renders the 0-9 index for quick-jump, or blank for 10+.
func indexCol(index int, bg *lipgloss.Color) string {
	s := " "
	if index <= 9 {
		s = fmt.Sprintf("%d", index)
	}
	style := lipgloss.NewStyle().Faint(true)
	if bg != nil {
		style = style.Background(*bg)
	}
	return style.Render(s)
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

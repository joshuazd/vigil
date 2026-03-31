package view

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// ANSI standard colors — adapts to the user's terminal theme.
// lipgloss accepts ANSI color numbers 0-15 as strings.
var (
	Black        = lipgloss.Color("0")
	Red          = lipgloss.Color("1")
	Green        = lipgloss.Color("2")
	Yellow       = lipgloss.Color("3")
	Blue         = lipgloss.Color("4")
	Magenta      = lipgloss.Color("5")
	Cyan         = lipgloss.Color("6")
	White        = lipgloss.Color("7")
	BrightBlack  = lipgloss.Color("8")  // dim/gray
	BrightRed    = lipgloss.Color("9")
	BrightGreen  = lipgloss.Color("10")
	BrightYellow = lipgloss.Color("11")
	BrightBlue   = lipgloss.Color("12")
	BrightMagenta = lipgloss.Color("13")
	BrightCyan   = lipgloss.Color("14")
	BrightWhite  = lipgloss.Color("15")

	// Semantic aliases
	Dim = BrightBlack
)

// StateColor maps session states to their display color.
var StateColor = map[session.SessionState]lipgloss.Color{
	session.Attention:  BrightYellow,
	session.Blocked:    BrightRed,
	session.Unresolved: BrightRed,
	session.Mergeable:  BrightGreen,
	session.Approved:   BrightGreen,
	session.Pending:    BrightYellow,
	session.Review:     Cyan,
	session.Done:       Dim,
	session.Idle:       Dim,
}

// StateDot maps session states to their dot character.
var StateDot = map[session.SessionState]string{
	session.Attention:  "●",
	session.Blocked:    "●",
	session.Unresolved: "○",
	session.Mergeable:  "●",
	session.Approved:   "○",
	session.Pending:    "○",
	session.Review:     "●",
	session.Done:       "●",
	session.Idle:       "·",
}

// BarBg is the background color for status bar, footer, and cursor row.
var BarBg = Black

// Reusable styles
var (
	StatusBarStyle = lipgloss.NewStyle().Background(BarBg).Padding(0, 1)
	CursorStyle    = lipgloss.NewStyle().Background(BarBg)
	BoldStyle      = lipgloss.NewStyle().Bold(true)
	DimStyle       = lipgloss.NewStyle().Faint(true)
)

// OnBar returns text styled with fg color on the bar background.
func OnBar(fg lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(fg).Background(BarBg)
}

// PlainOnBar returns text on the bar background with no fg override.
func PlainOnBar() lipgloss.Style {
	return lipgloss.NewStyle().Background(BarBg)
}

// FaintOnBar returns dim text on the bar background.
func FaintOnBar() lipgloss.Style {
	return lipgloss.NewStyle().Faint(true).Background(BarBg)
}

// BoldOnBar returns bold text on the bar background.
func BoldOnBar() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Background(BarBg)
}

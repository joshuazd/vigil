package view

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// styledFg returns a style with foreground and optional background.
func styledFg(fg lipgloss.Color, bg *lipgloss.Color) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(fg)
	if bg != nil {
		s = s.Background(*bg)
	}
	return s
}

func styledFaint(bg *lipgloss.Color) lipgloss.Style {
	s := lipgloss.NewStyle().Faint(true)
	if bg != nil {
		s = s.Background(*bg)
	}
	return s
}

// Indicator returns the session indicator column (3 chars wide).
func Indicator(s *session.Session, selected bool) string {
	return IndicatorWithBg(s, selected, nil)
}

func IndicatorWithBg(s *session.Session, selected bool, bg *lipgloss.Color) string {
	// Left char: selection or bell
	left := " "
	var leftStyle *lipgloss.Color
	if selected {
		left = "◆"
		leftStyle = &BrightCyan
	} else if s.HasBell {
		left = "*"
		leftStyle = &BrightYellow
	}

	// Right char: current/last session
	right := s.Indicator()

	plain := lipgloss.NewStyle()
	if bg != nil {
		plain = plain.Background(*bg)
	}

	var result string
	if leftStyle != nil {
		result = styledFg(*leftStyle, bg).Render(left)
	} else {
		result = plain.Render(left)
	}
	result += plain.Render(right + " ")
	return result
}

// StateIndicator returns the colored state dot.
func StateIndicator(s *session.Session) string {
	return StateIndicatorWithBg(s, nil)
}

func StateIndicatorWithBg(s *session.Session, bg *lipgloss.Color) string {
	state := s.State()
	dot := StateDot[state]
	color := StateColor[state]
	if state == session.Done || state == session.Idle {
		return styledFaint(bg).Render(dot)
	}
	return styledFg(color, bg).Render(dot)
}

// GitCol returns the formatted git status column.
func GitCol(s *session.Session, staleThreshold int) string {
	return GitColWithBg(s, staleThreshold, nil)
}

func GitColWithBg(s *session.Session, staleThreshold int, bg *lipgloss.Color) string {
	stale := s.Git.IsStale(staleThreshold)

	var parts []string
	if s.Git.Modified > 0 {
		parts = append(parts, fmt.Sprintf("~%d", s.Git.Modified))
	}
	if s.Git.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", s.Git.Added))
	}
	if s.Git.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("-%d", s.Git.Deleted))
	}
	if s.Git.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d", s.Git.Unpushed))
	}

	age := s.Git.RebaseAgeDisplay()
	if len(parts) == 0 && age == "" {
		if bg != nil {
			return lipgloss.NewStyle().Background(*bg).Render("—")
		}
		return "—"
	}

	sp := " "
	if bg != nil {
		sp = lipgloss.NewStyle().Background(*bg).Render(" ")
	}

	var b strings.Builder
	if len(parts) > 0 {
		if bg != nil {
			b.WriteString(lipgloss.NewStyle().Background(*bg).Render(strings.Join(parts, " ")))
		} else {
			b.WriteString(strings.Join(parts, " "))
		}
	}
	if age != "" {
		if len(parts) > 0 {
			b.WriteString(sp)
		}
		ageStr := age
		if stale {
			ageStr = "⚠" + age
		}
		if stale {
			b.WriteString(styledFg(BrightRed, bg).Render(ageStr))
		} else {
			if bg != nil {
				b.WriteString(lipgloss.NewStyle().Background(*bg).Render(ageStr))
			} else {
				b.WriteString(ageStr)
			}
		}
	}
	return b.String()
}

// PRCol returns the formatted PR column with color.
func PRCol(s *session.Session) string {
	return PRColWithBg(s, nil)
}

func PRColWithBg(s *session.Session, bg *lipgloss.Color) string {
	if s.PR == nil || s.PR.Number == 0 {
		if bg != nil {
			return lipgloss.NewStyle().Background(*bg).Render("—")
		}
		return "—"
	}
	return ColorizePRWithBg(s.PR, bg)
}

// ColorizePR renders a PR status with styled icons.
func ColorizePR(pr *session.PRStatus) string {
	return ColorizePRWithBg(pr, nil)
}

func ColorizePRWithBg(pr *session.PRStatus, bg *lipgloss.Color) string {
	var b strings.Builder

	sp := " "
	if bg != nil {
		sp = lipgloss.NewStyle().Background(*bg).Render(" ")
	}

	// Number with state color
	switch {
	case pr.IsDraft:
		b.WriteString(styledFaint(bg).Render(fmt.Sprintf("#%d", pr.Number)))
	case pr.State == "MERGED":
		b.WriteString(styledFg(Magenta, bg).Render(fmt.Sprintf("#%d", pr.Number)))
	case pr.State == "CLOSED":
		b.WriteString(styledFg(Red, bg).Render(fmt.Sprintf("#%d", pr.Number)))
	default:
		b.WriteString(styledFg(Green, bg).Render(fmt.Sprintf("#%d", pr.Number)))
	}

	// Check status
	switch pr.Checks {
	case "pass":
		b.WriteString(sp + styledFg(BrightGreen, bg).Render("✓"))
	case "fail":
		b.WriteString(sp + styledFg(BrightRed, bg).Render("✗"))
	case "pending":
		b.WriteString(sp + styledFg(BrightYellow, bg).Render("●"))
	}

	// Review status (only for open PRs)
	if pr.State == "OPEN" {
		switch {
		case pr.ReviewDecision == "APPROVED":
			b.WriteString(sp + styledFg(BrightGreen, bg).Render("☑"))
		case pr.ReviewDecision == "CHANGES_REQUESTED":
			b.WriteString(sp + styledFg(BrightRed, bg).Render("✎"))
		case pr.Approvals > 0:
			b.WriteString(sp + styledFg(BrightYellow, bg).Render("☑"))
		}
		if pr.UnresolvedComments > 0 {
			b.WriteString(sp + styledFg(BrightYellow, bg).Render(fmt.Sprintf("☐ %d", pr.UnresolvedComments)))
		}
		if !pr.IsDraft && pr.ReviewersRequested == 0 && pr.ReviewDecision == "" && pr.Approvals == 0 {
			b.WriteString(sp + styledFg(BrightYellow, bg).Render("⚠"))
		}
		if pr.HasConflicts {
			b.WriteString(sp + styledFg(BrightRed, bg).Render("⚡"))
		}
	}

	return b.String()
}

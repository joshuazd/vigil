package view

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// RenderDetail renders the detail panel content for a session.
func RenderDetail(s *session.Session, mode DetailMode, paneContent string, comments []session.ReviewComment, staleThreshold int, width, height int) string {
	if s == nil {
		return ""
	}

	var b strings.Builder
	renderDetailHeader(&b, s, mode, staleThreshold)
	b.WriteString("\n")

	switch mode {
	case DetailPRDesc:
		renderPRDesc(&b, s, height-3)
	case DetailPRComments:
		renderPRComments(&b, s, comments, height-3)
	default:
		renderPane(&b, paneContent, height-3, width)
	}

	// Truncate to height lines
	content := truncateLines(b.String(), height-2) // -2 for border + header

	// Add top border
	border := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(Cyan).
		Width(width).
		MaxHeight(height)

	return border.Render(content)
}

func renderDetailHeader(b *strings.Builder, s *session.Session, mode DetailMode, staleThreshold int) {
	// Branch
	if s.Git.Branch != "" {
		b.WriteString(DimStyle.Render("⎇ "))
		b.WriteString(lipgloss.NewStyle().Foreground(Cyan).Render(s.Git.Branch))
	}

	// PR info
	if s.PR != nil && s.PR.Number > 0 {
		b.WriteString("  ")
		b.WriteString(ColorizePR(s.PR))
	}

	// Git changes
	var gitParts []string
	if s.Git.Modified > 0 {
		gitParts = append(gitParts, fmt.Sprintf("~%d", s.Git.Modified))
	}
	if s.Git.Added > 0 {
		gitParts = append(gitParts, fmt.Sprintf("+%d", s.Git.Added))
	}
	if s.Git.Deleted > 0 {
		gitParts = append(gitParts, fmt.Sprintf("-%d", s.Git.Deleted))
	}
	if s.Git.Unpushed > 0 {
		gitParts = append(gitParts, fmt.Sprintf("↑%d", s.Git.Unpushed))
	}
	if len(gitParts) > 0 {
		b.WriteString(DimStyle.Render("  ± "))
		b.WriteString(strings.Join(gitParts, " "))
	}

	// Rebase age
	age := s.Git.RebaseAgeDisplay()
	if age != "" && s.Git.RebaseAgeSecs != nil {
		stale := s.Git.IsStale(staleThreshold)
		secs := *s.Git.RebaseAgeSecs
		var label string
		if secs < 86400 {
			label = fmt.Sprintf("  ↻ rebased %dh ago", secs/3600)
		} else {
			label = fmt.Sprintf("  ↻ rebased %dd ago", secs/86400)
		}
		if stale {
			b.WriteString(lipgloss.NewStyle().Foreground(BrightRed).Render("  ⚠" + strings.TrimSpace(label)))
		} else {
			b.WriteString(DimStyle.Render(label))
		}
	}

	// State dot
	state := s.State()
	dot := StateDot[state]
	color := StateColor[state]
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%s %s", dot, state)))

	// Mode indicator
	b.WriteString(DimStyle.Render(fmt.Sprintf("  [%s]", mode)))
}

func renderPane(b *strings.Builder, paneContent string, maxLines int, width int) {
	if paneContent == "" {
		return
	}
	// Available width: total width minus indent (2) and border padding (2)
	maxW := width - 4
	if maxW < 10 {
		maxW = 10
	}
	lines := strings.Split(paneContent, "\n")
	// Show last maxLines
	start := 0
	if len(lines) > maxLines {
		start = len(lines) - maxLines
	}
	for _, line := range lines[start:] {
		stripped := strings.TrimRight(line, " \t")
		if stripped != "" {
			stripped = truncateVisible(stripped, maxW)
			b.WriteString("  " + stripped + "\n")
		}
	}
}

// truncateVisible truncates a string (which may contain ANSI escapes) to maxW
// visible characters, appending "…" if truncated.
//
// The result never exceeds maxW cells, ellipsis included. It used to cut at
// maxW and then append, so a truncated string was maxW+1 and every caller
// padding to maxW overflowed its column by one - which is how a queue row
// could run past the width it was handed.
func truncateVisible(s string, maxW int) string {
	// Guard first: a string that already fits is returned untouched, or a
	// value of exactly maxW would be cut to make room for an ellipsis it
	// does not need.
	if visibleLen(s) <= maxW {
		return s
	}

	visible := 0
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
		// Count visible character
		b := s[i]
		if b >= 0x80 && b < 0xC0 {
			continue // UTF-8 continuation byte
		}
		visible++
		// maxW-1, not maxW: the "…" appended below occupies the last cell, so
		// stopping at maxW returned maxW+1 cells and every caller that padded
		// to maxW overflowed its column by one.
		if visible >= maxW-1 {
			// Skip past remaining bytes of this UTF-8 character
			end := i + 1
			for end < len(s) && s[end] >= 0x80 && s[end] < 0xC0 {
				end++
			}
			// Include any trailing ANSI reset sequences
			for end < len(s) && s[end] == '\x1b' {
				j := end + 1
				for j < len(s) && s[j] != 'm' {
					j++
				}
				if j < len(s) {
					end = j + 1
				} else {
					break
				}
			}
			return s[:end] + "…"
		}
	}
	return s
}

func renderPRDesc(b *strings.Builder, s *session.Session, maxLines int) {
	if s.PR == nil || s.PR.Number == 0 {
		b.WriteString(DimStyle.Render("  No PR"))
		return
	}
	lines := 0
	if s.PR.Title != "" {
		b.WriteString(BoldStyle.Render("  "+s.PR.Title) + "\n")
		lines++
	}
	if s.PR.Body != "" {
		b.WriteString("\n")
		lines++
		for _, line := range strings.Split(s.PR.Body, "\n") {
			if lines >= maxLines {
				break
			}
			b.WriteString("  " + line + "\n")
			lines++
		}
	}
}

func renderPRComments(b *strings.Builder, s *session.Session, comments []session.ReviewComment, maxLines int) {
	if s.PR == nil {
		b.WriteString(DimStyle.Render("  No review comments"))
		return
	}
	// nil means not fetched yet; the model stores an empty non-nil slice once a
	// fetch has answered, so an in-flight fetch does not read as "none".
	if comments == nil {
		if s.PR.UnresolvedComments > 0 {
			b.WriteString(DimStyle.Render("  Loading comments…"))
		} else {
			b.WriteString(DimStyle.Render("  No review comments"))
		}
		return
	}
	if len(comments) == 0 {
		b.WriteString(DimStyle.Render("  No review comments"))
		return
	}
	var unresolved []session.ReviewComment
	for _, c := range comments {
		if !c.Resolved {
			unresolved = append(unresolved, c)
		}
	}
	if len(unresolved) == 0 {
		b.WriteString(DimStyle.Render("  All comments resolved"))
		return
	}
	lines := 0
	for i, c := range unresolved {
		if lines >= maxLines {
			break
		}
		if i > 0 {
			b.WriteString("\n")
			lines++
		}
		// Author + path header
		b.WriteString("  " + lipgloss.NewStyle().Foreground(Cyan).Render(c.Author))
		if c.Path != "" {
			b.WriteString("  " + DimStyle.Render(c.Path))
		}
		b.WriteString("\n")
		lines++
		for _, line := range strings.Split(c.Body, "\n") {
			if lines >= maxLines {
				break
			}
			b.WriteString("    " + line + "\n")
			lines++
		}
	}
}

// truncateLines keeps only the first n lines of a string.
func truncateLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines++
			if lines >= n {
				return s[:i]
			}
		}
	}
	return s
}

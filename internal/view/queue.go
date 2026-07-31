package view

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jzinkduda/vigil/internal/session"
)

// queueLabelWidth caps the id column. portal#34967 is 12; the cap keeps a
// pathological repo name from eating the title.
const queueLabelWidth = 20

// RenderQueue renders the work-waiting section below the session table.
// cursor is an index into items, or -1 when the cursor is on a session row -
// the Model owns the single cursor and does that translation, so this never
// has to know how many sessions precede it.
//
// hidden is what Collector.Queue removed because a session already covers it.
// It is deliberately not "N filtered": the queries filter server-side and
// those removals are not visible from here.
//
// maxRows caps how many item rows are drawn (the header is not counted).
// maxRows <= 0 means no cap. Items beyond it collapse into one "... +N more"
// row rather than growing the section past what the caller budgeted, which is
// what keeps the whole dashboard within m.height - RenderQueue has no other
// way to know the terminal has a bottom edge.
func RenderQueue(items []session.QueueItem, hidden, cursor, width, maxRows int, now time.Time) string {
	if len(items) == 0 {
		return ""
	}

	header := fmt.Sprintf("QUEUE  %d", len(items))
	if hidden > 0 {
		header += fmt.Sprintf(" · %d in progress", hidden)
	}

	lines := []string{FaintOnBar().Render(header)}

	shown := items
	overflow := 0
	if maxRows > 0 && len(items) > maxRows {
		visible := maxRows - 1
		if visible < 0 {
			visible = 0
		}
		shown = items[:visible]
		overflow = len(items) - visible
	}

	for i, it := range shown {
		marker := "  "
		if i == cursor {
			marker = "> "
		}
		label := truncateVisible(it.Label(), queueLabelWidth)
		age := queueAge(it.UpdatedAt, now)

		fixed := len(marker) + queueLabelWidth + 2 + len(age) + 1
		titleWidth := width - fixed
		if titleWidth < 1 {
			titleWidth = 1
		}
		row := fmt.Sprintf("%s%-*s  %-*s %s",
			marker, queueLabelWidth, label, titleWidth, truncateVisible(it.Title, titleWidth), age)

		if i == cursor {
			row = CursorStyle.Render(row)
		}
		lines = append(lines, row)
	}
	if overflow > 0 {
		lines = append(lines, FaintOnBar().Render(fmt.Sprintf("… +%d more", overflow)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func queueAge(updated int64, now time.Time) string {
	if updated == 0 {
		return ""
	}
	d := now.Sub(time.Unix(updated, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

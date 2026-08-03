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

// queueAuthorWidth caps the author column. The longest login seen in real
// data is 15; GitHub allows 39, so this truncates rather than stretching.
const queueAuthorWidth = 16

// queueAuthorMinTitle is the title width below which the author column is
// dropped for the whole section. A narrow queue is worth more as titles than
// as logins, and dropping the column is how this degrades instead of wrapping.
const queueAuthorMinTitle = 24

// QueueRowsShown returns how many of itemCount items RenderQueue draws as
// item rows once capped at maxRows - the header and the "... +N more" line
// are not counted. Model.drawnQueueRows calls this too, which is what keeps
// the cursor's ceiling from disagreeing with what RenderQueue actually draws.
func QueueRowsShown(itemCount, maxRows int) int {
	if maxRows > 0 && itemCount > maxRows {
		visible := maxRows - 1
		if visible < 0 {
			visible = 0
		}
		return visible
	}
	return itemCount
}

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
// way to know the terminal has a bottom edge. QueueRowsShown above is the
// row-count half of that arithmetic, shared so it stays in sync.
func RenderQueue(items []session.QueueItem, hidden, cursor, width, maxRows int, now time.Time) string {
	if len(items) == 0 {
		return ""
	}

	header := fmt.Sprintf("QUEUE  %d", len(items))
	if hidden > 0 {
		header += fmt.Sprintf(" · %d in progress", hidden)
	}

	lines := []string{FaintOnBar().Render(header)}

	n := QueueRowsShown(len(items), maxRows)
	shown := items
	overflow := 0
	if n < len(items) {
		shown = items[:n]
		overflow = len(items) - n
	}

	// Both of these are decided once for the whole section rather than per
	// row. Age width varies by row, so a per-row decision would put the
	// author column on some rows and not others and slide the title with it.
	ageWidth := 0
	for _, it := range shown {
		if n := len(queueAge(it.UpdatedAt, now)); n > ageWidth {
			ageWidth = n
		}
	}
	fixed := 2 + queueLabelWidth + 2 + ageWidth + 1
	showAuthor := width-fixed-queueAuthorWidth-2 >= queueAuthorMinTitle

	titleWidth := width - fixed
	if showAuthor {
		titleWidth -= queueAuthorWidth + 2
	}
	if titleWidth < 1 {
		titleWidth = 1
	}

	for i, it := range shown {
		marker := "  "
		if i == cursor {
			marker = "> "
		}
		label := truncateVisible(it.Label(), queueLabelWidth)
		age := queueAge(it.UpdatedAt, now)

		row := fmt.Sprintf("%s%-*s  ", marker, queueLabelWidth, label)
		if showAuthor {
			row += fmt.Sprintf("%-*s  ", queueAuthorWidth, truncateVisible(it.Author, queueAuthorWidth))
		}
		row += fmt.Sprintf("%-*s %*s", titleWidth, truncateVisible(it.Title, titleWidth), ageWidth, age)

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

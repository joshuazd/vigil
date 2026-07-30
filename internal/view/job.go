package view

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// RenderJobLine renders one line describing dispatch activity, or "" when
// there is none. One line rather than one per job: jobs are serialized, so at
// most one is doing anything, and a panel is ten rows tall.
//
// A failure outranks a running job, which outranks a queued one: a failure is
// the only state the user has to act on.
func RenderJobLine(jobs []protocol.Job, width int) string {
	if len(jobs) == 0 || width <= 0 {
		return ""
	}

	lead := pickJob(jobs)
	if lead == nil {
		return ""
	}

	queued := 0
	for _, j := range jobs {
		if j.State == protocol.JobQueued && j.ID != lead.ID {
			queued++
		}
	}

	marker, colour := "⚡", BrightCyan
	switch lead.State {
	case protocol.JobFailed, protocol.JobRefused:
		marker, colour = "✗", BrightRed
	case protocol.JobSucceeded:
		marker, colour = "✓", BrightGreen
	}

	suffix := ""
	if queued > 0 {
		suffix = fmt.Sprintf(" (+%d)", queued)
	}

	// TruncateVisible cuts from the end, so the queued suffix is reserved and
	// rendered on its own rather than folded into the string that gets cut:
	// the marker, the input, and the queued count are what a glance needs,
	// and the status - a free-text progress note - is the one piece worth
	// dropping first to make room.
	budget := max(0, width-visibleLen(suffix))

	body := marker + " " + lead.Input
	if lead.Status != "" {
		const sep = " · "
		if room := budget - visibleLen(body) - visibleLen(sep); room > 0 {
			body += sep + truncateName(lead.Status, room)
		}
	}

	style := lipgloss.NewStyle().Foreground(colour)
	return style.Render(TruncateVisible(body, budget)) + style.Render(suffix)
}

func pickJob(jobs []protocol.Job) *protocol.Job {
	// Refused ranks with failed: both are "this did not happen", and both are
	// the only states the user has to act on.
	for _, state := range []string{protocol.JobRefused, protocol.JobFailed, protocol.JobRunning, protocol.JobSucceeded, protocol.JobQueued} {
		for i := range jobs {
			if jobs[i].State == state {
				return &jobs[i]
			}
		}
	}
	return nil
}

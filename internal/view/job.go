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

	text := fmt.Sprintf("%s %s", marker, lead.Input)
	if lead.Status != "" {
		text += " · " + lead.Status
	}
	if queued > 0 {
		text += fmt.Sprintf(" (+%d)", queued)
	}

	return lipgloss.NewStyle().Foreground(colour).Render(TruncateVisible(text, width))
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

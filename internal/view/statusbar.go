package view

import (
	"fmt"
	"strings"

	"github.com/jzinkduda/vigil/internal/session"
)

// RenderStatusBar renders the top status bar.
func RenderStatusBar(sessions []*session.Session, filterState *session.SessionState, sortMode session.SortMode, width int) string {
	// Count by state
	counts := make(map[session.SessionState]int)
	for _, s := range sessions {
		counts[s.State()]++
	}

	p := PlainOnBar()

	var b strings.Builder
	b.WriteString(BoldOnBar().Render("vigil"))
	b.WriteString(p.Render(fmt.Sprintf(" · %d sessions", len(sessions))))

	for _, state := range session.AllStates() {
		n := counts[state]
		if n > 0 {
			b.WriteString(p.Render(" · "))
			if state == session.Done || state == session.Idle {
				b.WriteString(FaintOnBar().Render(fmt.Sprintf("%d %s", n, state)))
			} else {
				b.WriteString(OnBar(StateColor[state]).Render(fmt.Sprintf("%d %s", n, state)))
			}
		}
	}

	if filterState != nil {
		color := StateColor[*filterState]
		b.WriteString(p.Render(" · "))
		b.WriteString(OnBar(color).Render(fmt.Sprintf("filter: %s", *filterState)))
	}

	if sortMode != session.SortCreated {
		b.WriteString(p.Render(" · "))
		b.WriteString(FaintOnBar().Render(fmt.Sprintf("sort: %s", sortMode)))
	}

	return StatusBarStyle.Width(width).Render(b.String())
}

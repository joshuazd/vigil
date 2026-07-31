package view

import (
	"fmt"
	"strings"

	"github.com/jzinkduda/vigil/internal/session"
)

// RenderStatusBar renders the top status bar. Segments are appended in
// priority order and any that does not fit the width is skipped, so a 40
// column panel gets the identity, the count and the health rather than a
// wrapped line. health is empty in the common case. queueCount is the number
// of items waiting in the work queue; 0 hides the badge.
func RenderStatusBar(sessions []*session.Session, filterState *session.SessionState, sortMode session.SortMode, width int, health string, queueCount int) string {
	counts := make(map[session.SessionState]int)
	for _, s := range sessions {
		counts[s.State()]++
	}

	p := PlainOnBar()

	var b strings.Builder
	// budget is what StatusBarStyle leaves for content: it renders with
	// Padding(0, 1), and lipgloss counts padding inside Width.
	budget := width - 2
	used := 0

	// addSegment appends a separator and a segment together, and only if both
	// fit. Together, because a segment dropped on its own leaves a dangling
	// " · ". Only if it fits, because the alternative is truncating a rendered
	// string mid escape sequence.
	//
	// visibleLen rather than len: the separator is a multi-byte "·" and state
	// names may not stay ASCII forever.
	addSegment := func(text, rendered string) {
		const sep = " · "
		cost := visibleLen(sep) + visibleLen(text)
		if used+cost > budget {
			return
		}
		used += cost
		b.WriteString(p.Render(sep))
		b.WriteString(rendered)
	}

	if visibleLen("vigil") <= budget {
		used += visibleLen("vigil")
		b.WriteString(BoldOnBar().Render("vigil"))
	}

	countText := fmt.Sprintf("%d sessions", len(sessions))
	addSegment(countText, p.Render(countText))

	// Health outranks the state counts: in a 40 column panel it is the
	// segment worth the space.
	if health != "" {
		addSegment(health, OnBar(BrightYellow).Render(health))
	}

	// Above the state counts: at a narrow width, "work is waiting" is worth
	// more than a per-state breakdown of what is already running.
	if queueCount > 0 {
		text := fmt.Sprintf("⚡%d", queueCount)
		addSegment(text, OnBar(BrightYellow).Render(text))
	}

	for _, state := range session.AllStates() {
		n := counts[state]
		if n == 0 {
			continue
		}
		text := fmt.Sprintf("%d %s", n, state)
		if state == session.Done || state == session.Idle {
			addSegment(text, FaintOnBar().Render(text))
		} else {
			addSegment(text, OnBar(StateColor[state]).Render(text))
		}
	}

	if filterState != nil {
		text := fmt.Sprintf("filter: %s", *filterState)
		addSegment(text, OnBar(StateColor[*filterState]).Render(text))
	}

	if sortMode != session.SortCreated {
		text := fmt.Sprintf("sort: %s", sortMode)
		addSegment(text, FaintOnBar().Render(text))
	}

	return StatusBarStyle.Width(width).Render(b.String())
}

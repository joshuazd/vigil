package view

import "strings"

// Column widths. colName is a cap, not a fixed width: the name column shrinks
// with the pane but never stretches past this, so the dashboard at 200 columns
// looks exactly as it does at 104 rather than flinging the git and PR columns
// out to the right edge.
const (
	colIndicator = 3
	colIndex     = 1
	colState     = 1
	colName      = 52
	colGit       = 18
	colPR        = 22

	// colPRCompact is what survives when a panel is too narrow for the full
	// PR column. ColorizePR renders the number and check state first, so
	// truncating to this keeps what matters and drops the review icons.
	colPRCompact = 12

	// nameMin is the narrowest name column worth rendering. Below it, drop a
	// column instead: "SC-1902…" plus a PR number beats four characters of
	// name plus everything else.
	nameMin = 8
	sep     = 1
)

// fixed is every column but the name, including every separator: one between
// each pair, so the name contributes a separator on both sides when a column
// follows it.
const (
	fullFixed    = colIndicator + sep + colIndex + sep + colState + sep + sep + colGit + sep + colPR // 50
	noGitFixed   = colIndicator + sep + colIndex + sep + colState + sep + sep + colPR                // 31
	compactFixed = colIndicator + sep + colState + sep + sep + colPRCompact                          // 19
	noPRFixed    = colIndicator + sep + colState + sep                                               // 6
	bareFixed    = colState + sep                                                                    // 2
)

// The width at which each tier is chosen. Tuned, not derived: fixed+nameMin
// would move tierNoGit to 39, and width 40 - the landscape panel's default -
// would stop choosing the compact tier, taking a 9-column name beside a full PR
// column in exchange for 20 columns of name and a compact one. Every value here
// is the width that tier is chosen at today, so every layout verified on a real
// pane stays verified. TestFrozenThresholdsAdmitAUsefulName keeps them honest
// against the costs above.
const (
	tierFull    = 60
	tierNoGit   = 41
	tierCompact = 28
	tierNoPR    = 15
	tierBare    = 4
)

// TableLayout is which columns fit a given width and how wide each is. Only
// the name column is never dropped.
type TableLayout struct {
	Indicator bool
	Index     bool
	State     bool
	Name      int
	Git       int
	PR        int
}

// Total is the exact number of columns a row will occupy. Rows wider than the
// pane wrap, and one wrapped row shifts every row below it for as long as the
// panel is open, so tests assert this against the requested width at every
// width from 1 up.
func (l TableLayout) Total() int {
	total := l.Name
	if l.Indicator {
		total += colIndicator + sep
	}
	if l.Index {
		total += colIndex + sep
	}
	if l.State {
		total += colState + sep
	}
	if l.Git > 0 {
		total += sep + l.Git
	}
	if l.PR > 0 {
		total += sep + l.PR
	}
	return total
}

// LayoutForWidth picks the widest layout that fits, for width >= 1. Columns
// drop in reverse order of what a glance needs: git first, then the quick-jump
// index (with the PR column going compact at the same time), then the PR, then
// the indicator, and the state dot only when there is nothing left to give.
func LayoutForWidth(width int) TableLayout {
	switch {
	case width >= tierFull:
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-fullFixed, nameMin, colName),
			Git:  colGit, PR: colPR,
		}
	case width >= tierNoGit:
		return TableLayout{
			Indicator: true, Index: true, State: true,
			Name: clamp(width-noGitFixed, nameMin, colName),
			PR:   colPR,
		}
	case width >= tierCompact:
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-compactFixed, nameMin, colName),
			PR:   colPRCompact,
		}
	case width >= tierNoPR:
		return TableLayout{
			Indicator: true, State: true,
			Name: clamp(width-noPRFixed, nameMin, colName),
		}
	case width >= tierBare:
		return TableLayout{State: true, Name: clamp(width-bareFixed, 1, colName)}
	default:
		return TableLayout{Name: clamp(width, 1, colName)}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// TruncateVisible cuts s to width visible columns, ignoring ANSI escapes and
// closing any style it cuts through so the color does not bleed into the rest
// of the row.
func TruncateVisible(s string, width int) string {
	if visibleLen(s) <= width {
		return s
	}
	var b strings.Builder
	visible, cut := 0, false
	inEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\x1b' {
			inEscape = true
			b.WriteByte(c)
			continue
		}
		if inEscape {
			b.WriteByte(c)
			if c == 'm' {
				inEscape = false
			}
			continue
		}
		// The budget is only allowed to run out at a rune start. Checking it
		// per byte would let a multi-byte character's lead byte through and
		// then break before its continuation bytes, emitting invalid UTF-8
		// that visibleLen still counts as exactly one column.
		if c < 0x80 || c >= 0xC0 {
			if visible >= width {
				cut = true
				break
			}
			visible++
		}
		b.WriteByte(c)
	}
	if cut {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// truncateName cuts an unstyled session name, marking the cut. Names come
// straight from tmux and carry no escapes, so this counts runes.
func truncateName(name string, width int) string {
	runes := []rune(name)
	if len(runes) <= width {
		return name
	}
	if width <= 1 {
		return string(runes[:max(0, width)])
	}
	return string(runes[:width-1]) + "…"
}

// VisibleWidth returns how many terminal columns s occupies, ignoring ANSI
// escapes. Exported so callers rendering into a fixed-width pane - and the
// tests that check they did - have one definition of "what is actually on
// screen" to work from.
func VisibleWidth(s string) int {
	return visibleLen(s)
}

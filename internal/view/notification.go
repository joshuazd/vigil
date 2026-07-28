package view

import (
	"regexp"
	"strings"
)

// ansiPattern matches CSI ANSI escapes; mirror of the helper in internal/action.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// StripANSI removes escape sequences. Exported so tests asserting on rendered
// output - what is actually on screen, not the styled string - have one
// definition to work from instead of reimplementing it per package.
func StripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// clampNotification normalizes a notification string for safe rendering as a
// single table row: strips ANSI sequences, takes the last non-empty line, and
// truncates to width with an ellipsis if needed. Returns "" for empty input
// or width <= 0.
func clampNotification(s string, width int) string {
	if s == "" || width <= 0 {
		return ""
	}
	s = ansiPattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	line := ""
	for _, candidate := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" {
			line = trimmed
		}
	}
	if line == "" {
		return ""
	}
	if visibleLen(line) <= width {
		return line
	}
	runes := []rune(line)
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

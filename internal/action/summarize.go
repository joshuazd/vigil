package action

import (
	"regexp"
	"strings"
)

// successMessages maps action names to their canonical success-notification strings.
var successMessages = map[string]string{
	"cleanup":  "cleaned up",
	"merge":    "merged",
	"approve":  "approved",
	"rebase":   "rebased and pushed",
	"dispatch": "dispatched",
}

// SuccessMessage returns the canonical single-line success notification for an action.
// Unknown actions get a generic "<action> ok" so the caller never sees an empty string.
func SuccessMessage(action string) string {
	if msg, ok := successMessages[action]; ok {
		return msg
	}
	return action + " ok"
}

// FailureMessage builds a single-line failure notification: "<action> failed: <detail>".
// Detail is the last non-empty line of output (ANSI stripped). If output is empty, it
// falls back to err.Error() with the same last-line normalization. If both are empty,
// returns "<action> failed: unknown error".
func FailureMessage(action, output string, err error) string {
	detail := lastNonEmptyLine(stripANSI(output))
	if detail == "" && err != nil {
		detail = lastNonEmptyLine(err.Error())
	}
	if detail == "" {
		detail = "unknown error"
	}
	return action + " failed: " + detail
}

// ansiPattern matches CSI-style ANSI escape sequences (the colors emitted by lib/output.sh).
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// lastNonEmptyLine returns the last non-empty trimmed line of s, treating both
// "\n" and "\r\n" as line separators. Returns "" if s has no non-empty line.
func lastNonEmptyLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for i := len(s); i > 0; {
		// find the previous newline
		j := strings.LastIndex(s[:i], "\n")
		line := strings.TrimSpace(s[j+1 : i])
		if line != "" {
			return line
		}
		if j < 0 {
			return ""
		}
		i = j
	}
	return ""
}

package view

import (
	"strings"
	"testing"
)

func TestClampNotification_EmptyInput(t *testing.T) {
	got := clampNotification("", 80)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestClampNotification_SingleLineFits(t *testing.T) {
	got := clampNotification("hello", 80)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestClampNotification_MultiLineKeepsLastNonEmpty(t *testing.T) {
	got := clampNotification("first\nsecond\nthird\n", 80)
	if got != "third" {
		t.Errorf("got %q, want %q", got, "third")
	}
}

func TestClampNotification_StripsANSI(t *testing.T) {
	got := clampNotification("\x1b[32mhello\x1b[0m", 80)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestClampNotification_TruncatesWithEllipsis(t *testing.T) {
	got := clampNotification("abcdefghijklmnopqrstuvwxyz", 10)
	if got != "abcdefghi…" {
		t.Errorf("got %q, want %q", got, "abcdefghi…")
	}
	if visibleLen(got) != 10 {
		t.Errorf("visible length = %d, want 10", visibleLen(got))
	}
}

func TestClampNotification_AllBlankLines(t *testing.T) {
	got := clampNotification("\n\n   \n", 80)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestClampNotification_NeverHasNewline(t *testing.T) {
	inputs := []string{
		"a\nb\nc",
		"\x1b[32m>>> info\x1b[0m\n\x1b[31m!!! err\x1b[0m\n",
		"only one",
		"first\r\nsecond\r\n",
	}
	for _, in := range inputs {
		got := clampNotification(in, 80)
		if strings.Contains(got, "\n") {
			t.Errorf("input %q produced newline-containing output %q", in, got)
		}
	}
}

func TestClampNotification_ZeroWidth(t *testing.T) {
	got := clampNotification("hello", 0)
	if got != "" {
		t.Errorf("got %q, want empty for width 0", got)
	}
}

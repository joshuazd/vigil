package action

import (
	"errors"
	"testing"
)

func TestSuccessMessage_KnownActions(t *testing.T) {
	cases := map[string]string{
		"cleanup":  "cleaned up",
		"merge":    "merged",
		"approve":  "approved",
		"rebase":   "rebased and pushed",
		"dispatch": "dispatched",
	}
	for action, want := range cases {
		got := SuccessMessage(action)
		if got != want {
			t.Errorf("SuccessMessage(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestSuccessMessage_UnknownAction(t *testing.T) {
	got := SuccessMessage("frobnicate")
	if got != "frobnicate ok" {
		t.Errorf("SuccessMessage(unknown) = %q, want %q", got, "frobnicate ok")
	}
}

func TestFailureMessage_LastNonEmptyLineFromOutput(t *testing.T) {
	out := "first line\nsecond line\nthird line\n"
	got := FailureMessage("cleanup", out, errors.New("exit 1"))
	want := "cleanup failed: third line"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_StripsANSI(t *testing.T) {
	out := "\x1b[32m>>> Cleanup complete!\x1b[0m\n"
	got := FailureMessage("cleanup", out, errors.New("exit 1"))
	want := "cleanup failed: >>> Cleanup complete!"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_EmptyOutputFallsBackToErr(t *testing.T) {
	got := FailureMessage("rebase", "", errors.New("network down"))
	want := "rebase failed: network down"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_MultiLineErrFlattens(t *testing.T) {
	got := FailureMessage("rebase", "", errors.New("first\nsecond"))
	want := "rebase failed: second"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_TrailingBlankLinesIgnored(t *testing.T) {
	out := "real line\n\n\n"
	got := FailureMessage("cleanup", out, errors.New("x"))
	want := "cleanup failed: real line"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_OutputWithCarriageReturns(t *testing.T) {
	// Bash output sometimes uses \r\n
	out := "step one\r\nstep two\r\n"
	got := FailureMessage("cleanup", out, errors.New("x"))
	want := "cleanup failed: step two"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_NilError_UsesOutput(t *testing.T) {
	// Defensive: caller passes nil err but failure path still invoked.
	got := FailureMessage("cleanup", "boom", nil)
	want := "cleanup failed: boom"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

func TestFailureMessage_NilErrorAndEmptyOutput(t *testing.T) {
	got := FailureMessage("cleanup", "", nil)
	want := "cleanup failed: unknown error"
	if got != want {
		t.Errorf("FailureMessage = %q, want %q", got, want)
	}
}

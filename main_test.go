package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseArgsReturnsTheRemainingArguments(t *testing.T) {
	cmd, rest, err := parseArgs([]string{"daemon"})
	if err != nil {
		t.Fatalf("parseArgs returned %v, want nil", err)
	}
	if cmd != "daemon" {
		t.Errorf("got command %q, want daemon", cmd)
	}
	if len(rest) != 0 {
		t.Errorf("got rest %v, want empty", rest)
	}
}

func TestParseArgsRejectsAnUnknownArgument(t *testing.T) {
	if _, _, err := parseArgs([]string{"nonsense"}); err == nil {
		t.Fatal("parseArgs accepted an unknown argument, want an error")
	}
}

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "vigil ") {
		t.Errorf("got stdout %q, want it to start with \"vigil \"", stdout.String())
	}
}

func TestRunPrintsHelpToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("got exit code %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("got stdout %q, want it to contain \"Usage:\"", stdout.String())
	}
}

func TestRunRejectsAnUnknownArgumentWithExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"nonsense"}, &stdout, &stderr); code != 2 {
		t.Fatalf("got exit code %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown argument") {
		t.Errorf("got stderr %q, want it to mention the unknown argument", stderr.String())
	}
}

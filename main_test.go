package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args runs the tui", nil, "tui"},
		{"daemon subcommand", []string{"daemon"}, "daemon"},
		{"long help", []string{"--help"}, "help"},
		{"short help", []string{"-h"}, "help"},
		{"long version", []string{"--version"}, "version"},
		{"short version", []string{"-v"}, "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	got, err := parseArgs([]string{"--bogus"})
	if err == nil {
		t.Fatalf("want an error, got command %q", got)
	}
	if !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("error %q should name the offending argument", err)
	}
}

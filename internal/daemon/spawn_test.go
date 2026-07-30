package daemon

import (
	"reflect"
	"testing"
)

func TestWithoutTmuxEnvDropsOnlyTheTmuxVars(t *testing.T) {
	got := withoutTmuxEnv([]string{
		"PATH=/usr/bin", "TMUX=/tmp/tmux-501/default,123,4",
		"TMUX_PANE=%7", "TMUXP=keep", "HOME=/Users/x",
	})
	want := []string{"PATH=/usr/bin", "TMUXP=keep", "HOME=/Users/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

package fetch

import "testing"

func TestParseChecksPass(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "SUCCESS"},
	}
	if parseChecks(rollup) != "pass" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksFail(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "FAILURE"},
	}
	if parseChecks(rollup) != "fail" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksPending(t *testing.T) {
	rollup := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"status": "IN_PROGRESS"},
	}
	if parseChecks(rollup) != "pending" {
		t.Errorf("got %q", parseChecks(rollup))
	}
}

func TestParseChecksEmpty(t *testing.T) {
	if parseChecks(nil) != "" {
		t.Errorf("got %q, want empty", parseChecks(nil))
	}
}

func TestGetNWOSSH(t *testing.T) {
	// Clear cache
	nwoCache.Delete("/repo")

	mock := NewMockCommander()
	mock.OnArgs("git remote get-url origin", "git@github.com:owner/repo.git", nil)

	nwo := getNWO(nil, mock, "/repo")
	if nwo[0] != "owner" || nwo[1] != "repo" {
		t.Errorf("got %v, want [owner repo]", nwo)
	}

	nwoCache.Delete("/repo")
}

func TestGetNWOHTTPS(t *testing.T) {
	nwoCache.Delete("/repo2")

	mock := NewMockCommander()
	mock.OnArgs("git remote get-url origin", "https://github.com/myorg/myrepo.git", nil)

	nwo := getNWO(nil, mock, "/repo2")
	if nwo[0] != "myorg" || nwo[1] != "myrepo" {
		t.Errorf("got %v, want [myorg myrepo]", nwo)
	}

	nwoCache.Delete("/repo2")
}

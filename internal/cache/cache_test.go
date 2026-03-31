package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

func makeSession(name string) *session.Session {
	return &session.Session{Name: name, PanePath: "/tmp", Created: 1000}
}

func TestRoundTripBasic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	sessions := []*session.Session{makeSession("test")}
	if err := Save(p, sessions); err != nil {
		t.Fatal(err)
	}
	loaded := Load(p, 30*time.Second)
	if loaded == nil || len(loaded) != 1 {
		t.Fatal("expected 1 session")
	}
	if loaded[0].Name != "test" {
		t.Errorf("got %q, want test", loaded[0].Name)
	}
}

func TestRoundTripWithPR(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	s := makeSession("test")
	s.Git = session.GitStatus{Branch: "feat"}
	s.PR = &session.PRStatus{Number: 42, State: "OPEN", Checks: "pass", URL: "https://example.com"}
	if err := Save(p, []*session.Session{s}); err != nil {
		t.Fatal(err)
	}
	loaded := Load(p, 30*time.Second)
	if loaded[0].PR == nil {
		t.Fatal("expected PR")
	}
	if loaded[0].PR.Number != 42 {
		t.Errorf("got %d, want 42", loaded[0].PR.Number)
	}
	if loaded[0].PR.Checks != "pass" {
		t.Errorf("got %q, want pass", loaded[0].PR.Checks)
	}
}

func TestRoundTripWithGit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	s := makeSession("test")
	s.Git = session.GitStatus{Branch: "feat", Modified: 3, Unpushed: 1}
	if err := Save(p, []*session.Session{s}); err != nil {
		t.Fatal(err)
	}
	loaded := Load(p, 30*time.Second)
	if loaded[0].Git.Branch != "feat" {
		t.Errorf("got %q", loaded[0].Git.Branch)
	}
	if loaded[0].Git.Modified != 3 {
		t.Errorf("got %d", loaded[0].Git.Modified)
	}
}

func TestStaleReturnsNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	data, _ := json.Marshal(map[string]any{
		"version":   1,
		"timestamp": time.Now().Unix() - 60,
		"sessions":  []any{},
	})
	os.WriteFile(p, data, 0o644)
	if Load(p, 30*time.Second) != nil {
		t.Error("expected nil for stale cache")
	}
}

func TestMissingReturnsNil(t *testing.T) {
	if Load("/nonexistent/cache.json", 30*time.Second) != nil {
		t.Error("expected nil for missing file")
	}
}

func TestMalformedReturnsNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	os.WriteFile(p, []byte("not json"), 0o644)
	if Load(p, 30*time.Second) != nil {
		t.Error("expected nil for malformed JSON")
	}
}

func TestWrongVersionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	data, _ := json.Marshal(map[string]any{
		"version":   999,
		"timestamp": time.Now().Unix(),
		"sessions":  []any{},
	})
	os.WriteFile(p, data, 0o644)
	if Load(p, 30*time.Second) != nil {
		t.Error("expected nil for wrong version")
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	Save(p, []*session.Session{makeSession("test")})
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("got %o, want 600", mode)
	}
}

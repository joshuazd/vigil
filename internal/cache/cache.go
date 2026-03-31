package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/jzinkduda/vigil/internal/session"
)

const CacheVersion = 1

// CachePath returns the default cache file path.
func CachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "vigil", "cache.json")
}

type cacheFile struct {
	Version   int            `json:"version"`
	Timestamp int64          `json:"timestamp"`
	Sessions  []cacheSession `json:"sessions"`
}

type cacheSession struct {
	Name     string            `json:"name"`
	PanePath string            `json:"pane_path"`
	Created  int64             `json:"created"`
	HasBell  bool              `json:"has_bell"`
	Git      cacheGit          `json:"git"`
	PR       *session.PRStatus `json:"pr,omitempty"`
}

type cacheGit struct {
	Branch        string `json:"branch"`
	GitRoot       string `json:"git_root"`
	Modified      int    `json:"modified"`
	Added         int    `json:"added"`
	Deleted       int    `json:"deleted"`
	Unpushed      int    `json:"unpushed"`
	RebaseAgeSecs *int   `json:"rebase_age_seconds"`
}

// Save writes sessions to cache file atomically.
func Save(path string, sessions []*session.Session) error {
	cf := cacheFile{
		Version:   CacheVersion,
		Timestamp: time.Now().Unix(),
		Sessions:  make([]cacheSession, len(sessions)),
	}
	for i, s := range sessions {
		cs := cacheSession{
			Name:     s.Name,
			PanePath: s.PanePath,
			Created:  s.Created,
			HasBell:  s.HasBell,
			Git: cacheGit{
				Branch:        s.Git.Branch,
				GitRoot:       s.Git.GitRoot,
				Modified:      s.Git.Modified,
				Added:         s.Git.Added,
				Deleted:       s.Git.Deleted,
				Unpushed:      s.Git.Unpushed,
				RebaseAgeSecs: s.Git.RebaseAgeSecs,
			},
		}
		if s.PR != nil {
			cs.PR = s.PR
		}
		cf.Sessions[i] = cs
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}

	// Atomic write via temp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads sessions from cache. Returns nil if missing, stale, or invalid.
func Load(path string, cacheTTL time.Duration) []*session.Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil
	}
	if cf.Version != CacheVersion {
		return nil
	}
	if time.Now().Unix()-cf.Timestamp > int64(cacheTTL.Seconds()) {
		return nil
	}
	sessions := make([]*session.Session, len(cf.Sessions))
	for i, cs := range cf.Sessions {
		s := &session.Session{
			Name:     cs.Name,
			PanePath: cs.PanePath,
			Created:  cs.Created,
			HasBell:  cs.HasBell,
			Git: session.GitStatus{
				Branch:        cs.Git.Branch,
				GitRoot:       cs.Git.GitRoot,
				Modified:      cs.Git.Modified,
				Added:         cs.Git.Added,
				Deleted:       cs.Git.Deleted,
				Unpushed:      cs.Git.Unpushed,
				RebaseAgeSecs: cs.Git.RebaseAgeSecs,
			},
		}
		if cs.PR != nil {
			s.PR = cs.PR
		}
		sessions[i] = s
	}
	return sessions
}

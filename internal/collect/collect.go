package collect

import (
	"context"
	"sync"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

const defaultGitWorkers = 8

type Collector struct {
	Cmd        fetch.Commander
	GitWorkers int
}

func New(cfg *config.Config, cmd fetch.Commander) *Collector {
	workers := cfg.GetSettingInt("git_workers")
	if workers <= 0 {
		workers = defaultGitWorkers
	}
	return &Collector{Cmd: cmd, GitWorkers: workers}
}

func (c *Collector) Snapshot(ctx context.Context) ([]*session.Session, error) {
	raw, err := fetch.ListSessions(ctx, c.Cmd)
	if err != nil {
		return nil, err
	}

	bells := fetch.BellFlags(ctx, c.Cmd)

	sessions := make([]*session.Session, len(raw))
	for i, r := range raw {
		sessions[i] = &session.Session{
			Name:     r.Name,
			PanePath: r.PanePath,
			Created:  r.Created,
			HasBell:  bells[r.Name],
		}
	}

	c.fillGit(ctx, sessions)
	c.fillPRs(ctx, sessions)
	return sessions, nil
}

func (c *Collector) fillGit(ctx context.Context, sessions []*session.Session) {
	sem := make(chan struct{}, c.GitWorkers)
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.Git = fetch.FetchGitStatus(ctx, c.Cmd, s.PanePath)
		}(s)
	}
	wg.Wait()
}

func (c *Collector) fillPRs(ctx context.Context, sessions []*session.Session) {
	sem := make(chan struct{}, c.GitWorkers)
	var wg sync.WaitGroup
	for _, s := range sessions {
		if s.Git.Branch == "" || s.Git.GitRoot == "" {
			continue
		}
		wg.Add(1)
		go func(s *session.Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.PR = fetch.FetchPRStatus(ctx, c.Cmd, s.Git.Branch, s.Git.GitRoot)
		}(s)
	}
	wg.Wait()
}

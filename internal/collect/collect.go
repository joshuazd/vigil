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

func runParallel[T any](items []T, workers int, do func(T)) {
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			do(item)
		}(item)
	}
	wg.Wait()
}

func (c *Collector) fillGit(ctx context.Context, sessions []*session.Session) {
	runParallel(sessions, c.GitWorkers, func(s *session.Session) {
		s.Git = fetch.FetchGitStatus(ctx, c.Cmd, s.PanePath)
	})
}

func (c *Collector) fillPRs(ctx context.Context, sessions []*session.Session) {
	type branchRoot struct {
		branch, gitRoot string
		sessions        []*session.Session
	}
	branchMap := make(map[string]*branchRoot)
	var branches []*branchRoot

	for _, s := range sessions {
		if s.Git.Branch == "" || s.Git.GitRoot == "" {
			continue
		}
		key := s.Git.Branch + "\x00" + s.Git.GitRoot
		if br, exists := branchMap[key]; exists {
			br.sessions = append(br.sessions, s)
		} else {
			br := &branchRoot{branch: s.Git.Branch, gitRoot: s.Git.GitRoot}
			br.sessions = append(br.sessions, s)
			branchMap[key] = br
			branches = append(branches, br)
		}
	}

	runParallel(branches, c.GitWorkers, func(br *branchRoot) {
		pr := fetch.FetchPRStatus(ctx, c.Cmd, br.branch, br.gitRoot)
		for _, s := range br.sessions {
			s.PR = pr
		}
	})
}

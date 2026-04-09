package session

import (
	"strconv"
	"strings"
)

// GitStatus holds parsed git status for a session's working directory.
type GitStatus struct {
	Branch          string `json:"branch"`
	GitRoot         string `json:"git_root"`
	Modified        int    `json:"modified"`
	Added           int    `json:"added"`
	Deleted         int    `json:"deleted"`
	Unpushed        int    `json:"unpushed"`
	RebaseAgeSecs   *int   `json:"rebase_age_seconds"`
}

func (g *GitStatus) IsClean() bool {
	return g.Modified == 0 && g.Added == 0 && g.Deleted == 0 && g.Unpushed == 0
}

func (g *GitStatus) RebaseAgeDisplay() string {
	if g.RebaseAgeSecs == nil || *g.RebaseAgeSecs < 3600 {
		return ""
	}
	if *g.RebaseAgeSecs < 86400 {
		return "↻ " + strconv.Itoa(*g.RebaseAgeSecs/3600) + "h"
	}
	return "↻ " + strconv.Itoa(*g.RebaseAgeSecs/86400) + "d"
}

func (g *GitStatus) IsStale(threshold int) bool {
	return g.RebaseAgeSecs != nil && *g.RebaseAgeSecs > threshold
}

func (g *GitStatus) Display() string {
	var parts []string
	if g.Modified > 0 {
		parts = append(parts, "~"+strconv.Itoa(g.Modified))
	}
	if g.Added > 0 {
		parts = append(parts, "+"+strconv.Itoa(g.Added))
	}
	if g.Deleted > 0 {
		parts = append(parts, "-"+strconv.Itoa(g.Deleted))
	}
	if g.Unpushed > 0 {
		parts = append(parts, "↑"+strconv.Itoa(g.Unpushed))
	}
	if age := g.RebaseAgeDisplay(); age != "" {
		parts = append(parts, age)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// ReviewComment is a single comment from a PR review thread.
type ReviewComment struct {
	Author   string `json:"author"`
	Body     string `json:"body"`
	Path     string `json:"path"`
	Resolved bool   `json:"resolved"`
}

// PRStatus holds GitHub PR state for a branch.
type PRStatus struct {
	Number             int             `json:"number"`
	State              string          `json:"state"` // OPEN, MERGED, CLOSED
	IsDraft            bool            `json:"is_draft"`
	URL                string          `json:"url"`
	Checks             string          `json:"checks"` // pass, fail, pending, ""
	ReviewDecision     string          `json:"review_decision"`
	Approvals          int             `json:"approvals"`
	UnresolvedComments int             `json:"unresolved_comments"`
	HasConflicts       bool            `json:"has_conflicts"`
	ReviewersRequested int             `json:"reviewers_requested"`
	Title              string          `json:"title"`
	Body               string          `json:"body"`
	ReviewComments     []ReviewComment `json:"review_comments"`
}

func (p *PRStatus) Display() string {
	if p.Number == 0 {
		return "—"
	}
	parts := []string{"#" + strconv.Itoa(p.Number)}
	switch p.Checks {
	case "pass":
		parts = append(parts, "✓")
	case "fail":
		parts = append(parts, "✗")
	case "pending":
		parts = append(parts, "●")
	}
	if p.State == "OPEN" {
		switch {
		case p.ReviewDecision == "APPROVED":
			parts = append(parts, "☑")
		case p.ReviewDecision == "CHANGES_REQUESTED":
			parts = append(parts, "✎")
		case p.Approvals > 0:
			parts = append(parts, "☑")
		}
		if p.UnresolvedComments > 0 {
			parts = append(parts, "☐ "+strconv.Itoa(p.UnresolvedComments))
		}
		if !p.IsDraft && p.ReviewersRequested == 0 && p.ReviewDecision == "" && p.Approvals == 0 {
			parts = append(parts, "⚠")
		}
	}
	return strings.Join(parts, " ")
}

// Session represents a tmux session with its git and PR state.
type Session struct {
	Name      string    `json:"name"`
	PanePath  string    `json:"pane_path"`
	Created   int64     `json:"created"`
	IsCurrent bool      `json:"-"`
	IsLast    bool      `json:"-"`
	HasBell   bool      `json:"has_bell"`
	Git       GitStatus `json:"git"`
	PR        *PRStatus `json:"pr,omitempty"`
}

func (s *Session) State() SessionState {
	if s.HasBell {
		return Attention
	}
	if s.PR == nil || s.PR.Number == 0 {
		return Idle
	}
	if s.PR.State == "MERGED" {
		return Done
	}
	if s.PR.Checks == "fail" || s.PR.ReviewDecision == "CHANGES_REQUESTED" {
		return Blocked
	}
	if s.PR.HasConflicts {
		return Blocked
	}
	if s.PR.UnresolvedComments > 0 {
		return Unresolved
	}
	if s.PR.ReviewDecision == "APPROVED" && s.PR.Checks == "pass" {
		return Mergeable
	}
	if s.PR.ReviewDecision == "APPROVED" {
		return Approved
	}
	if s.PR.Checks == "pending" {
		return Pending
	}
	if s.PR.State == "OPEN" && !s.PR.IsDraft {
		return Review
	}
	return Idle
}

func (s *Session) Indicator() string {
	if s.IsCurrent {
		return "▸"
	}
	if s.IsLast {
		return "·"
	}
	return " "
}


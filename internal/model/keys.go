package model

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Quit             key.Binding
	Down             key.Binding
	Up               key.Binding
	Select           key.Binding
	OpenPR           key.Binding
	MergePR          key.Binding
	ApprovePR        key.Binding
	Cleanup          key.Binding
	ToggleDraft      key.Binding
	Dispatch         key.Binding
	RebasePush       key.Binding
	Refresh          key.Binding
	ToggleDetail     key.Binding
	CycleFilter      key.Binding
	CycleFilterBack  key.Binding
	CycleDetailMode  key.Binding
	CycleSort        key.Binding
	CycleSortBack    key.Binding
	ToggleSelect     key.Binding
	Cancel           key.Binding
}

var keys = keyMap{
	Quit:             key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	Down:             key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j", "down")),
	Up:               key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k", "up")),
	Select:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	OpenPR:           key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open PR")),
	MergePR:          key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "merge")),
	ApprovePR:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve")),
	Cleanup:          key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cleanup")),
	ToggleDraft:      key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "draft")),
	Dispatch:         key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dispatch")),
	RebasePush:       key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "rebase")),
	Refresh:          key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	ToggleDetail:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "detail")),
	CycleFilter:      key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
	CycleFilterBack:  key.NewBinding(key.WithKeys("F")),
	CycleDetailMode:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "panel")),
	CycleSort:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
	CycleSortBack:    key.NewBinding(key.WithKeys("S")),
	ToggleSelect:     key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "select")),
	Cancel:           key.NewBinding(key.WithKeys("esc")),
}

// ShortHelp returns the visible keybinding help for the footer.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.OpenPR, k.MergePR, k.ApprovePR, k.Cleanup,
		k.ToggleDraft, k.RebasePush, k.ToggleDetail,
		k.CycleFilter, k.CycleDetailMode, k.CycleSort,
	}
}

// FullHelp returns the full help (not used).
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

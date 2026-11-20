// Package wizard is the Bubble Tea front end for ffwiz: it routes between
// screens, remembers answers and renders the live command preview.
//
// It deliberately owns no ffmpeg or schema knowledge. Every rule about what a
// valid command looks like lives in internal/ffmpeg and internal/tasks, which
// import no TUI library at all (plan.md §3), so this package stays a thin
// controller and the rules stay testable without a terminal.
package wizard

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Styles holds every colour and decoration the wizard uses.
//
// Lip Gloss v2 removed AdaptiveColor, so a set is built for one known
// background and rebuilt when tea.BackgroundColorMsg arrives (the Bubbles
// UPGRADE_GUIDE_V2, "Light and Dark Styles").
type Styles struct {
	Breadcrumb     lipgloss.Style
	BreadcrumbOn   lipgloss.Style
	Heading        lipgloss.Style
	Hint           lipgloss.Style
	Cursor         lipgloss.Style
	ChoiceCurrent  lipgloss.Style
	ChoiceDisabled lipgloss.Style
	Muted          lipgloss.Style
	Placeholder    lipgloss.Style
	Key            lipgloss.Style
	Label          lipgloss.Style
	Error          lipgloss.Style
	Warning        lipgloss.Style
	OK             lipgloss.Style
	Rule           lipgloss.Style
	PaneTitle      lipgloss.Style
}

// NewStyles builds the style set for a terminal background.
//
// Colours are 256-colour indices rather than hex so the wizard looks the same in
// a terminal that only offers the basic palette. There are no gradients and no
// background fills: this is a terminal UI, and plan.md §6 asks for a plain,
// readable one.
func NewStyles(isDark bool) Styles {
	accent, ok, warn, bad, muted := "39", "42", "214", "203", "245"
	if !isDark {
		accent, ok, warn, bad, muted = "26", "28", "130", "160", "242"
	}
	return Styles{
		Breadcrumb:     lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		BreadcrumbOn:   lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Bold(true),
		Heading:        lipgloss.NewStyle().Bold(true),
		Hint:           lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		Cursor:         lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Bold(true),
		ChoiceCurrent:  lipgloss.NewStyle().Foreground(lipgloss.Color(ok)),
		ChoiceDisabled: lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		Muted:          lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		Placeholder:    lipgloss.NewStyle().Foreground(lipgloss.Color(warn)),
		Key:            lipgloss.NewStyle().Foreground(lipgloss.Color(accent)),
		Label:          lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		Error:          lipgloss.NewStyle().Foreground(lipgloss.Color(bad)),
		Warning:        lipgloss.NewStyle().Foreground(lipgloss.Color(warn)),
		OK:             lipgloss.NewStyle().Foreground(lipgloss.Color(ok)),
		Rule:           lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		PaneTitle:      lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Bold(true),
	}
}

// rule renders a full-width horizontal rule. Rules are used instead of borders
// so that the layout still fits an 80x24 terminal (plan.md §6).
func (s Styles) rule(width int) string {
	if width < 1 {
		width = 1
	}
	return s.Rule.Render(strings.Repeat("─", width))
}

// visibleWidth is the terminal width less the page margin.
func visibleWidth(width int) int {
	if width <= 0 {
		return 80
	}
	const margin = 2
	if width <= margin+8 {
		return width
	}
	return width - margin
}

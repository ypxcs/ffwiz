// Command ffwiz is a TUI wizard that helps people build correct ffmpeg commands.
//
// M0 SPIKE: this file exists only to prove that the Bubble Tea v2 API shapes
// compile before any real code is written. It is replaced in M1 by the real
// entry point (binary detection, flag parsing, program startup).
package main

import (
	"fmt"
	"os"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type spikeModel struct {
	fp       filepicker.Model
	spinner  spinner.Model
	viewport viewport.Model
	escapes  int
}

func newSpikeModel() spikeModel {
	fp := filepicker.New()
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	fp.CurrentDirectory = cwd

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	vp := viewport.New(viewport.WithWidth(40), viewport.WithHeight(3))
	vp.SetContent("viewport ok")

	return spikeModel{fp: fp, spinner: sp, viewport: vp}
}

// Init returns an optional initial command (v2.1 signature; the retracted
// v2.0.0-beta1 used `Init() (tea.Model, tea.Cmd)` — do not copy that).
func (m spikeModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m spikeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "e", "esc":
			m.escapes++
		}
	}

	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)
	return m, cmd
}

// v2 change: View returns tea.View; alt screen is a field on it.
func (m spikeModel) View() tea.View {
	style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	v := tea.NewView(style.Render(fmt.Sprintf("M0 spike — escapes: %d", m.escapes)) +
		"\n" + m.viewport.View())
	v.AltScreen = true
	return v
}

func main() {
	if _, err := tea.NewProgram(newSpikeModel()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

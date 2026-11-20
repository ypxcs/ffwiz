package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// enterOutput moves from the last question to the output path, prefilled with
// the default name from plan.md §6.5.
func (m Model) enterOutput() (tea.Model, tea.Cmd) {
	if m.engine == nil {
		return m, nil
	}
	path := m.outputPath
	if path == "" {
		derived, err := tasks.DefaultOutput(m.input, m.resolution())
		if err != nil {
			m.notice, m.noticeIsError = err.Error(), true
			return m, nil
		}
		path = derived
	}
	m.outputPath = path
	m.outputInput.SetValue(path)
	m.outputInput.SetCursor(utf8.RuneCountInString(path))
	m.outputInput.Focus()
	m.notice, m.noticeIsError = "", false
	m.screen = ScreenOutput
	return m, nil
}

// outputBody asks where to write, and shows the name that was derived.
func (m Model) outputBody(width int) string {
	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Where should the result go?"))
	b.WriteString("\n\n")
	b.WriteString("  " + m.outputInput.View() + "\n")

	if m.outputPath != "" {
		b.WriteString("  " + m.styles.Muted.Render(shortPath(dirOf(m.outputPath), width-2)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.Muted.Render("The file is written with -n, so an existing file is never replaced."))
	return b.String()
}

// hintOutput explains the screen's rule, which is the one rule that protects the
// user's data.
func (m Model) hintOutput() string {
	return "enter accepts this name; esc goes back to the last question"
}

// updateOutput edits the output path.
func (m Model) updateOutput(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if isKey {
		switch {
		case key.Matches(keyMsg, m.keys.BackEsc):
			m.notice, m.noticeIsError = "", false
			m.screen = ScreenStep
			m.stepIndex = max(len(m.activeSteps())-1, 0)
			m.syncStep()
			return m, nil
		case key.Matches(keyMsg, m.keys.Select):
			return m.confirmOutput()
		}
	}

	var cmd tea.Cmd
	m.outputInput, cmd = m.outputInput.Update(msg)
	// Keep the preview in step with what is typed: the command pane must always
	// show the path that would really be written.
	m.outputPath = m.outputInput.Value()
	return m, cmd
}

// confirmOutput checks the path and moves to the review screen.
//
// Overwrite protection is deliberately strict here. plan.md §6.5 offers
// choose-another-name, overwrite or cancel; the overwrite branch needs the run
// flow of M4 (a confirmation, and `-y` only afterwards), so until then the
// wizard refuses the existing name and says why.
func (m Model) confirmOutput() (tea.Model, tea.Cmd) {
	path := strings.TrimSpace(m.outputInput.Value())
	if path == "" {
		m.notice, m.noticeIsError = "the output path cannot be empty", true
		return m, nil
	}

	if same, err := sameFile(path, m.input); err == nil && same {
		m.notice, m.noticeIsError = "that is the input file; choose another name", true
		return m, nil
	}

	if _, err := os.Stat(path); err == nil {
		m.notice = filepath.Base(path) + " already exists; choose another name (overwrite arrives with the run screen)"
		m.noticeIsError = true
		return m, nil
	}

	m.outputPath = path
	m.outputInput.SetValue(path)
	m.notice, m.noticeIsError = "", false
	m.copied = false
	m.screen = ScreenReview
	return m, nil
}

// sameFile reports whether two paths name the same file, best effort.
func sameFile(a, b string) (bool, error) {
	if a == "" || b == "" {
		return false, nil
	}
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	return absA == absB, nil
}

// dirOf returns the directory part for display.
func dirOf(path string) string {
	dir := filepath.Dir(path)
	if dir == "." {
		return "in the current directory"
	}
	return "in " + dir
}

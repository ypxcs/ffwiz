package wizard

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// reviewBody is the plain-language summary plus the final command (plan.md §6.6).
func (m Model) reviewBody(width int) string {
	cmd, pending, err := m.liveCommand()
	if err != nil {
		return m.styles.Error.Render("cannot build the command: " + err.Error())
	}

	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Ready to go") + "\n\n")

	if m.edited != nil {
		// A hand-edited command is not what the answers add up to any more, so
		// the summary of those answers is left out rather than left to
		// contradict it (DECISIONS D44).
		b.WriteString(m.styles.Warning.Render(
			"this command was edited by hand. ffwiz did not build it and cannot\n"+
				"explain it; it will run exactly as written.") + "\n\n")
		b.WriteString("  " + pad(m.styles.Label.Render("Output:"), summaryWidth) + " " +
			shortPath(m.outputPath, max(width-summaryWidth-3, 8)) + "\n")
	} else {
		res := m.resolution()
		for _, step := range res.Active {
			answer := res.Answers[step.ID]
			label := answer
			if step.Type == tasks.StepChoice {
				if c, ok := res.Choices[step.ID]; ok && c.Label != "" {
					label = c.Label
				}
			}
			if label == "" {
				label = m.styles.Placeholder.Render("not answered")
			}
			b.WriteString("  " + pad(m.styles.Label.Render(step.Prompt+":"), summaryWidth) +
				" " + truncate(label, max(width-summaryWidth-4, 8)) + "\n")
		}

		b.WriteString("\n")
		// The destination is what this screen is for, so it is never
		// abbreviated away: when it does not fit beside its label it gets a
		// line of its own.
		label := "  " + pad(m.styles.Label.Render("Output:"), summaryWidth) + " "
		if lipgloss.Width(label)+lipgloss.Width(m.outputPath) <= width {
			b.WriteString(label + m.outputPath + "\n")
		} else {
			b.WriteString(strings.TrimRight(label, " ") + "\n")
			b.WriteString("  " + shortPath(m.outputPath, width-2) + "\n")
		}
	}

	if len(pending) > 0 {
		b.WriteString("\n" + m.styles.Warning.Render(
			"still unanswered: "+strings.Join(pending, ", ")) + "\n")
	}

	switch {
	case m.edited != nil:
		b.WriteString("\n" + m.styles.Muted.Render(m.editedNote()) + "\n")
	case m.overwrite:
		// Replacing a file is the one irreversible thing the wizard can do, so
		// the screen that offers the Run action says so plainly (plan.md §8).
		b.WriteString("\n" + m.styles.Warning.Render(
			"the existing file will be replaced if you run this") + "\n")
	case m.copied:
		b.WriteString("\n" + m.styles.OK.Render("copied to the clipboard") + "\n")
	default:
		b.WriteString("\n" + m.styles.Muted.Render(
			"press r to run it, c to copy it, x to edit it or s to save it as a script") + "\n")
	}

	if cmd == nil {
		b.WriteString(m.styles.Error.Render("there is no command yet") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// editedNote says what is still true of a hand-edited command. The destination
// is, which is the point: the edit screen refuses to change it, so every check
// the wizard makes about the output file still means something (DECISIONS D44).
func (m Model) editedNote() string {
	if m.overwrite {
		return "the file it names will be replaced, because the command says -y"
	}
	return "the existing file will not be replaced: the command still writes to the chosen name"
}

// summaryWidth is the label column width on the review screen.
const summaryWidth = 18

// pad right-pads a styled string to a visible width.
func pad(s string, width int) string {
	gap := width - visibleLen(s)
	if gap < 1 {
		gap = 1
	}
	return s + strings.Repeat(" ", gap)
}

// visibleLen is the un-styled length of a rendered string.
func visibleLen(s string) int {
	return len([]rune(stripANSI(s)))
}

// stripANSI removes SGR escape sequences so a rendered string can be measured.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == 0x1b:
			inEscape = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hintReview says what the actions do, and what is still missing.
func (m Model) hintReview() string {
	if m.edited != nil {
		return "this command is yours now; x to edit it again, u to go back to the one ffwiz built"
	}
	_, pending, err := m.liveCommand()
	if err != nil {
		return "the command cannot be built yet"
	}
	if len(pending) > 0 {
		return "some answers are missing; go back and fill them in"
	}
	return "press r to run it, c to copy it, x to edit it, s to save it as a script, or esc to change an answer"
}

// updateReview handles the review screen's actions (plan.md §6.6).
func (m Model) updateReview(keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if !isKey {
		return m, nil
	}
	switch {
	case key.Matches(keyMsg, m.keys.Run):
		return m.startRun()
	case key.Matches(keyMsg, m.keys.Copy):
		return m.copyCommand()
	case key.Matches(keyMsg, m.keys.Edit):
		return m.openEditor()
	case key.Matches(keyMsg, m.keys.Script):
		return m.saveScript()
	case key.Matches(keyMsg, m.keys.Revert) && m.edited != nil:
		// Undoing an edit is not a step back: the answers never changed, only
		// the command did (DECISIONS D44).
		m.edited = nil
		m.notice, m.noticeIsError = "back to the command ffwiz built from your answers", false
		return m, nil
	case key.Matches(keyMsg, m.keys.Back):
		m.notice, m.noticeIsError = "", false
		m.screen = ScreenOutput
		return m, nil
	}
	return m, nil
}

// copyCommand puts the shell-quoted command on the clipboard.
//
// The display form is copied, not the executed one: the execution-only flags
// (-nostdin, -progress) are meaningless in a pasted command and would only
// confuse (DECISIONS D5).
func (m Model) copyCommand() (tea.Model, tea.Cmd) {
	cmd, _, err := m.liveCommand()
	if err != nil || cmd == nil {
		m.notice, m.noticeIsError = "there is no command to copy yet", true
		return m, nil
	}
	text := cmd.String()
	clip := m.clip
	return m, func() tea.Msg { return copyMsg{err: clip.WriteAll(text)} }
}

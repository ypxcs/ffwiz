package wizard

import (
	"path/filepath"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// inputBody is the file picker, the probe summary, or a progress note.
//
// The summary is a second state of the same screen, not a screen of its own:
// plan.md §6.2 has the input step both choose a file and show what is in it, and
// a separate step would mean pressing enter twice to get anywhere.
func (m Model) inputBody(width int) string {
	switch {
	case m.probing:
		return m.styles.Muted.Render(truncate("Probing "+m.input+"...", width))
	case m.chosen:
		summary, err := mediaSummary(m.info)
		header := m.styles.Heading.Render("This file") + "  " +
			m.styles.Muted.Render(shortPath(m.input, width-12))
		if err != nil {
			return header + "\n\n" + m.styles.Error.Render(err.Error())
		}
		return header + "\n\n" + summary
	}

	header := m.styles.Heading.Render("Choose a file")
	if m.filtered {
		header += "  " + m.styles.Muted.Render("(media files only, a for all)")
	} else {
		header += "  " + m.styles.Warning.Render("(showing every file)")
	}
	dir := m.styles.Muted.Render(shortPath(m.picker.CurrentDirectory, width-24))

	return header + "  " + dir + "\n" + m.picker.View()
}

// hintInput explains the highlighted entry, and says why a file cannot be
// chosen when the picker refuses it.
func (m Model) hintInput() string {
	switch {
	case m.probing:
		return "reading the file with ffprobe"
	case m.chosen:
		return "enter continues to the task list; esc chooses a different file"
	case m.picker.HighlightedPath() != "":
		if !m.filtered {
			return "press a again to list only media files"
		}
		return "enter selects this file; esc goes up a directory"
	default:
		return "no files here; esc goes up a directory"
	}
}

// updateInput handles the file picker and the probe summary.
func (m Model) updateInput(msg tea.Msg, isKey bool, keyMsg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.probing {
		return m, nil
	}

	if m.chosen {
		if isKey && key.Matches(keyMsg, m.keys.Select) {
			m.screen = ScreenTask
			m.taskIndex = 0
			m.notice, m.noticeIsError = "", false
			return m, nil
		}
		if isKey && key.Matches(keyMsg, m.keys.Back) {
			m.chosen = false
			return m, m.picker.Init()
		}
		return m, nil
	}

	if isKey && key.Matches(keyMsg, m.keys.ShowAll) {
		// Toggling only changes what can be selected, so the directory listing
		// is left alone and the cursor stays where it was.
		m.setFilter(!m.filtered)
		m.notice, m.noticeIsError = "", false
		return m, nil
	}

	// Selection has to be checked before the picker sees the message, because
	// the picker consumes `enter` for its own navigation.
	if selected, path := m.picker.DidSelectFile(msg); selected {
		m.input, m.info, m.chosen = path, nil, false
		m.probing = true
		m.notice, m.noticeIsError = "", false
		return m, m.probeCmd(path)
	}
	if refused, path := m.picker.DidSelectDisabledFile(msg); refused {
		m.notice = filepath.Base(path) + " is not a media file; press a to show every file"
		m.noticeIsError = true
		return m, nil
	}

	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	return m, cmd
}

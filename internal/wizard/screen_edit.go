package wizard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// This file is the review screen's edit mode (plan.md §6.6). It exists because
// the wizard cannot know every ffmpeg flag, and a tool that refuses to let the
// user add one would be a worse tool for it. The edited text is parsed back into
// an argument slice and run exactly as written (DECISIONS D44).

// openEditor puts the current command into the text area.
func (m Model) openEditor() (tea.Model, tea.Cmd) {
	cmd, _, err := m.liveCommand()
	if err != nil || cmd == nil {
		m.notice, m.noticeIsError = "there is no command to edit yet", true
		return m, nil
	}
	m.editArea.SetValue(cmd.String())
	m.editErr = ""
	m.screen = ScreenEdit
	m.resize()
	return m, m.editArea.Focus()
}

// editBody is the text area plus what it needs to explain itself.
func (m Model) editBody(width int) string {
	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Edit the command") + "\n")
	b.WriteString(m.styles.Muted.Render(
		"ffwiz stops explaining this command once you change it. It must still\n"+
			"write to the file you chose on the output screen.") + "\n\n")

	b.WriteString(m.editArea.View())

	if m.editErr != "" {
		b.WriteString("\n" + m.styles.Error.Render("  "+truncate(m.editErr, max(width-2, 20))))
	}
	return b.String()
}

// hintEdit says what the two ways out of the editor are.
func (m Model) hintEdit() string {
	if m.editErr != "" {
		return m.editErr
	}
	return "press ctrl+s to use this command, or esc to keep the one ffwiz built"
}

// updateEdit handles the editor's keys. Every letter belongs to the text area,
// which is why the global handler asks textEntryFocused first.
func (m Model) updateEdit(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if isKey {
		switch {
		case key.Matches(keyMsg, m.keys.Apply):
			return m.applyEdit()
		case key.Matches(keyMsg, m.keys.BackEsc):
			// Cancelling leaves the command exactly as it was, edited or not.
			m.editErr = ""
			m.screen = ScreenReview
			return m, nil
		case keyMsg.String() == "ctrl+c":
			// A text field owns `q`, but ctrl+c is how a terminal user leaves.
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.editArea, cmd = m.editArea.Update(msg)
	return m, cmd
}

// applyEdit parses the text back into arguments and adopts it as the command.
//
// The one thing the edit may not do is change the destination. The output screen
// is where -y is decided, where the folder is checked for writability and where
// the free space was read; an edit that quietly retargeted the file would leave
// every one of those checks describing something else, and the done screen
// (size, reveal, delete, diagnose) would be looking at the wrong file too. So
// the new arguments have to keep naming the same output, and an edit that wants
// a different name is sent back to the output screen (DECISIONS D44).
func (m Model) applyEdit() (tea.Model, tea.Cmd) {
	args, err := ffmpeg.SplitCommand(m.editArea.Value())
	if err != nil {
		m.editErr = err.Error()
		return m, nil
	}
	if len(args) == 0 {
		m.editErr = "there is nothing to run"
		return m, nil
	}
	if !namesOutput(args, m.outputPath) {
		m.editErr = fmt.Sprintf(
			"the command must still write to %s; use esc and change the name on the output screen instead",
			filepath.Base(m.outputPath))
		return m, nil
	}

	m.edited = args
	// -y is the user's own from here on: it can be added to the text or taken
	// out of it, and the overwrite question follows what the text says.
	m.overwrite = slices.Contains(args, "-y")
	m.copied = false
	m.editErr = ""
	m.screen = ScreenReview
	m.notice = "the command is yours now: it will run exactly as written"
	m.noticeIsError = false
	return m, nil
}

// namesOutput reports whether an argument slice still names the chosen output
// file. Both spellings are accepted because a name beginning with "-" is
// prefixed when the command is built (ffmpeg.GuardPath).
func namesOutput(args []string, outputPath string) bool {
	if outputPath == "" {
		return false
	}
	guarded := ffmpeg.GuardPath(outputPath)
	for _, a := range args {
		if a == outputPath || a == guarded {
			return true
		}
	}
	return false
}

// saveScript writes the command out as a runnable script beside the output
// (plan.md §6.6).
//
// It refuses to replace an existing file rather than silently writing over a
// script the user may have edited: the same rule the wizard applies to media
// files (plan.md §8).
func (m Model) saveScript() (tea.Model, tea.Cmd) {
	cmd, _, err := m.liveCommand()
	if err != nil || cmd == nil {
		m.notice, m.noticeIsError = "there is no command to save yet", true
		return m, nil
	}
	path := scriptPath(m.outputPath)
	if path == "" {
		m.notice, m.noticeIsError = "there is nowhere to save a script: no output file was chosen", true
		return m, nil
	}
	if _, err := os.Stat(path); err == nil {
		m.notice, m.noticeIsError = filepath.Base(path)+" already exists; nothing was written", true
		return m, nil
	}

	// The script is executable: it carries a shebang, and a shell script saved
	// out of a wizard is meant to be run.
	if err := os.WriteFile(path, []byte(cmd.Script()), 0o755); err != nil {
		m.notice, m.noticeIsError = "could not write the script: "+err.Error(), true
		return m, nil
	}
	m.notice, m.noticeIsError = "wrote "+path, false
	return m, nil
}

// scriptPath is where a script version of the command goes: beside the output
// file, with the same stem and a .sh extension.
func scriptPath(outputPath string) string {
	if outputPath == "" {
		return ""
	}
	ext := filepath.Ext(outputPath)
	return strings.TrimSuffix(outputPath, ext) + ".sh"
}

package wizard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
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
	m.conflict, m.conflictIdx = false, 0
	m.overwrite = false
	m.notice, m.noticeIsError = "", false
	m.screen = ScreenOutput
	return m, nil
}

// outputBody asks where to write, and shows the name that was derived. When the
// name would replace an existing file it becomes the overwrite question instead
// (plan.md §6.5): the answer decides between -n and -y.
func (m Model) outputBody(width int) string {
	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Where should the result go?"))
	b.WriteString("\n\n")
	b.WriteString("  " + m.outputInput.View() + "\n")

	if m.outputPath != "" {
		b.WriteString("  " + m.styles.Muted.Render(shortPath(dirOf(m.outputPath), width-2)) + "\n")
	}
	b.WriteString("\n")

	if m.conflict {
		b.WriteString(m.styles.Warning.Render("  "+m.conflictText()) + "\n\n")
		for i, opt := range overwriteOptions {
			b.WriteString(m.conflictLine(opt, i == m.conflictIdx) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	if line := m.spaceLine(width); line != "" {
		b.WriteString(line + "\n\n")
	}

	b.WriteString(m.styles.Muted.Render("The file is written with -n, so an existing file is never replaced."))
	return b.String()
}

// spaceLine is plan.md §8's disk-space warning, or "" when there is nothing
// honest to say.
//
// Only a target-size step makes the output's size knowable before the run, so
// that is the only time an estimate appears; a CRF encode's size depends on the
// material and guessing it would produce a warning that is wrong as often as it
// is right. Free space is asked of the folder the file will land in, which
// says nothing about a different filesystem.
func (m Model) spaceLine(width int) string {
	expected := m.resolution().ExpectedSizeBytes
	if expected <= 0 || m.outputPath == "" {
		return ""
	}
	free, err := m.space(filepath.Dir(m.outputPath))
	if err != nil {
		// Best effort: a platform that cannot answer, or a folder that has just
		// gone away, is not worth an error on a screen that is about to write
		// there anyway - the writability check is what refuses a bad folder.
		return ""
	}

	estimate := format.Bytes(expected)
	if free >= expected {
		return "  " + m.styles.Muted.Render(
			fmt.Sprintf("The target is %s; the folder has %s free.", estimate, format.Bytes(free)))
	}
	return "  " + m.styles.Warning.Render(truncate(
		fmt.Sprintf("The target is %s but the folder has only %s free; the encode will stop when the disk fills.",
			estimate, format.Bytes(free)), max(width-2, 20)))
}

// overwriteOption is one answer to "that file already exists".
type overwriteOption struct {
	label string
	help  string
}

// overwriteOptions are the answers in a fixed order. Replacing a file is the
// destructive one, so it is not first and is not the default (plan.md §6.5,
// DECISIONS D28).
var overwriteOptions = []overwriteOption{
	{"Choose another name", "go back to the name and change it"},
	{"Overwrite it", "replace the existing file; the command gets -y"},
	{"Cancel", "leave this question and go back to the last one"},
}

// conflictLine renders one entry of the overwrite question.
func (m Model) conflictLine(opt overwriteOption, highlighted bool) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
	}
	label := opt.label
	if highlighted {
		if strings.HasPrefix(label, "Overwrite") || label == "Cancel" {
			// Only the destructive answers are coloured as caution; the safe one
			// needs no emphasis to be preferred.
			label = m.styles.Warning.Render(label)
		} else {
			label = lipgloss.NewStyle().Bold(true).Render(label)
		}
	} else {
		label = m.styles.Muted.Render(label)
	}
	return cursor + label
}

// hintOutput explains the screen's rule, which is the one rule that protects the
// user's data.
func (m Model) hintOutput() string {
	if m.conflict {
		return overwriteOptions[clamp(m.conflictIdx, 0, len(overwriteOptions)-1)].help
	}
	return "enter accepts this name; esc goes back to the last question"
}

// updateOutput edits the output path, or drives the overwrite question when it
// is open.
func (m Model) updateOutput(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if m.conflict {
		return m.updateConflict(keyMsg, isKey)
	}

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
	if path := m.outputInput.Value(); path != m.outputPath {
		m.outputPath = path
		// A different name is a different question: an overwrite agreed to for
		// the old name must not carry over to the new one.
		m.overwrite = false
	}
	return m, cmd
}

// updateConflict drives the overwrite question.
func (m Model) updateConflict(keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if !isKey {
		return m, nil
	}
	switch {
	case key.Matches(keyMsg, m.keys.BackEsc):
		// Esc closes the question and puts the cursor back in the name, which is
		// the answer that cannot lose anything. The destructive answer is never
		// reachable without pressing enter on it (DECISIONS D28).
		m.dismissConflict()
		return m, nil
	case key.Matches(keyMsg, m.keys.Up):
		m.conflictIdx = wrap(m.conflictIdx-1, len(overwriteOptions))
	case key.Matches(keyMsg, m.keys.Down):
		m.conflictIdx = wrap(m.conflictIdx+1, len(overwriteOptions))
	case key.Matches(keyMsg, m.keys.Select):
		return m.resolveConflict()
	}
	return m, nil
}

// dismissConflict closes the overwrite question and returns to the name field
// with the name selected, so typing replaces it.
func (m *Model) dismissConflict() {
	m.conflict = false
	m.conflictIdx = 0
	m.overwrite = false
	m.outputInput.Focus()
	m.outputInput.CursorEnd()
	m.notice, m.noticeIsError = "", false
}

// resolveConflict acts on the chosen answer.
func (m Model) resolveConflict() (tea.Model, tea.Cmd) {
	switch clamp(m.conflictIdx, 0, len(overwriteOptions)-1) {
	case 0: // Choose another name
		m.dismissConflict()
		m.notice = "type a different name, then press enter"
		m.noticeIsError = false
		return m, nil
	case 1: // Overwrite it
		m.conflict = false
		m.overwrite = true
		m.notice, m.noticeIsError = "", false
		return m.toReview()
	default: // Cancel
		m.dismissConflict()
		m.screen = ScreenStep
		m.stepIndex = max(len(m.activeSteps())-1, 0)
		m.syncStep()
		return m, nil
	}
}

// conflictText says what the overwrite question is about. An ordinary name is
// one file; a numbered sequence may already have written many frames, so it
// counts them rather than pretending the pattern itself is on disk
// (DECISIONS D34).
func (m Model) conflictText() string {
	base := filepath.Base(m.outputPath)
	n := len(ffmpeg.OutputFiles(m.outputPath))
	switch {
	case !ffmpeg.IsSequence(m.outputPath):
		return base + " already exists."
	case n == 1:
		return base + " matches an existing file."
	default:
		return fmt.Sprintf("%s matches %d existing files.", base, n)
	}
}

// existingNotice explains that the output turned up since the question was
// answered, which sends the user back to the output screen (DECISIONS D29).
func (m Model) existingNotice() string {
	base := filepath.Base(m.outputPath)
	n := len(ffmpeg.OutputFiles(m.outputPath))
	switch {
	case !ffmpeg.IsSequence(m.outputPath):
		return base + " now exists; go back and choose another name"
	case n == 1:
		return base + " now matches an existing file; go back and choose another name"
	default:
		return fmt.Sprintf("%s now matches %d existing files; go back and choose another name", base, n)
	}
}

// confirmOutput checks the path and moves to the review screen.
//
// Overwrite protection is deliberately strict. plan.md §6.5 offers
// choose-another-name, overwrite or cancel, and the overwrite branch is the only
// thing in the wizard that sets -y (plan.md §8): nothing else may replace a file
// that is already there.
func (m Model) confirmOutput() (tea.Model, tea.Cmd) {
	path := strings.TrimSpace(m.outputInput.Value())
	if path == "" {
		m.notice, m.noticeIsError = "the output path cannot be empty", true
		return m, nil
	}

	m.overwrite = false

	if same, err := sameFile(path, m.input); err == nil && same {
		m.notice, m.noticeIsError = "that is the input file; choose another name", true
		return m, nil
	}

	// plan.md §8: the directory has to exist and be writable before there is any
	// point starting a process that would fail at the last moment.
	if err := checkOutputDir(path); err != nil {
		m.notice, m.noticeIsError = err.Error(), true
		return m, nil
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		// A folder is not a name to be replaced: the only useful answer is
		// a different name, so the overwrite question is not offered.
		m.notice, m.noticeIsError = filepath.Base(path)+" is a folder, not a file; choose another name", true
		return m, nil
	}

	// The output may be one file or a numbered sequence, and with image2 an
	// existing frame is replaced even under -n (DECISIONS D34), so a sequence
	// whose frames are already there has to ask the same question.
	if len(ffmpeg.OutputFiles(path)) > 0 {
		m.outputPath = path
		m.conflict, m.conflictIdx = true, 0
		m.notice, m.noticeIsError = "", false
		return m, nil
	}

	m.outputPath = path
	return m.toReview()
}

// checkOutputDir reports why the output could not be written there, without
// creating anything that outlives the check.
//
// Each message leads with the reason and puts the folder last: the notice is cut
// to one line, and the reason is the part that has to survive it.
//
// plan.md §8 asks whether the folder is *writable*, not merely present. Mode bits
// cannot answer that - ACLs, read-only mounts and a file where a folder should be
// all defeat them - so the only honest test is to write something and delete it.
// The alternative is discovering it from ffmpeg after it has already read the
// whole input.
func checkOutputDir(path string) error {
	dir := filepath.Dir(path)
	short := shortPath(dir, 48)

	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("the output folder does not exist: %s", short)
	case err != nil:
		return fmt.Errorf("that output folder cannot be used (%v): %s", err, short)
	case !info.IsDir():
		return fmt.Errorf("%s is a file, not a folder", short)
	}

	probe, err := os.CreateTemp(dir, ".ffwiz-write-check-*")
	if err != nil {
		return fmt.Errorf("the output folder cannot be written to (%v): %s", err, short)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// toReview is the shared tail of the output question.
func (m Model) toReview() (tea.Model, tea.Cmd) {
	m.outputInput.SetValue(m.outputPath)
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

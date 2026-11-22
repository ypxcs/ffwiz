package wizard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
)

// This file is the done screen (plan.md §6.8): what happened, how big the result
// is, and what to do next.

// doneBody reports the outcome, the file it produced, and - when something went
// wrong - the end of ffmpeg's log and a plain-language hint.
func (m Model) doneBody(width int) string {
	var b strings.Builder

	switch m.outcome {
	case ffmpeg.OutcomeSucceeded:
		b.WriteString(m.styles.OK.Render("Done") + "\n\n")
	case ffmpeg.OutcomeCancelled:
		b.WriteString(m.styles.Warning.Render("Stopped") + "\n\n")
	case ffmpeg.OutcomeRefused:
		b.WriteString(m.styles.Error.Render("Nothing was written") + "\n\n")
	default:
		b.WriteString(m.styles.Error.Render("It did not work") + "\n\n")
	}

	if m.outputPath != "" {
		b.WriteString("  " + m.styles.Label.Render("Output:") + " " + shortPath(m.outputPath, max(width-12, 12)) + "\n")
	}

	switch m.outcome {
	case ffmpeg.OutcomeSucceeded:
		b.WriteString("  " + m.styles.Label.Render("Size:") + "   " + m.sizeLine() + "\n")
	case ffmpeg.OutcomeCancelled:
		switch {
		case m.removed:
			b.WriteString("  " + m.styles.OK.Render("the partial file was deleted") + "\n")
		case m.outputExists():
			// Measured (DECISIONS, verification note): ffmpeg finalises the
			// container on SIGINT, so the file plays - it is simply short. Saying
			// it "will not play" would be untrue.
			b.WriteString("  " + m.styles.Muted.Render("the file ends where the encode stopped and is shorter than intended") + "\n")
			b.WriteString("  " + m.styles.Muted.Render("press d to delete it") + "\n")
		}
	}
	b.WriteString("\n")

	if m.hintText != "" {
		b.WriteString(m.styles.Warning.Render("  "+m.hintText) + "\n")
	}
	if m.notice != "" {
		style := m.styles.Muted
		if m.noticeIsError {
			style = m.styles.Error
		}
		b.WriteString("  " + style.Render(truncate(m.notice, max(width-2, 8))) + "\n")
	}

	if m.outcome == ffmpeg.OutcomeFailed || m.outcome == ffmpeg.OutcomeRefused {
		b.WriteString(m.failureDetail(width))
	}
	return strings.TrimRight(b.String(), "\n")
}

// canReveal reports whether there is a file on disk worth showing in the desktop
// file manager. A key that leads nowhere is worse than no key: `o` on a run that
// wrote nothing would open a folder to look for a file that is not in it.
func (m Model) canReveal() bool {
	switch m.outcome {
	case ffmpeg.OutcomeSucceeded, ffmpeg.OutcomeCancelled:
		return m.outputExists()
	default:
		return false
	}
}

// outputExists reports whether the output is really on disk right now. A
// numbered sequence counts as soon as one frame is there, because that is the
// file the user can point at (DECISIONS D34).
func (m Model) outputExists() bool {
	return len(ffmpeg.OutputFiles(m.outputPath)) > 0
}

// sizeLine reports the output's size, and how it compares with the input when
// the input's size is known (plan.md §6.8). A sequence is measured as a whole:
// comparing one frame of it with the input video would say nothing useful.
func (m Model) sizeLine() string {
	files := ffmpeg.OutputFiles(m.outputPath)
	if len(files) == 0 {
		return m.styles.Muted.Render("the file could not be measured")
	}
	var total int64
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			total += info.Size()
		}
	}
	out := format.Bytes(total)
	if len(files) > 1 {
		return fmt.Sprintf("%s in %d files", out, len(files))
	}
	in := m.inputSize()
	if in <= 0 {
		return out
	}
	delta := float64(in-total) / float64(in) * 100
	switch {
	case delta > 1:
		return fmt.Sprintf("%s, %.0f%% smaller than the input", out, delta)
	case delta < -1:
		return fmt.Sprintf("%s, %.0f%% larger than the input", out, -delta)
	default:
		return fmt.Sprintf("%s, about the same size as the input", out)
	}
}

// inputSize is the size of the file that was read, or 0 when it is unknown.
func (m Model) inputSize() int64 {
	if m.info == nil {
		return 0
	}
	return m.info.Format.Size
}

// failureDetail shows the end of ffmpeg's log, which is where the reason always
// is (plan.md §6.8). With no log at all - a process that could not even be
// started - it falls back to the error itself, so a failure is never shown with
// nothing to explain it.
func (m Model) failureDetail(width int) string {
	lines := m.result.LastLines(8)
	if len(lines) == 0 {
		if m.result.Err == nil {
			return ""
		}
		return "\n" + m.styles.Muted.Render(
			"  "+truncate(m.result.Err.Error(), max(width-2, 8))) + "\n"
	}
	var b strings.Builder
	b.WriteString("\n" + m.styles.Muted.Render("  the end of ffmpeg's log:") + "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString("    " + m.styles.Muted.Render(truncate(line, max(width-4, 8))) + "\n")
	}
	return b.String()
}

// hintDone says what the keys do, which is different after a success, after a
// stop, and after a failure (plan.md §6.8).
//
// The failure hints do not repeat the reason - that is in the body, above the
// log - they say what to do about it.
func (m Model) hintDone() string {
	switch {
	case m.outcome == ffmpeg.OutcomeCancelled && m.removed:
		return "press esc to pick another task, or q to quit"
	case m.outcome == ffmpeg.OutcomeCancelled && m.outputExists():
		return "press d to delete the partial file, or esc to pick another task"
	case m.outcome == ffmpeg.OutcomeCancelled:
		return "press esc to pick another task, or o to look at the partial file"
	case m.outcome == ffmpeg.OutcomeSucceeded:
		return "press o to open the folder, or esc for another task on the same input"
	case m.outcome == ffmpeg.OutcomeRefused:
		return "press esc and pick the same task again to choose a different name"
	default:
		return "press esc and pick the same task again to change an answer"
	}
}

// updateDone handles the done screen's actions.
func (m Model) updateDone(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if !isKey {
		return m, nil
	}
	switch {
	case key.Matches(keyMsg, m.keys.Reveal):
		return m.revealOutput()
	case key.Matches(keyMsg, m.keys.Delete):
		return m.deletePartial()
	case key.Matches(keyMsg, m.keys.Back):
		// "Run another task on the same input" (plan.md §6.8): the file is kept
		// and only the answers are thrown away.
		if m.set != nil {
			m.taskIndex = m.firstRunnableTask()
			m.screen = ScreenTask
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// revealOutput asks the desktop to show the output file. A sequence is shown by
// its first frame, which is the one the file manager can put the cursor on.
func (m Model) revealOutput() (tea.Model, tea.Cmd) {
	if !m.canReveal() {
		return m, nil
	}
	files := ffmpeg.OutputFiles(m.outputPath)
	if len(files) == 0 {
		return m, nil
	}
	reveal := m.reveal
	path := files[0]
	return m, func() tea.Msg {
		return revealMsg{err: reveal(path)}
	}
}

// revealMsg reports the outcome of asking the desktop to open a folder.
type revealMsg struct{ err error }

// deletePartial removes what a cancelled run left behind, and only then: after a
// failure the file may be worth keeping to look at (plan.md §6.7). It also
// requires the output to be there: a cancel that landed before ffmpeg created
// anything has nothing to delete, and reporting otherwise would be a lie.
func (m Model) deletePartial() (tea.Model, tea.Cmd) {
	if m.outcome != ffmpeg.OutcomeCancelled || m.removed {
		return m, nil
	}
	files := ffmpeg.OutputFiles(m.outputPath)
	if len(files) == 0 {
		return m, nil
	}
	// Every frame of a stopped sequence goes: leaving some behind would look
	// like a complete result (DECISIONS D34).
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			m.notice, m.noticeIsError = "could not delete the partial file: "+err.Error(), true
			return m, nil
		}
	}
	m.removed = true
	m.notice, m.noticeIsError = "", false
	return m, nil
}

// revealInFileManager shows a file in the desktop's file manager, best effort
// (plan.md §6.8). A failure is reported rather than ignored: the user asked for
// something and nothing happened, which is worth saying.
func revealInFileManager(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// -R reveals the file inside its folder rather than opening the file.
		cmd = exec.Command("open", "-R", path)
	case "windows":
		cmd = exec.Command("explorer", "/select,"+path)
	default:
		dir := filepath.Dir(path)
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Not waited for: the file manager outlives the wizard.
	go func() { _ = cmd.Wait() }()
	return nil
}

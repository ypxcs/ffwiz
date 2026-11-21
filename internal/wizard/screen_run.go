package wizard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
)

// This file is the run screen (plan.md §6.7): progress, cancel, and the log pane.
//
// Nothing here blocks. Starting ffmpeg and reading its output both happen inside
// tea.Cmds, and every update arrives as a message, which is what keeps the UI
// responsive while a long encode runs (plan.md §3, rule 4).

// runStartedMsg reports that the process was launched, or why it was not.
type runStartedMsg struct {
	handle RunHandle
	err    error
}

// runEventMsg carries one event from a running ffmpeg.
type runEventMsg struct {
	ev ffmpeg.Event
	ch <-chan ffmpeg.Event
}

// tickMsg is the once-a-second redraw that keeps elapsed time moving even when
// ffmpeg reports nothing (plan.md §7).
type tickMsg time.Time

// startRun builds the command, starts ffmpeg and shows the run screen.
//
// It refuses to start a command that is still missing answers, and one whose
// output would replace a file the user has not agreed to replace - the output
// screen is the only place -y comes from, and this is the last gate before a
// process could do damage (plan.md §8).
func (m Model) startRun() (tea.Model, tea.Cmd) {
	if m.engine == nil || m.input == "" {
		return m, nil
	}

	preview, _, err := m.engine.Preview(m.answers, m.buildOptions())
	if err != nil {
		m.notice, m.noticeIsError = "cannot build the command: "+err.Error(), true
		return m, nil
	}
	if len(preview.Pending) > 0 {
		m.notice, m.noticeIsError = "some answers are missing: "+strings.Join(preview.Pending, ", "), true
		return m, nil
	}

	command, _, err := m.engine.Build(m.answers, m.buildOptions())
	if err != nil {
		m.notice, m.noticeIsError = "cannot build the command: "+err.Error(), true
		return m, nil
	}

	// The output screen checked this before the review screen existed; a file
	// that appeared since then would otherwise turn a "run" into a silent
	// refusal, because -n exits 0 without writing (DECISIONS D9) - and for a
	// numbered sequence it would not even refuse (DECISIONS D34).
	if !m.overwrite {
		if len(ffmpeg.OutputFiles(m.outputPath)) > 0 {
			m.notice, m.noticeIsError = m.existingNotice(), true
			m.screen = ScreenOutput
			m.conflict, m.conflictIdx = true, 0
			return m, nil
		}
	}

	if m.tools.FFmpeg == "" {
		m.notice, m.noticeIsError = "ffmpeg was not found, so nothing can be run", true
		return m, nil
	}

	m.screen = ScreenRun
	m.handle = nil
	m.starting = true
	m.progress = ffmpeg.Progress{}
	m.logs = nil
	m.elapsed = 0
	m.askingToStop = false
	m.startedAt = time.Now()
	m.notice, m.noticeIsError = "", false

	runner := m.runner
	path := m.tools.FFmpeg
	return m, tea.Batch(
		func() tea.Msg {
			handle, err := runner(context.Background(), path, command, ffmpeg.RunOptions{})
			return runStartedMsg{handle: handle, err: err}
		},
		m.startSpinner(),
		tickIn(time.Second),
	)
}

// tickIn schedules the elapsed-time redraw.
func tickIn(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// startSpinner kicks the spinner once. It reschedules itself from then on, so
// this must be sent a single time per run.
func (m Model) startSpinner() tea.Cmd {
	fps := m.spin.Spinner.FPS
	if fps <= 0 {
		fps = time.Second / 10
	}
	return tea.Tick(fps, func(time.Time) tea.Msg { return m.spin.Tick() })
}

// handleRunStarted records the process and starts reading its events.
func (m Model) handleRunStarted(msg runStartedMsg) (tea.Model, tea.Cmd) {
	m.starting = false
	if msg.err != nil {
		m.finishRun(ffmpeg.Result{
			Err:      fmt.Errorf("starting ffmpeg: %w", msg.err),
			ExitCode: -1,
		})
		return m, nil
	}
	if msg.handle == nil {
		// A Runner that reports success without a handle would otherwise panic
		// on Events(). The wizard's own Runner never does this.
		m.finishRun(ffmpeg.Result{
			Err:      errors.New("ffmpeg was started but nothing can watch it"),
			ExitCode: -1,
		})
		return m, nil
	}
	m.handle = msg.handle
	return m, waitForEvent(msg.handle.Events())
}

// waitForEvent reads one event. A closed channel without a Done event - which
// ffmpeg.Run never does, but a different implementation might - is reported as a
// failure rather than leaving the screen spinning for ever.
func waitForEvent(ch <-chan ffmpeg.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return runEventMsg{ev: ffmpeg.Event{
				Done: true,
				Result: ffmpeg.Result{
					Err:      errors.New("ffmpeg stopped reporting without saying how it ended"),
					ExitCode: -1,
				},
			}}
		}
		return runEventMsg{ev: ev, ch: ch}
	}
}

// handleRunEvent records an update and asks for the next one.
func (m Model) handleRunEvent(msg runEventMsg) (tea.Model, tea.Cmd) {
	// A run that has already been reported as finished must not be re-opened by
	// an event still in flight. The rest of the stream is still drained: the
	// process is writing its last lines and, with nobody reading, it would
	// eventually block on a full channel instead of exiting.
	if m.screen != ScreenRun {
		if msg.ch != nil {
			return m, drainEvents(msg.ch)
		}
		return m, nil
	}
	if msg.ev.Done {
		m.finishRun(msg.ev.Result)
		return m, nil
	}
	if p := msg.ev.Progress; p != nil {
		m.progress = *p
	} else if msg.ev.Log != "" {
		m.appendLog(msg.ev.Log)
	}
	if msg.ch == nil {
		return m, nil
	}
	return m, waitForEvent(msg.ch)
}

// drainEvents reads a stream to its end and throws it away, so a process that is
// still writing is never left blocked on an unread channel.
func drainEvents(ch <-chan ffmpeg.Event) tea.Cmd {
	return func() tea.Msg {
		for range ch {
		}
		return nil
	}
}

// appendLog keeps the log pane bounded (see maxLogLines).
func (m *Model) appendLog(line string) {
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
}

// handleTick advances the elapsed clock. Progress arrives only when ffmpeg has
// something new to say, so without this the display would freeze during a long
// frame and look like a hang (plan.md §7).
func (m Model) handleTick(msg tickMsg) (tea.Model, tea.Cmd) {
	if m.screen != ScreenRun {
		return m, nil
	}
	m.elapsed = time.Since(m.startedAt)
	return m, tickIn(time.Second)
}

// finishRun leaves the run screen and reports what happened.
func (m *Model) finishRun(result ffmpeg.Result) {
	m.elapsed = time.Since(m.startedAt)
	m.result = result
	m.progress = result.Progress
	m.handle = nil
	m.starting = false
	m.askingToStop = false
	m.removed = false
	if len(result.Stderr) > 0 && len(m.logs) == 0 {
		m.logs = append(m.logs, result.Stderr...)
	}
	// The exit status is not the verdict on its own: see ffmpeg.Diagnose and
	// DECISIONS D9.
	m.outcome, m.hintText = ffmpeg.Diagnose(result, m.outputPath)
	m.screen = ScreenDone
}

// updateRun drives the run screen.
func (m Model) updateRun(keyMsg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The stop confirmation takes over the keyboard while it is up, so a second
	// stray keypress cannot stop the encode (plan.md §6.7).
	if m.askingToStop {
		switch {
		case key.Matches(keyMsg, m.keys.Cancel):
			return m.cancelRun()
		default:
			m.askingToStop = false
			m.notice, m.noticeIsError = "", false
			return m, nil
		}
	}

	switch {
	case key.Matches(keyMsg, m.keys.Cancel):
		if m.starting {
			// The process is between "asked to start" and "started". Quitting
			// here could leave an ffmpeg behind that nothing is watching, and
			// the window is milliseconds wide, so the key is ignored.
			return m, nil
		}
		if m.handle == nil {
			// Nothing to stop: the process has already finished and its Done
			// event is in flight. Quitting now is safe.
			return m, tea.Quit
		}
		m.askingToStop = true
		m.notice, m.noticeIsError = "", false
		return m, nil
	case key.Matches(keyMsg, m.keys.Log):
		m.showLog = !m.showLog
		return m, nil
	}
	return m, nil
}

// cancelRun stops the process and moves straight to the done screen.
//
// The screens change here rather than waiting for the Done event, because the
// run is over as far as the user is concerned: the event that follows is the
// process actually dying, and handleRunEvent ignores anything that arrives once
// the screen has moved on. No notice is set: the done screen's own heading says
// what happened, and a notice would displace the hint that says what to do next.
func (m Model) cancelRun() (tea.Model, tea.Cmd) {
	m.askingToStop = false
	if m.handle != nil {
		m.handle.Cancel()
	}
	m.finishRun(ffmpeg.Result{
		Cancelled: true,
		ExitCode:  -1,
		Progress:  m.progress,
		Stderr:    m.logs,
	})
	return m, nil
}

// runBody renders progress, the numbers behind it, and the log pane.
func (m Model) runBody(width int) string {
	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Encoding") + "\n\n")

	if m.starting {
		b.WriteString("  " + m.spin.View() + " " + m.styles.Muted.Render("starting ffmpeg...") + "\n")
		return b.String()
	}

	if m.askingToStop {
		b.WriteString("  " + m.styles.Warning.Render("Stop the encode? press q again to stop, any other key to carry on") + "\n\n")
	}

	total := m.expectedDuration()
	if total > 0 {
		pct := m.progress.Percent(total)
		b.WriteString("  " + m.bar.ViewAs(pct/100) + " " +
			m.styles.Key.Render(fmt.Sprintf("%3.0f%%", pct)) + "\n")
	} else {
		// Without a known length there is no honest percentage to show, so the
		// screen shows that something is happening instead of a bar that would
		// have to guess (plan.md §7).
		b.WriteString("  " + m.spin.View() + " " +
			m.styles.Muted.Render("length unknown, showing elapsed time") + "\n")
	}
	b.WriteString("\n")

	if facts := m.runFacts(total); facts != "" {
		b.WriteString("  " + facts + "\n")
	}

	if m.showLog {
		b.WriteString("\n" + m.logPane(width) + "\n")
	} else if len(m.logs) > 0 {
		b.WriteString("\n" + m.styles.Muted.Render("  press l to see what ffmpeg is saying") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// runFacts is the line of numbers under the bar: elapsed, remaining, speed, bit
// rate and bytes written. Every part is omitted when it is not known, because a
// row of zeroes reads as a broken screen rather than as a missing measurement.
func (m Model) runFacts(total time.Duration) string {
	parts := []string{"elapsed " + format.Clock(m.elapsed)}
	if eta := m.progress.ETA(total); eta > 0 {
		parts = append(parts, "about "+format.Clock(eta)+" left")
	}
	if m.progress.Speed > 0 {
		parts = append(parts, fmt.Sprintf("%.2gx", m.progress.Speed))
	}
	if m.progress.Bitrate > 0 {
		parts = append(parts, format.BitRate(int64(m.progress.Bitrate)))
	}
	if m.progress.TotalSize > 0 {
		parts = append(parts, format.Bytes(m.progress.TotalSize)+" written")
	}
	return strings.Join(parts, "  •  ")
}

// logPane shows the tail of ffmpeg's log.
func (m Model) logPane(width int) string {
	if len(m.logs) == 0 {
		return m.styles.Muted.Render("  ffmpeg has said nothing yet")
	}
	// The last lines are the ones that matter, and the pane has a fixed number
	// of rows to give them.
	rows := clamp(bodyHeightFor(m.height, m.help.ShowAll)-6, 2, 8)
	lines := m.logs
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("  " + m.styles.Muted.Render(truncate(line, max(width-2, 8))))
	}
	return b.String()
}

// hintRun is the one thing worth saying while an encode runs.
func (m Model) hintRun() string {
	if m.askingToStop {
		return "stopping leaves a partially written file, which you can delete from the next screen"
	}
	return "q or ctrl+c stops the encode; l shows what ffmpeg is saying"
}

// expectedDuration is how long the finished output will be, for the progress
// bar. It is the *output* duration, which a trim or a speed change shortens
// (DECISIONS D3).
//
// It re-derives the command rather than caching the length at start, so that the
// bar cannot show a percentage against a length that no longer matches the
// command. Resolving a task is a handful of steps, and this runs once per frame.
func (m Model) expectedDuration() time.Duration {
	command, _, err := m.liveCommand()
	if err != nil || command == nil {
		return 0
	}
	return command.ExpectedDuration()
}

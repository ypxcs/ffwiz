package wizard

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// startTask resets the wizard onto a task and shows its first question.
func (m *Model) startTask(id string) {
	task, ok := m.set.Get(id)
	if !ok {
		m.notice, m.noticeIsError = "unknown task "+id, true
		return
	}
	m.engine = &tasks.Engine{Task: task, Info: m.info, Caps: m.caps, TempDir: m.tempDir}
	// A fresh map, not a cleared one: answers from the previous task were keyed
	// by that task's step ids and mean nothing here.
	m.answers = map[string]string{}
	m.stepIndex = 0
	m.choiceIdx = 0
	m.outputPath = ""
	m.copied = false
	// An edited command belongs to the task that produced it (DECISIONS D44).
	m.edited = nil
	m.editErr = ""
	// The clip browser belongs to the task, so a new task starts with an empty
	// list even if the previous one also joined clips.
	m.concatPicked = nil
	m.concatDir = ""
	m.concatShowAll = false
	m.clearConcatFilter()
	m.presetIndex = 0
	m.notice, m.noticeIsError = "", false
	m.screen = ScreenStep
	m.syncStep()
	// A task with presets is configured by picking one first: plan.md §5 calls
	// them "task answers prefilled into a task", so the choice of preset decides
	// which answers to start from before any question is asked (DECISIONS D45).
	if len(task.Presets) > 0 {
		m.screen = ScreenPreset
	}
}

// resolution re-resolves the task against the answers so far. It is cheap (a
// task has a handful of steps) and is what makes the wizard stateless about
// which steps are active: nothing is remembered that could go stale.
func (m Model) resolution() tasks.Resolution {
	if m.engine == nil {
		return tasks.Resolution{}
	}
	return m.engine.Resolve(m.answers)
}

// activeSteps are the steps the current answers leave switched on.
func (m Model) activeSteps() []tasks.Step {
	if m.engine == nil {
		return nil
	}
	return m.engine.Resolve(m.answers).Active
}

// currentStep is the step being asked about.
func (m Model) currentStep() (tasks.Step, bool) {
	steps := m.activeSteps()
	if m.stepIndex < 0 || m.stepIndex >= len(steps) {
		return tasks.Step{}, false
	}
	return steps[m.stepIndex], true
}

// customRowIndex is the row number of a resolution step's "Custom..." entry,
// which sits after the presets, or -1 when the step offers none.
func customRowIndex(step tasks.Step, shown []tasks.Choice) int {
	if step.Type != tasks.StepResolution || !step.Custom {
		return -1
	}
	return len(shown)
}

// syncStep points the widgets at the current step, restoring whatever answer it
// already had, so stepping back and forward never loses a choice (plan.md §6.4).
func (m *Model) syncStep() {
	steps := m.activeSteps()
	if len(steps) == 0 {
		return
	}
	if m.stepIndex >= len(steps) {
		m.stepIndex = len(steps) - 1
	}
	if m.stepIndex < 0 {
		m.stepIndex = 0
	}

	step := steps[m.stepIndex]
	res := m.resolution()
	current := res.Answers[step.ID]

	// Only one field is ever live; blur the rest so a stray keystroke cannot
	// land somewhere invisible.
	m.stepInput.Blur()
	m.rangeStart.Blur()
	m.rangeEnd.Blur()
	m.customInput.Blur()

	switch step.Type {
	case tasks.StepChoice:
		shown := res.Shown[step.ID]
		m.choiceIdx = indexOfChoice(shown, current)
		if m.choiceIdx < 0 {
			m.choiceIdx = 0
		}

	case tasks.StepMulti:
		shown := res.Shown[step.ID]
		// Rebuild the ticks from the resolved answer: a multi answer is a
		// comma-separated list of the values that are ticked.
		m.toggles = map[string]bool{}
		for _, c := range shown {
			for _, v := range strings.Split(current, ",") {
				if v != "" && c.Value == v {
					m.toggles[c.Value] = true
				}
			}
		}
		m.choiceIdx = 0

	case tasks.StepConfirm:
		m.confirmYes = res.Yes(step.ID)

	case tasks.StepResolution:
		shown := res.Shown[step.ID]
		m.choiceIdx = indexOfChoice(shown, current)
		if m.choiceIdx < 0 {
			// Not a preset: either a typed value (put it back in the field) or
			// nothing chosen yet (start on the first preset).
			if row := customRowIndex(step, shown); row >= 0 && current != "" {
				m.choiceIdx = row
				m.customInput.SetValue(current)
				m.customInput.SetCursor(utf8.RuneCountInString(current))
			} else {
				m.choiceIdx = 0
			}
		}
		if m.choiceIdx == customRowIndex(step, shown) {
			m.customInput.Focus()
		}

	case tasks.StepTimeRange:
		start, end := "", ""
		if r, err := tasks.ParseRange(current); err == nil {
			start, end = tasks.FormatTimecode(r.Start), tasks.FormatTimecode(r.End)
		}
		m.rangeStart.SetValue(start)
		m.rangeStart.SetCursor(utf8.RuneCountInString(start))
		m.rangeEnd.SetValue(end)
		m.rangeEnd.SetCursor(utf8.RuneCountInString(end))
		m.rangeField = 0
		m.rangeStart.Focus()

	case tasks.StepConcat:
		m.syncConcat(step)

	default:
		m.stepInput.Placeholder = step.Placeholder
		m.stepInput.SetValue(current)
		m.stepInput.SetCursor(utf8.RuneCountInString(current))
		m.stepInput.Focus()
	}
}

// indexOfChoice returns the row a value sits on, or -1.
func indexOfChoice(choices []tasks.Choice, value string) int {
	if value == "" {
		return -1
	}
	for i, c := range choices {
		if c.Value == value {
			return i
		}
	}
	return -1
}

// stepBody renders the current question.
func (m Model) stepBody(width int) string {
	step, ok := m.currentStep()
	if !ok {
		return m.styles.Muted.Render("this task has no questions")
	}
	steps := m.activeSteps()

	var b strings.Builder
	b.WriteString(m.styles.Muted.Render(fmt.Sprintf("%d/%d", m.stepIndex+1, len(steps))))
	b.WriteString("  ")
	b.WriteString(m.styles.Heading.Render(step.Prompt))
	b.WriteString("\n\n")

	res := m.resolution()
	switch step.Type {
	case tasks.StepChoice:
		shown := res.Shown[step.ID]
		if len(shown) == 0 {
			b.WriteString(m.styles.Error.Render("no option is available for this question") + "\n")
		}
		current := res.Answers[step.ID]
		for i, c := range shown {
			b.WriteString(m.choiceLine(c, current, i == m.choiceIdx, width) + "\n")
		}
		b.WriteString(m.hiddenNoteBlock(step))

	case tasks.StepMulti:
		shown := res.Shown[step.ID]
		if len(shown) == 0 {
			b.WriteString(m.styles.Error.Render("no option is available for this question") + "\n")
		}
		for i, c := range shown {
			b.WriteString(m.checkLine(c, m.toggles[c.Value], i == m.choiceIdx, width) + "\n")
		}
		b.WriteString(m.hiddenNoteBlock(step))

	case tasks.StepConfirm:
		b.WriteString(m.confirmLine("Yes", m.confirmYes, false, width) + "\n")
		b.WriteString(m.confirmLine("No", !m.confirmYes, true, width) + "\n")

	case tasks.StepResolution:
		shown := res.Shown[step.ID]
		current := res.Answers[step.ID]
		// A typed value matches no preset, so no preset is ticked.
		if indexOfChoice(shown, current) < 0 {
			current = ""
		}
		for i, c := range shown {
			b.WriteString(m.choiceLine(c, current, i == m.choiceIdx, width) + "\n")
		}
		if row := customRowIndex(step, shown); row >= 0 {
			cursor := "  "
			onRow := m.choiceIdx == row
			if onRow {
				cursor = m.styles.Key.Render("> ")
			}
			label := "Custom..."
			if onRow {
				label = lipgloss.NewStyle().Bold(true).Render(label)
			} else {
				label = m.styles.Muted.Render(label)
			}
			b.WriteString(cursor + label + "\n")
			if onRow {
				b.WriteString("  " + m.customInput.View() + "\n")
			}
		}
		b.WriteString(m.hiddenNoteBlock(step))

	case tasks.StepTimeRange:
		b.WriteString(m.rangeFieldLine(stepStartLabel(step), m.rangeStart.View(), m.rangeField == 0) + "\n")
		b.WriteString(m.rangeFieldLine(stepEndLabel(step), m.rangeEnd.View(), m.rangeField == 1) + "\n")
		if len := m.inputLength(); len != "" {
			b.WriteString("\n" + m.styles.Muted.Render("the input is "+len+" long") + "\n")
		}

	case tasks.StepConcat:
		b.WriteString(m.concatBody(width))

	default:
		b.WriteString("  " + m.stepInput.View() + "\n")
		if step.Unit != "" {
			b.WriteString("  " + m.styles.Muted.Render("in "+step.Unit) + "\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// hiddenNoteBlock renders the "not offered" explanation as its own paragraph.
func (m Model) hiddenNoteBlock(step tasks.Step) string {
	if note := m.hiddenNote(step); note != "" {
		return "\n" + m.styles.Muted.Render("not offered: "+note) + "\n"
	}
	return ""
}

// choiceLine renders one option. The cursor marks where the user is; the tick
// marks the answer that would be used.
func (m Model) choiceLine(c tasks.Choice, current string, highlighted bool, width int) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
	}
	// Leave room for the cursor and for the "(default)" marker, which is
	// appended after the label, so a long label cannot push it off the edge.
	label := truncate(c.Label, max(width-15, 8))
	switch {
	case c.Value == current && current != "":
		label = m.styles.ChoiceCurrent.Render("✓ " + label)
	case highlighted:
		label = lipgloss.NewStyle().Bold(true).Render(label)
	default:
		label = m.styles.Muted.Render(label)
	}
	if c.Default {
		label += m.styles.Muted.Render("  (default)")
	}
	return cursor + label
}

// checkLine renders one checkbox of a multi step.
func (m Model) checkLine(c tasks.Choice, ticked, highlighted bool, width int) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
	}
	box := "[ ]"
	if ticked {
		box = "[x]"
	}
	label := truncate(c.Label, max(width-16, 8))
	switch {
	case ticked:
		label = m.styles.ChoiceCurrent.Render(label)
	case highlighted:
		label = lipgloss.NewStyle().Bold(true).Render(label)
	default:
		label = m.styles.Muted.Render(label)
	}
	if c.Default {
		label += m.styles.Muted.Render("  (default)")
	}
	return cursor + m.styles.Key.Render(box) + " " + label
}

// confirmLine renders one half of a yes/no toggle.
func (m Model) confirmLine(label string, selected, highlighted bool, width int) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
	}
	mark := "( )"
	switch {
	case selected:
		mark, label = "(•)", m.styles.ChoiceCurrent.Render(label)
	case highlighted:
		label = lipgloss.NewStyle().Bold(true).Render(label)
	default:
		label = m.styles.Muted.Render(label)
	}
	return cursor + m.styles.Key.Render(mark) + " " + truncate(label, max(width-14, 8))
}

// rangeFieldLine renders one time_range field with a label and a cursor.
func (m Model) rangeFieldLine(label, value string, focused bool) string {
	cursor := "  "
	if focused {
		cursor = m.styles.Key.Render("> ")
	}
	name := m.styles.Label.Render(fmt.Sprintf("%-6s", label+":"))
	return cursor + name + " " + value
}

// stepStartLabel and stepEndLabel name a time_range step's two fields, falling
// back to From/To when the YAML does not say.
func stepStartLabel(step tasks.Step) string {
	if step.MinLabel != "" {
		return step.MinLabel
	}
	return "From"
}

func stepEndLabel(step tasks.Step) string {
	if step.MaxLabel != "" {
		return step.MaxLabel
	}
	return "To"
}

// inputLength renders the input's duration for the time_range hint, or "" when
// it is unknown.
func (m Model) inputLength() string {
	if m.info == nil {
		return ""
	}
	d := m.info.Duration()
	if d <= 0 {
		return ""
	}
	return tasks.FormatTimecode(d)
}

// hiddenNote explains the options a step is not offering: either the local
// ffmpeg build cannot produce them or a previous answer ruled them out
// (plan.md §7, §11).
func (m Model) hiddenNote(step tasks.Step) string {
	if m.engine == nil {
		return ""
	}
	hidden := m.resolution().HiddenChoices[step.ID]
	if len(hidden) == 0 {
		return ""
	}
	parts := make([]string, 0, len(hidden))
	for _, c := range hidden {
		reason := m.engine.Unavailable(c)
		if reason == "" && c.When != nil {
			reason = c.When.Explain()
		}
		if reason == "" {
			reason = "not applicable here"
		}
		parts = append(parts, c.Label+" ("+reason+")")
	}
	return strings.Join(parts, "; ")
}

// hintStep is the highlighted option's own explanation, falling back to the
// step's hint. This is the "why" line of plan.md §6.
func (m Model) hintStep() string {
	step, ok := m.currentStep()
	if !ok {
		return ""
	}
	// A calculated answer shows its arithmetic: plan.md §5.2 asks for the math
	// behind a target size to be visible while choosing it.
	if step.Compute != nil {
		if note := m.resolution().Notes[step.ID]; note != "" {
			return note
		}
	}
	switch step.Type {
	case tasks.StepConcat:
		return m.hintConcat()
	case tasks.StepChoice, tasks.StepMulti:
		shown := m.resolution().Shown[step.ID]
		if m.choiceIdx >= 0 && m.choiceIdx < len(shown) && shown[m.choiceIdx].Hint != "" {
			return shown[m.choiceIdx].Hint
		}
	case tasks.StepResolution:
		shown := m.resolution().Shown[step.ID]
		if m.choiceIdx >= 0 && m.choiceIdx < len(shown) && shown[m.choiceIdx].Hint != "" {
			return shown[m.choiceIdx].Hint
		}
		if m.choiceIdx == customRowIndex(step, shown) {
			if step.Hint != "" {
				return step.Hint
			}
			return step.Prompt + ": type a value and press enter"
		}
	case tasks.StepConfirm:
		if m.confirmYes {
			return "This will be added to the command."
		}
		return "This is left out of the command."
	}
	return step.Hint
}

// updateStep drives the generic step renderer.
func (m Model) updateStep(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	step, ok := m.currentStep()
	if !ok {
		return m.enterOutput()
	}

	switch step.Type {
	case tasks.StepChoice:
		if !isKey {
			return m, nil
		}
		shown := m.resolution().Shown[step.ID]
		switch {
		case key.Matches(keyMsg, m.keys.Back):
			return m.stepBack()
		case key.Matches(keyMsg, m.keys.Up):
			m.choiceIdx = wrap(m.choiceIdx-1, len(shown))
		case key.Matches(keyMsg, m.keys.Down):
			m.choiceIdx = wrap(m.choiceIdx+1, len(shown))
		case key.Matches(keyMsg, m.keys.Select):
			return m.stepAdvance()
		}
		return m, nil

	case tasks.StepMulti:
		if !isKey {
			return m, nil
		}
		shown := m.resolution().Shown[step.ID]
		switch {
		case key.Matches(keyMsg, m.keys.Back):
			return m.stepBack()
		case key.Matches(keyMsg, m.keys.Up):
			m.choiceIdx = wrap(m.choiceIdx-1, len(shown))
		case key.Matches(keyMsg, m.keys.Down):
			m.choiceIdx = wrap(m.choiceIdx+1, len(shown))
		case key.Matches(keyMsg, m.keys.Toggle):
			if m.choiceIdx >= 0 && m.choiceIdx < len(shown) {
				v := shown[m.choiceIdx].Value
				m.toggles[v] = !m.toggles[v]
			}
		case key.Matches(keyMsg, m.keys.Select):
			return m.stepAdvance()
		}
		return m, nil

	case tasks.StepConfirm:
		if !isKey {
			return m, nil
		}
		switch {
		case key.Matches(keyMsg, m.keys.Back):
			return m.stepBack()
		case key.Matches(keyMsg, m.keys.Up), key.Matches(keyMsg, m.keys.Down), key.Matches(keyMsg, m.keys.Toggle):
			m.confirmYes = !m.confirmYes
		case key.Matches(keyMsg, m.keys.Select):
			return m.stepAdvance()
		}
		return m, nil

	case tasks.StepResolution:
		shown := m.resolution().Shown[step.ID]
		rows := len(shown)
		if customRowIndex(step, shown) >= 0 {
			rows++
		}
		onCustom := customRowIndex(step, shown) >= 0 && m.choiceIdx == customRowIndex(step, shown)
		if isKey {
			switch {
			case key.Matches(keyMsg, m.keys.Back):
				return m.stepBack()
			case key.Matches(keyMsg, m.keys.Up):
				m.choiceIdx = wrap(m.choiceIdx-1, rows)
				m.focusResolutionRow(step, shown)
			case key.Matches(keyMsg, m.keys.Down):
				m.choiceIdx = wrap(m.choiceIdx+1, rows)
				m.focusResolutionRow(step, shown)
			case key.Matches(keyMsg, m.keys.Select):
				return m.stepAdvance()
			}
		}
		if onCustom {
			// Everything else is typing. j/k move the cursor rather than typing
			// letters, but a custom resolution is a number, so that costs
			// nothing.
			var cmd tea.Cmd
			m.customInput, cmd = m.customInput.Update(msg)
			return m, cmd
		}
		return m, nil

	case tasks.StepTimeRange:
		if isKey {
			switch {
			case key.Matches(keyMsg, m.keys.BackEsc):
				return m.stepBack()
			case key.Matches(keyMsg, m.keys.Tab):
				m.switchRangeField()
				return m, nil
			case key.Matches(keyMsg, m.keys.Select):
				return m.rangeAdvance(step)
			}
		}
		var cmd tea.Cmd
		if m.rangeField == 0 {
			m.rangeStart, cmd = m.rangeStart.Update(msg)
		} else {
			m.rangeEnd, cmd = m.rangeEnd.Update(msg)
		}
		return m, cmd

	case tasks.StepConcat:
		return m.updateConcatStep(msg, keyMsg, isKey)

	default:
		// A single text field: only esc leaves, because `e` and `q` are letters
		// here (DECISIONS D14).
		if isKey {
			switch {
			case key.Matches(keyMsg, m.keys.BackEsc):
				return m.stepBack()
			case key.Matches(keyMsg, m.keys.Select):
				return m.stepAdvance()
			}
		}
		var cmd tea.Cmd
		m.stepInput, cmd = m.stepInput.Update(msg)
		return m, cmd
	}
}

// focusResolutionRow moves the text focus onto the "Custom..." row when the
// cursor is there, and off it otherwise.
func (m *Model) focusResolutionRow(step tasks.Step, shown []tasks.Choice) {
	if row := customRowIndex(step, shown); row >= 0 && m.choiceIdx == row {
		m.customInput.Focus()
		return
	}
	m.customInput.Blur()
}

// switchRangeField moves the cursor between the two time_range fields.
func (m *Model) switchRangeField() {
	if m.rangeField == 0 {
		m.rangeField = 1
		m.rangeStart.Blur()
		m.rangeEnd.Focus()
	} else {
		m.rangeField = 0
		m.rangeEnd.Blur()
		m.rangeStart.Focus()
	}
}

// rangeAdvance walks a time_range step: the first enter moves from the start
// field to the end field, the second records the range.
func (m Model) rangeAdvance(step tasks.Step) (tea.Model, tea.Cmd) {
	if m.rangeField == 0 {
		if _, err := tasks.ParseTime(m.rangeStart.Value()); err != nil {
			m.notice, m.noticeIsError = "start: "+err.Error(), true
			return m, nil
		}
		m.switchRangeField()
		m.notice, m.noticeIsError = "", false
		return m, nil
	}

	answer := strings.TrimSpace(m.rangeStart.Value()) + "," + strings.TrimSpace(m.rangeEnd.Value())
	var input time.Duration
	if m.info != nil {
		input = m.info.Duration()
	}
	if err := step.CheckRangeAnswer(answer, input); err != nil {
		m.notice, m.noticeIsError = err.Error(), true
		return m, nil
	}
	r, err := tasks.ParseRange(answer)
	if err != nil {
		m.notice, m.noticeIsError = err.Error(), true
		return m, nil
	}
	// Store one canonical spelling so re-entering the step shows the same text.
	canonical := tasks.FormatTimecode(r.Start) + "," + tasks.FormatTimecode(r.End)
	return m.recordAndAdvance(step.ID, canonical)
}

// lastFrameTime is the latest point on the input at which a frame can still be
// read, or 0 when the input's length is unknown. A timestamp step is checked
// against it so a thumbnail "at the end" is refused rather than silently
// producing no file (DECISIONS D36).
func (m Model) lastFrameTime() time.Duration {
	if m.info == nil {
		return 0
	}
	return m.info.LastFrameTime()
}

// stepAdvance records the answer and moves to the next active step, which may be
// a different set of steps than before the answer was recorded.
func (m Model) stepAdvance() (tea.Model, tea.Cmd) {
	step, ok := m.currentStep()
	if !ok {
		return m.enterOutput()
	}
	res := m.resolution()

	switch step.Type {
	case tasks.StepChoice:
		shown := res.Shown[step.ID]
		if len(shown) == 0 {
			m.notice, m.noticeIsError = "no option is available for this question", true
			return m, nil
		}
		m.choiceIdx = clamp(m.choiceIdx, 0, len(shown)-1)
		return m.recordAndAdvance(step.ID, shown[m.choiceIdx].Value)

	case tasks.StepMulti:
		shown := res.Shown[step.ID]
		picked := make([]string, 0, len(shown))
		for _, c := range shown {
			if m.toggles[c.Value] {
				picked = append(picked, c.Value)
			}
		}
		return m.recordAndAdvance(step.ID, strings.Join(picked, ","))

	case tasks.StepConfirm:
		if m.confirmYes {
			return m.recordAndAdvance(step.ID, "yes")
		}
		return m.recordAndAdvance(step.ID, "no")

	case tasks.StepResolution:
		shown := res.Shown[step.ID]
		if row := customRowIndex(step, shown); row >= 0 && m.choiceIdx == row {
			value := strings.TrimSpace(m.customInput.Value())
			if err := step.CheckAnswer(value); err != nil {
				m.notice, m.noticeIsError = err.Error(), true
				return m, nil
			}
			return m.recordAndAdvance(step.ID, value)
		}
		if len(shown) == 0 {
			m.notice, m.noticeIsError = "no option is available for this question", true
			return m, nil
		}
		m.choiceIdx = clamp(m.choiceIdx, 0, len(shown)-1)
		return m.recordAndAdvance(step.ID, shown[m.choiceIdx].Value)

	case tasks.StepTimeRange:
		return m.rangeAdvance(step)

	case tasks.StepConcat:
		return m.concatAdvance(step)

	default:
		value := strings.TrimSpace(m.stepInput.Value())
		if err := step.CheckAnswer(value); err != nil {
			m.notice, m.noticeIsError = err.Error(), true
			return m, nil
		}
		if err := step.CheckTimeAnswer(value, m.lastFrameTime()); err != nil {
			m.notice, m.noticeIsError = err.Error(), true
			return m, nil
		}
		return m.recordAndAdvance(step.ID, value)
	}
}

// recordAndAdvance stores an answer and moves on. It is the single place that
// advances the cursor, so every step type lands on the same "what comes next"
// logic.
func (m Model) recordAndAdvance(stepID, value string) (tea.Model, tea.Cmd) {
	m.answers[stepID] = value
	// Changing an answer takes the command back: it was built from the answers,
	// and an edited command that ignored a later change would be a trap
	// (DECISIONS D44).
	m.edited = nil
	m.notice, m.noticeIsError = "", false
	m.stepIndex++

	// Recompute before checking the end: the answer may have switched later
	// steps on or off.
	if m.stepIndex >= len(m.activeSteps()) {
		return m.enterOutput()
	}
	m.syncStep()
	return m, nil
}

// stepBack returns to the previous question, or to the task list from the first
// one. The answers already given are kept, which is what makes going back
// useful rather than merely possible (plan.md §6.4).
func (m Model) stepBack() (tea.Model, tea.Cmd) {
	m.notice, m.noticeIsError = "", false
	if m.stepIndex > 0 {
		m.stepIndex--
		m.syncStep()
		return m, nil
	}
	m.screen = ScreenTask
	return m, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

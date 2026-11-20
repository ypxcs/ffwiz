package wizard

import (
	"fmt"
	"strings"
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
	m.engine = &tasks.Engine{Task: task, Info: m.info, Caps: m.caps}
	// A fresh map, not a cleared one: answers from the previous task were keyed
	// by that task's step ids and mean nothing here.
	m.answers = map[string]string{}
	m.stepIndex = 0
	m.choiceIdx = 0
	m.outputPath = ""
	m.copied = false
	m.notice, m.noticeIsError = "", false
	m.screen = ScreenStep
	m.syncStep()
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

	if step.Type == tasks.StepChoice {
		shown := res.Shown[step.ID]
		m.choiceIdx = 0
		if current := res.Answers[step.ID]; current != "" {
			for i, c := range shown {
				if c.Value == current {
					m.choiceIdx = i
					break
				}
			}
		}
		return
	}

	m.stepInput.Placeholder = step.Placeholder
	m.stepInput.SetValue(res.Answers[step.ID])
	m.stepInput.SetCursor(utf8.RuneCountInString(res.Answers[step.ID]))
	m.stepInput.Focus()
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
		if note := m.hiddenNote(step); note != "" {
			b.WriteString("\n" + m.styles.Muted.Render("not offered: "+note) + "\n")
		}

	case tasks.StepInput:
		b.WriteString("  " + m.stepInput.View() + "\n")
		if step.Unit != "" {
			b.WriteString("  " + m.styles.Muted.Render("in "+step.Unit) + "\n")
		}

	default:
		// The remaining step types (multi, time_range, resolution, confirm) are
		// M5 work (plan.md §10). Saying so is better than rendering something
		// that looks like a control but is not one.
		b.WriteString(m.styles.Warning.Render(
			fmt.Sprintf("this question type (%s) is not available yet; it is left at its default", step.Type)) + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
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
	if step.Type == tasks.StepChoice {
		shown := m.resolution().Shown[step.ID]
		if m.choiceIdx >= 0 && m.choiceIdx < len(shown) && shown[m.choiceIdx].Hint != "" {
			return shown[m.choiceIdx].Hint
		}
	}
	return step.Hint
}

// updateStep drives the generic step renderer.
func (m Model) updateStep(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	step, ok := m.currentStep()
	if !ok {
		return m.enterOutput()
	}

	if step.Type == tasks.StepChoice {
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
	}

	// Everything else is a text field: only esc leaves, because `e` and `q` are
	// letters here (DECISIONS D14).
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

// stepAdvance records the answer and moves to the next active step, which may be
// a different set of steps than before the answer was recorded.
func (m Model) stepAdvance() (tea.Model, tea.Cmd) {
	step, ok := m.currentStep()
	if !ok {
		return m.enterOutput()
	}

	switch step.Type {
	case tasks.StepChoice:
		shown := m.resolution().Shown[step.ID]
		if len(shown) == 0 {
			m.notice, m.noticeIsError = "no option is available for this question", true
			return m, nil
		}
		m.choiceIdx = clamp(m.choiceIdx, 0, len(shown)-1)
		m.answers[step.ID] = shown[m.choiceIdx].Value

	case tasks.StepInput:
		value := strings.TrimSpace(m.stepInput.Value())
		if err := step.CheckAnswer(value); err != nil {
			m.notice, m.noticeIsError = err.Error(), true
			return m, nil
		}
		m.answers[step.ID] = value

	default:
		// Unsupported type: keep whatever the engine resolved and move on, so
		// the flow still reaches a command.
	}

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

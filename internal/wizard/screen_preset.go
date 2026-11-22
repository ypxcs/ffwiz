package wizard

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// This file is the preset screen (plan.md §5, "Presets"): the named answer sets
// a task offers, e.g. "Discord under 25 MB", plus the row that starts the task
// with none of them.
//
// A preset is only a set of answers - it is not a second kind of task and it does
// not skip the engine. Choosing one writes its answers into m.answers, which is
// exactly what typing them in would do, and everything downstream (conditions,
// capability gating, the command preview, the run) is then the ordinary path
// (DECISIONS D45).

// noPresetLabel is the last row of the preset screen: the way to answer the
// task's questions without starting from somebody else's answers.
const noPresetLabel = "No preset: answer each question myself"

// taskPresets are the presets of the task being configured, or nil.
func (m Model) taskPresets() []tasks.Preset {
	if m.engine == nil {
		return nil
	}
	return m.engine.Task.Presets
}

// presetRowCount is how many rows the preset screen offers: the task's presets,
// then the "no preset" row.
func (m Model) presetRowCount() int {
	return len(m.taskPresets()) + 1
}

// highlightedPreset is the preset under the cursor. The second result is false
// when the cursor is on the "no preset" row.
func (m Model) highlightedPreset() (tasks.Preset, bool) {
	presets := m.taskPresets()
	if m.presetIndex < 0 || m.presetIndex >= len(presets) {
		return tasks.Preset{}, false
	}
	return presets[m.presetIndex], true
}

// presetBody lists the presets the task offers.
func (m Model) presetBody(width int) string {
	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("Start from a preset") + "\n")
	if m.engine != nil {
		b.WriteString(m.styles.Muted.Render(truncate(m.engine.Task.Label, width)) + "\n")
	}
	b.WriteString("\n")

	for i, p := range m.taskPresets() {
		b.WriteString(m.presetLine(p.Label, i == m.presetIndex) + "\n")
	}
	b.WriteString(m.presetLine(noPresetLabel, m.presetIndex == len(m.taskPresets())) + "\n")

	return strings.TrimRight(b.String(), "\n")
}

// presetLine renders one row, following the task list's convention: the cursor
// is a marker in the key colour, the highlighted label is bold and the rest are
// muted.
func (m Model) presetLine(label string, highlighted bool) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
		label = lipgloss.NewStyle().Bold(true).Render(label)
	} else {
		label = m.styles.Muted.Render(label)
	}
	return cursor + label
}

// hintPreset explains the highlighted preset.
//
// The description comes first because it is the only thing here that says *why*
// somebody would pick this preset; the list of answers is the fallback, and the
// command pane below shows the result of them either way.
func (m Model) hintPreset() string {
	preset, ok := m.highlightedPreset()
	if !ok {
		return "the wizard's own recommended answers will be used; you can change any of them"
	}
	if preset.Description != "" {
		return preset.Description
	}
	if summary := m.presetSummary(preset); summary != "" {
		return "fills in " + summary
	}
	return preset.Label
}

// presetSummary names the answers a preset sets, in the task's step order, using
// the labels the option lists show so the hint matches what the user will see.
func (m Model) presetSummary(preset tasks.Preset) string {
	if m.engine == nil {
		return ""
	}
	parts := make([]string, 0, len(preset.Answers))
	for _, step := range m.engine.Task.Steps {
		answer, ok := preset.Answers[step.ID]
		if !ok {
			continue
		}
		parts = append(parts, presetAnswerLabel(step, answer))
	}
	return strings.Join(parts, ", ")
}

// presetAnswerLabel is the human-readable form of one prefilled answer.
func presetAnswerLabel(step tasks.Step, answer string) string {
	switch step.Type {
	case tasks.StepChoice, tasks.StepMulti, tasks.StepResolution:
		values := strings.Split(answer, ",")
		labels := make([]string, 0, len(values))
		for _, v := range values {
			label := v
			for _, c := range step.Choices {
				if c.Value == v {
					label = c.Label
					break
				}
			}
			labels = append(labels, label)
		}
		return strings.Join(labels, " + ")
	case tasks.StepConfirm:
		if strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return step.Prompt
		}
		return "not " + step.Prompt
	default:
		return answer
	}
}

// updatePreset moves the cursor and applies the chosen row.
func (m Model) updatePreset(keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if !isKey {
		return m, nil
	}
	switch {
	case key.Matches(keyMsg, m.keys.Up):
		m.presetIndex = wrap(m.presetIndex-1, m.presetRowCount())
	case key.Matches(keyMsg, m.keys.Down):
		m.presetIndex = wrap(m.presetIndex+1, m.presetRowCount())
	case key.Matches(keyMsg, m.keys.Select):
		return m.choosePreset()
	case key.Matches(keyMsg, m.keys.Back):
		m.notice, m.noticeIsError = "", false
		m.screen = ScreenTask
		return m, nil
	}
	return m, nil
}

// choosePreset applies the highlighted row and moves on to the questions.
func (m Model) choosePreset() (tea.Model, tea.Cmd) {
	preset, ok := m.highlightedPreset()
	if !ok {
		return m.withoutPreset()
	}
	return m.applyPreset(preset)
}

// withoutPreset starts the task's questions from the beginning, with nothing
// filled in. It is the same screen the task would have had if it offered no
// presets at all.
func (m Model) withoutPreset() (tea.Model, tea.Cmd) {
	m.stepIndex = 0
	m.screen = ScreenStep
	m.notice, m.noticeIsError = "", false
	m.syncStep()
	return m, nil
}

// applyPreset writes a preset's answers into the task and moves to whatever the
// preset did not settle.
//
// It lands on the first *active* step the preset has no answer for, rather than
// on the first question of the task. The point of a preset is that the questions
// it covers are already answered: stopping on each of them so the user can press
// enter would be the preset doing nothing. The questions it does not cover are
// still asked, which is what makes "the user can accept or tweak any step" true
// without making the preset pointless (plan.md §5, DECISIONS D45).
func (m Model) applyPreset(preset tasks.Preset) (tea.Model, tea.Cmd) {
	for id, value := range preset.Answers {
		m.answers[id] = value
	}
	// A hand-edited command was built from the answers as they were, and this
	// change is a later one (DECISIONS D44).
	m.edited = nil

	next := m.firstStepOutside(preset)
	if next < 0 {
		// The preset answered everything the task asks, so the output screen -
		// where the last question leads anyway - is the honest place to land.
		m.notice = "\"" + preset.Label + "\" filled in the options; esc goes back and changes any of them"
		m.noticeIsError = false
		return m.enterOutputNow()
	}

	m.stepIndex = next
	m.screen = ScreenStep
	m.notice = "\"" + preset.Label + "\" filled in some options"
	m.noticeIsError = false
	m.syncStep()
	return m, nil
}

// firstStepOutside is the row of the first active step the preset leaves
// unanswered, or -1 when it covers every question the task asks.
//
// It is deliberately not "the first step with no default". A step's default is
// the engine's fallback, not the user's decision: a preset that says nothing
// about the streaming question should still ask it, even though the task would
// have a default for it. Asking only what is unanswered keeps the preset list
// honest - a preset that covers less does less.
func (m Model) firstStepOutside(preset tasks.Preset) int {
	for i, step := range m.activeSteps() {
		if _, ok := preset.Answers[step.ID]; !ok {
			return i
		}
	}
	return -1
}

package wizard

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// taskEntry is one row of the task list, with whether this input can support it.
type taskEntry struct {
	task    tasks.Task
	applies bool
	reason  string
}

// taskEntries is the catalogue as the task screen shows it: every task, with the
// ones the input cannot support marked and explained rather than hidden
// (plan.md §6.3). Probed data is what makes a task inapplicable, so a nil probe
// means everything applies.
func (m Model) taskEntries() []taskEntry {
	if m.set == nil {
		return nil
	}
	entries := make([]taskEntry, 0, len(m.set.Tasks))
	for _, t := range m.set.Tasks {
		if t.Disabled {
			continue
		}
		ok, reason := t.AppliesTo(m.info, m.caps)
		entries = append(entries, taskEntry{task: t, applies: ok, reason: reason})
	}
	return entries
}

// firstRunnableTask is where the cursor starts: the first task this input and
// this build can actually run. Landing on a greyed-out row would put the
// cursor - the most prominent position on the screen - on the one thing enter
// refuses to start.
func (m Model) firstRunnableTask() int {
	for i, e := range m.taskEntries() {
		if e.applies {
			return i
		}
	}
	return 0
}

// taskBody lists the catalogue.
func (m Model) taskBody(width int) string {
	entries := m.taskEntries()
	if len(entries) == 0 {
		return m.styles.Error.Render("no tasks are available; the task definitions failed to load")
	}

	var b strings.Builder
	b.WriteString(m.styles.Heading.Render("What do you want to do?") + "\n")
	if m.input != "" {
		b.WriteString(m.styles.Muted.Render(shortPath(m.input, width)) + "\n")
	}
	b.WriteString("\n")

	for i, e := range entries {
		cursor := "  "
		if i == m.taskIndex {
			cursor = m.styles.Key.Render("> ")
		}

		// The cursor and the reason share the line with the label, so the label
		// is cut to what is left rather than allowed to push the reason off the
		// edge, where it would never be seen.
		room := width - 4
		if e.reason != "" {
			room -= lipgloss.Width(e.reason) + 2
		}
		label := truncate(e.task.Label, max(room, 8))
		if !e.applies {
			label = m.styles.ChoiceDisabled.Render(label)
		} else if i == m.taskIndex {
			label = lipgloss.NewStyle().Bold(true).Render(label)
		}

		line := cursor + label
		if e.reason != "" {
			line += "  " + m.styles.ChoiceDisabled.Render("("+e.reason+")")
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// hintTask explains the highlighted task, or why it cannot run.
func (m Model) hintTask() string {
	entries := m.taskEntries()
	if m.taskIndex < 0 || m.taskIndex >= len(entries) {
		return ""
	}
	e := entries[m.taskIndex]
	if !e.applies {
		return e.task.Label + " cannot run: " + e.reason
	}
	if e.task.Description != "" {
		return e.task.Description
	}
	return e.task.Label
}

// updateTask moves the task cursor and starts the chosen task.
func (m Model) updateTask(keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if !isKey {
		return m, nil
	}
	entries := m.taskEntries()

	switch {
	case key.Matches(keyMsg, m.keys.Up):
		m.taskIndex = wrap(m.taskIndex-1, len(entries))
	case key.Matches(keyMsg, m.keys.Down):
		m.taskIndex = wrap(m.taskIndex+1, len(entries))
	case key.Matches(keyMsg, m.keys.Select):
		if len(entries) == 0 {
			return m, nil
		}
		entry := entries[m.taskIndex]
		if !entry.applies {
			// Offering a task that cannot run is how a wizard ends up emitting
			// a command that fails at run time.
			m.notice, m.noticeIsError = "this task cannot run: "+entry.reason, true
			return m, nil
		}
		m.startTask(entry.task.ID)
		return m, nil
	case key.Matches(keyMsg, m.keys.Back):
		m.screen = ScreenInput
		m.notice, m.noticeIsError = "", false
		return m, m.picker.Init()
	}
	return m, nil
}

// wrap moves an index around a list of n items.
func wrap(i, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i % n) + n) % n
}

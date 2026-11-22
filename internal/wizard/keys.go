package wizard

import "charm.land/bubbles/v2/key"

// bindings are the wizard's key bindings. One set is shared by every screen; a
// screen simply does not list the ones it has no use for.
type bindings struct {
	Up   key.Binding
	Down key.Binding
	// Select confirms the answer and moves on.
	Select key.Binding
	// Toggle flips a checkbox in a multi step or a yes/no in a confirm step.
	Toggle key.Binding
	// Tab moves between the two fields of a time_range step.
	Tab  key.Binding
	Back key.Binding
	// BackEsc is Back without the `e` alias, for screens with a text field,
	// where `e` is a letter (DECISIONS D14).
	BackEsc key.Binding
	Quit    key.Binding
	Help    key.Binding
	ShowAll key.Binding
	// Filter narrows a list to the names containing what is typed (plan.md §6,
	// Keyboard: "/ to filter lists").
	Filter key.Binding
	// Open descends into the highlighted directory, and Parent goes back up.
	// They are separate from Select because a list of files has no single
	// "the value": enter finishes choosing, so descending needs its own keys.
	Open   key.Binding
	Parent key.Binding
	Copy   key.Binding
	Run    key.Binding
	// Cancel stops a running encode. It deliberately shares `q` and `ctrl+c`
	// with Quit: those are the keys a user reaches for to stop something, and
	// during a run they must ask rather than exit (plan.md §6.7).
	Cancel key.Binding
	// Log toggles the encoder log pane.
	Log key.Binding
	// Delete removes the partial output left behind by a cancelled run.
	Delete key.Binding
	// Reveal shows the output in the desktop's file manager.
	Reveal key.Binding
	// Edit opens the command for hand editing on the review screen, and
	// Apply accepts what was typed in it (plan.md §6.6).
	Edit  key.Binding
	Apply key.Binding
	// Script writes the command out as a runnable shell script.
	Script key.Binding
	// Revert drops a hand-edited command and goes back to the one the wizard
	// built, so an edit is never a one-way door (plan.md §6.6, DECISIONS D44).
	Revert key.Binding
}

// newBindings returns a fresh set. Bindings are keyed by name because the
// Bubbles v2 upgrade replaced the mutable DefaultKeyMap variables with
// functions (UPGRADE_GUIDE_V2 §2c).
func newBindings() bindings {
	return bindings{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),
		Toggle: key.NewBinding(
			key.WithKeys("space", "x"),
			key.WithHelp("space", "toggle"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next field"),
		),
		// DECISIONS D14: esc is the conventional way out, and `e` is the
		// single-finger alternative. Text fields only honour esc, because `e`
		// is a letter there.
		Back: key.NewBinding(
			key.WithKeys("esc", "e", "shift+tab"),
			key.WithHelp("esc/e", "back"),
		),
		BackEsc: key.NewBinding(
			key.WithKeys("esc", "shift+tab"),
			key.WithHelp("esc", "back"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "q"),
			key.WithHelp("q", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		ShowAll: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "show all files"),
		),
		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		Open: key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("l", "open"),
		),
		Parent: key.NewBinding(
			key.WithKeys("h", "left", "backspace"),
			key.WithHelp("h", "up"),
		),
		Copy: key.NewBinding(
			key.WithKeys("c"),
			// "copy" rather than "copy command": the review screen's short help
			// line carries six bindings, and one word keeps it inside 80
			// columns (plan.md §6).
			key.WithHelp("c", "copy"),
		),
		Run: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "run"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c", "q"),
			key.WithHelp("q", "stop"),
		),
		Log: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "log"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete partial file"),
		),
		Reveal: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open folder"),
		),
		Edit: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "edit command"),
		),
		Apply: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "use this command"),
		),
		Script: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "save as script"),
		),
		Revert: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "undo the edit"),
		),
	}
}

// helpView adapts a screen's bindings to the bubbles/help KeyMap interface.
type helpView struct {
	short []key.Binding
	full  [][]key.Binding
}

func (h helpView) ShortHelp() []key.Binding  { return h.short }
func (h helpView) FullHelp() [][]key.Binding { return h.full }

package wizard

import "charm.land/bubbles/v2/key"

// bindings are the wizard's key bindings. One set is shared by every screen; a
// screen simply does not list the ones it has no use for.
type bindings struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Back   key.Binding
	// BackEsc is Back without the `e` alias, for screens with a text field,
	// where `e` is a letter (DECISIONS D14).
	BackEsc key.Binding
	Quit    key.Binding
	Help    key.Binding
	ShowAll key.Binding
	Copy    key.Binding
	Run     key.Binding
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
		Copy: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "copy command"),
		),
		Run: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "run (M4)"),
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

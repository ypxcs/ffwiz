package wizard

import (
	"errors"

	"github.com/atotto/clipboard"
)

// Clipboard writes text to the system clipboard.
//
// It is an interface so the wizard can be tested without one, and so a machine
// with no clipboard support degrades with an explanation instead of appearing to
// have copied (plan.md §2, DECISIONS D16).
type Clipboard interface {
	WriteAll(text string) error
}

// systemClipboard uses atotto/clipboard, the library plan.md §2 names. It works
// on macOS, Windows, and X11 or Wayland with the usual helper binaries present;
// everywhere else WriteAll returns an error rather than panicking.
type systemClipboard struct{}

func (systemClipboard) WriteAll(text string) error { return clipboard.WriteAll(text) }

// NewSystemClipboard returns the system clipboard.
func NewSystemClipboard() Clipboard { return systemClipboard{} }

// UnavailableClipboard refuses every write. New uses it when the caller supplies
// no clipboard, so the model has one code path rather than a nil check in the
// middle of an action.
type UnavailableClipboard struct {
	// Reason is shown to the user.
	Reason string
}

func (u UnavailableClipboard) WriteAll(string) error {
	if u.Reason == "" {
		return errors.New("no clipboard is available")
	}
	return errors.New(u.Reason)
}

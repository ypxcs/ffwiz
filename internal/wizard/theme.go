package wizard

import (
	"fmt"
	"strings"
)

// Theme says which background the palette is built for.
//
// Lip Gloss v2 has no AdaptiveColor, so a style set is built for one known
// background and rebuilt when Bubble Tea reports the terminal's own answer
// (tea.BackgroundColorMsg). Following that answer is right far more often than
// not, so it is the default; the other two values exist because a terminal is
// allowed to say nothing - the query is an escape sequence, and ssh, screen and
// some terminals simply do not reply - and a terminal is allowed to be wrong
// (a dark-on-light terminal behind a multiplexer that reports the outer
// colours). Pinning the scheme with --theme is the escape hatch for both
// (plan.md §10, M6 "theming").
//
// NO_COLOR is deliberately not a Theme: it is not a question about which
// background, and it is not ours to implement. Bubble Tea detects the colour
// profile of its output and strips colour when NO_COLOR is set (keeping bold and
// other text decoration, as no-color.org specifies), and every colour the wizard
// draws goes through Lip Gloss, so the switch needs no code here at all
// (DECISIONS D46).
type Theme int

const (
	// ThemeAuto follows the terminal's own answer about its background.
	ThemeAuto Theme = iota
	// ThemeLight forces the palette meant for a light background.
	ThemeLight
	// ThemeDark forces the palette meant for a dark background.
	ThemeDark
)

// ParseTheme turns the --theme flag's value into a Theme. The empty string means
// the flag was not given, which is the same as "auto".
func ParseTheme(value string) (Theme, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return ThemeAuto, nil
	case "light":
		return ThemeLight, nil
	case "dark":
		return ThemeDark, nil
	}
	return ThemeAuto, fmt.Errorf("unknown theme %q: use auto, light or dark", value)
}

// String is the flag spelling, so an error or a log line reads back as the value
// the user typed.
func (t Theme) String() string {
	switch t {
	case ThemeLight:
		return "light"
	case ThemeDark:
		return "dark"
	default:
		return "auto"
	}
}

// IsDark is the background to build the first palette for: dark only when the
// user asked for it, since a terminal that has not answered yet is more often
// light than not (this is what plan.md §6 means by degrading gracefully).
func (t Theme) IsDark() bool { return t == ThemeDark }

// Pinned is true when the terminal's own answer must be ignored, which is the
// whole point of passing --theme.
func (t Theme) Pinned() bool { return t != ThemeAuto }

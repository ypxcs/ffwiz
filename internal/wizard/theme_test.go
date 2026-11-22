package wizard

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// This file covers the colour scheme (plan.md §10, M6 "theming"): the palette
// chosen for a background, and the --theme flag that overrides the terminal's
// own answer.
//
// NO_COLOR has no test here on purpose. It is not a choice the wizard makes: the
// colour profile of the output is Bubble Tea's, and every colour is drawn through
// Lip Gloss, so stripping colour is the renderer's job (DECISIONS D46). It is
// checked by hand on a pty instead, which is the only place the escape sequences
// exist at all.

func TestParseTheme(t *testing.T) {
	tests := []struct {
		in      string
		want    Theme
		wantErr bool
	}{
		{in: "", want: ThemeAuto},
		{in: "auto", want: ThemeAuto},
		{in: "  Auto ", want: ThemeAuto},
		{in: "light", want: ThemeLight},
		{in: "LIGHT", want: ThemeLight},
		{in: "dark", want: ThemeDark},
		{in: "darkish", wantErr: true},
		{in: "solarized", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ParseTheme(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTheme(%q) = %v, want an error", tc.in, got)
			} else if !strings.Contains(err.Error(), "auto, light or dark") {
				t.Errorf("ParseTheme(%q) error = %q, want it to name the accepted values", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTheme(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseTheme(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestThemeRoundTrips: the flag spelling comes back out of String, so an error
// message or a debug log reads as the value the user typed.
func TestThemeRoundTrips(t *testing.T) {
	for _, want := range []string{"auto", "light", "dark"} {
		theme, err := ParseTheme(want)
		if err != nil {
			t.Fatalf("ParseTheme(%q): %v", want, err)
		}
		if got := theme.String(); got != want {
			t.Errorf("ParseTheme(%q).String() = %q, want %q", want, got, want)
		}
	}
}

// TestThemeIsDarkAndPinned pins the two questions the flag answers.
func TestThemeIsDarkAndPinned(t *testing.T) {
	if ThemeAuto.IsDark() {
		t.Error("auto starts light, since a terminal that has not answered is only known not to be dark")
	}
	if ThemeAuto.Pinned() {
		t.Error("auto must let the terminal's answer through")
	}
	if ThemeLight.IsDark() || !ThemeLight.Pinned() {
		t.Error("light must pin a light palette")
	}
	if !ThemeDark.IsDark() || !ThemeDark.Pinned() {
		t.Error("dark must pin a dark palette")
	}
}

// paletteOf identifies the palette in use by a colour only one of the two uses.
// Reading a style back is the only way to see which one a model ended up with.
func paletteOf(m Model) bool { // true when the dark palette is in use
	return m.styles.Muted.GetForeground() == NewStyles(true).Muted.GetForeground()
}

func TestTheAutoThemeFollowsTheTerminal(t *testing.T) {
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	if paletteOf(s.model) {
		t.Fatal("the wizard started dark before the terminal said anything")
	}

	s.send(tea.BackgroundColorMsg{Color: color.Black})
	if !paletteOf(s.model) {
		t.Error("a terminal reporting a dark background did not switch the palette")
	}
}

// TestAPinnedThemeIgnoresTheTerminal is the reason the flag exists, and covers
// both of what it buys. The first is that the palette is right from the first
// frame - the model is checked straight out of New, before any message - so
// there is no light flash on a dark terminal whose answer arrives late. The
// second is that a terminal which answers wrongly - a light terminal behind a
// multiplexer reporting the outer colours, say - cannot undo an explicit
// --theme.
func TestAPinnedThemeIgnoresTheTerminal(t *testing.T) {
	tests := []struct {
		theme Theme
		dark  bool
	}{
		{theme: ThemeDark, dark: true},
		{theme: ThemeLight, dark: false},
	}
	for _, tc := range tests {
		t.Run(tc.theme.String(), func(t *testing.T) {
			caps := machineCaps()
			s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}, Theme: tc.theme})
			if got := paletteOf(s.model); got != tc.dark {
				t.Fatalf("the first frame's palette = dark:%v, want dark:%v", got, tc.dark)
			}

			// Both answers, and the one that contradicts the flag last, so a
			// model that merely ignored the first message would still fail.
			for _, c := range []color.Color{color.White, color.Black} {
				s.send(tea.BackgroundColorMsg{Color: c})
				if got := paletteOf(s.model); got != tc.dark {
					t.Errorf("after the terminal reported %T, palette = dark:%v, want dark:%v", c, got, tc.dark)
				}
			}
		})
	}
}

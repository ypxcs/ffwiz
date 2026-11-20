package wizard

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// token is one rendered piece of the command preview.
type token struct {
	text  string
	style lipgloss.Style
}

// commandPane renders the live ffmpeg command.
//
// The command is visible from the moment a task is chosen, because seeing it
// build up is the whole point of the wizard (plan.md §6). Answers that are not
// settled yet appear as <step-id> tokens rather than being quietly filled in, so
// the pane never claims the command is finished when it is not.
func (m Model) commandPane(width int) string {
	title := m.styles.PaneTitle.Render("command")

	cmd, pending, err := m.liveCommand()
	switch {
	case err != nil:
		return title + "\n" + m.styles.Error.Render("cannot build the command: "+err.Error())
	case cmd == nil:
		return title + "\n" + m.styles.Muted.Render("choose a task to see the command")
	}

	tokens := make([]token, 0, len(cmd.DisplayArgs())+1)
	tokens = append(tokens, token{text: "ffmpeg", style: m.styles.Key})
	for _, arg := range cmd.DisplayArgs() {
		style := m.styles.Muted
		text := arg
		if isPlaceholder(arg) {
			// A placeholder is a marker, not an argument: quoting it would make
			// it look like a path that really exists.
			style = m.styles.Placeholder
		} else {
			// Quoted exactly as the copied command quotes it. A path with a
			// space in it must look the same here as it does in the clipboard,
			// or the pane is showing a command that would not work.
			text = ffmpeg.Quote(arg)
		}
		tokens = append(tokens, token{text: text, style: style})
	}

	lines := wrapTokens(tokens, width)
	if len(pending) > 0 {
		lines = append(lines, m.styles.Muted.Render("waiting for: "+strings.Join(pending, ", ")))
	}
	return title + "\n" + strings.Join(lines, "\n")
}

// isPlaceholder reports whether an argument is one of the preview's <step-id>
// markers rather than something that will really be passed to ffmpeg.
func isPlaceholder(arg string) bool {
	return len(arg) > 2 && strings.HasPrefix(arg, "<") && strings.HasSuffix(arg, ">")
}

// wrapTokens lays tokens out into lines of at most width visible columns.
//
// Wrapping happens before styling, on plain text, because measuring a string
// that already contains escape sequences is exactly the kind of thing that goes
// wrong. Continuation lines are indented to line up under the first argument.
func wrapTokens(tokens []token, width int) []string {
	if width < 1 {
		width = 1
	}
	// Continuation lines are indented past the first token ("ffmpeg"), so the
	// arguments line up in a column instead of restarting at the left margin.
	indent := 0
	if len(tokens) > 0 {
		indent = lipgloss.Width(tokens[0].text) + 1
	}

	var lines []string
	var current []token
	currentWidth := 0
	// limit is how wide the next line may be: the full pane for the first line,
	// less the indent for every line after it.
	limit := width

	flush := func() {
		if len(current) == 0 {
			return
		}
		parts := make([]string, len(current))
		for i, t := range current {
			parts[i] = t.style.Render(t.text)
		}
		line := strings.Join(parts, " ")
		if len(lines) > 0 {
			line = strings.Repeat(" ", indent) + line
		}
		lines = append(lines, line)
		current = current[:0]
		currentWidth = 0
	}

	for _, t := range tokens {
		w := lipgloss.Width(t.text)
		// A token longer than the pane still has to go somewhere; giving it a
		// line of its own is better than an infinite loop.
		switch {
		case len(current) == 0:
			current = append(current, t)
			currentWidth = w
		case currentWidth+1+w <= limit:
			current = append(current, t)
			currentWidth += 1 + w
		default:
			flush()
			limit = max(width-indent, 1)
			current = append(current, t)
			currentWidth = w
		}
	}
	flush()

	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// liveCommand previews the command for the current answers. A nil command with
// a nil error means "there is nothing to preview yet".
func (m Model) liveCommand() (*ffmpeg.Command, []string, error) {
	if m.engine == nil || m.input == "" || m.width == 0 {
		return nil, nil, nil
	}
	preview, _, err := m.engine.Preview(m.answers, m.buildOptions())
	if err != nil {
		return nil, nil, err
	}
	return preview.Command, preview.Pending, nil
}

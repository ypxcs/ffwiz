package ffmpeg

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Splitting a command back into arguments is the inverse of Quote, and it exists
// because of one feature: the review screen lets the user edit the command by
// hand (plan.md §6.6). Handing that text to the run screen means turning it back
// into an argument slice, and the slice is what reaches exec - there is no shell
// anywhere in ffwiz (plan.md §8), so anything shell-like has to be undone here
// rather than left to a shell.

// ErrUnterminatedQuote is returned for a quote that is never closed. It is
// separate from the generic error so the message can name the offending
// character.
var ErrUnterminatedQuote = errors.New("unterminated quote")

// SplitCommand parses a command line into arguments the way a POSIX shell would
// split words, with two deliberate differences.
//
// First, the program name is dropped: the text on the review screen starts with
// "ffmpeg", because that is what the user would paste into a terminal, and the
// argument slice ffwiz executes never carries the program. A leading token is
// treated as the program only when its base name is ffmpeg or ffmpeg.exe, so a
// file genuinely called ffmpeg is not eaten.
//
// Second, nothing is expanded. `$HOME`, a backtick and a glob all stay exactly
// as typed, because there is no shell to expand them and a silent expansion of
// some of them would be worse than none.
//
//   - 'single quotes' are literal, with no escapes inside - which is what Quote
//     produces, so ffwiz's own output round-trips;
//   - "double quotes" allow \" \\ \$ \` and a line continuation, and keep a
//     backslash before anything else;
//   - outside quotes a backslash escapes the next character;
//   - a quoted empty string is a real, empty argument.
func SplitCommand(text string) ([]string, error) {
	var (
		args    []string
		word    strings.Builder
		started bool
	)
	// flush ends the current word, if one was begun. `started` is what makes
	// '' an empty argument rather than nothing at all.
	flush := func() {
		if started {
			args = append(args, word.String())
			word.Reset()
			started = false
		}
	}

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case ' ', '\t', '\n', '\r':
			flush()
		case '\'':
			started = true
			i++
			for ; i < len(runes) && runes[i] != '\''; i++ {
				word.WriteRune(runes[i])
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("%w: %s", ErrUnterminatedQuote, text)
			}
		case '"':
			started = true
			i++
			for ; i < len(runes) && runes[i] != '"'; i++ {
				if runes[i] != '\\' {
					word.WriteRune(runes[i])
					continue
				}
				// Inside double quotes a backslash only escapes a character
				// that is special there; before anything else it is literal.
				i++
				if i >= len(runes) {
					return nil, fmt.Errorf("%w: %s", ErrUnterminatedQuote, text)
				}
				switch next := runes[i]; next {
				case '"', '\\', '$', '`':
					word.WriteRune(next)
				case '\n':
					// A backslash-newline is a line continuation: nothing at
					// all, not even a space.
				default:
					word.WriteRune('\\')
					word.WriteRune(next)
				}
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("%w: %s", ErrUnterminatedQuote, text)
			}
		case '\\':
			i++
			if i >= len(runes) {
				// A backslash-newline at the end of a line joins it to the
				// next; a backslash with nothing after it would silently
				// swallow the end of the command.
				return nil, fmt.Errorf("%w: trailing backslash in %s", ErrUnterminatedQuote, text)
			}
			if runes[i] == '\n' {
				// A line continuation adds nothing, not even a word
				// boundary, so it must not mark a word as started.
				break
			}
			started = true
			word.WriteRune(runes[i])
		default:
			started = true
			word.WriteRune(r)
		}
	}
	flush()

	return dropProgram(args), nil
}

// dropProgram removes a leading ffmpeg from a parsed command.
func dropProgram(args []string) []string {
	if len(args) == 0 {
		return args
	}
	base := strings.ToLower(filepath.Base(args[0]))
	if base == "ffmpeg" || base == "ffmpeg.exe" {
		return args[1:]
	}
	return args
}

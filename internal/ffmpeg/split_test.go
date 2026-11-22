package ffmpeg

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain words",
			in:   "ffmpeg -i in.mp4 out.mp4",
			want: []string{"-i", "in.mp4", "out.mp4"},
		},
		{
			name: "extra spacing and tabs",
			in:   "  ffmpeg   -i\tin.mp4  \n out.mp4 ",
			want: []string{"-i", "in.mp4", "out.mp4"},
		},
		{
			name: "single quotes keep spaces",
			in:   "ffmpeg -i '/a b/c.mp4' out.mp4",
			want: []string{"-i", "/a b/c.mp4", "out.mp4"},
		},
		{
			// This is the form Quote emits for a path containing a quote, and
			// the reason the escape has to survive a round trip: sqlite's error
			// 'x' is spelled '\'' in a POSIX shell.
			name: "single quote escapes",
			in:   `ffmpeg -i '/a'\''b.mp4' out.mp4`,
			want: []string{"-i", "/a'b.mp4", "out.mp4"},
		},
		{
			name: "double quotes",
			in:   `ffmpeg -i "/a b.mp4" out.mp4`,
			want: []string{"-i", "/a b.mp4", "out.mp4"},
		},
		{
			name: "double quotes honour escapes",
			in:   `ffmpeg -metadata title=\"quoted\" out.mp4`,
			want: []string{"-metadata", `title="quoted"`, "out.mp4"},
		},
		{
			// A backslash before a character that is not special inside double
			// quotes stays a backslash: ffmpeg filtergraph syntax uses them.
			name: "double quotes keep a literal backslash",
			in:   `ffmpeg -vf "subtitles=/a\:b.srt" out.mp4`,
			want: []string{"-vf", `subtitles=/a\:b.srt`, "out.mp4"},
		},
		{
			name: "a quote can begin mid-word",
			in:   `ffmpeg -i a"b c"d.mp4`,
			want: []string{"-i", "ab cd.mp4"},
		},
		{
			name: "an empty quoted word is an argument",
			in:   `ffmpeg -metadata title='' out.mp4`,
			want: []string{"-metadata", "title=", "out.mp4"},
		},
		{
			name: "backslash escapes outside quotes",
			in:   `ffmpeg -i a\ b.mp4`,
			want: []string{"-i", "a b.mp4"},
		},
		{
			// Nothing is expanded: there is no shell to do it, and expanding
			// only the easy cases would be a surprise.
			name: "no expansion",
			in:   `ffmpeg -i $HOME/a.mp4 "*.mp4" '` + "`id`" + `'`,
			want: []string{"-i", "$HOME/a.mp4", "*.mp4", "`id`"},
		},
		{
			name: "a line continuation joins the words",
			in:   "ffmpeg -i in.mp4 \\\n -c:v copy out.mp4",
			want: []string{"-i", "in.mp4", "-c:v", "copy", "out.mp4"},
		},
		{
			name: "empty input is no arguments",
			in:   "   ",
			want: nil,
		},
		{
			// Only a leading token whose base name is ffmpeg is dropped, so a
			// filter argument that happens to be called ffmpeg survives.
			name: "a file called ffmpeg is not the program",
			in:   "ffmpeg -i ffmpeg out.mp4",
			want: []string{"-i", "ffmpeg", "out.mp4"},
		},
		{
			name: "a path to the program is dropped",
			in:   "/opt/homebrew/bin/ffmpeg -i in.mp4 out.mp4",
			want: []string{"-i", "in.mp4", "out.mp4"},
		},
		{
			name: "a command without a program name is fine",
			in:   "-i in.mp4 out.mp4",
			want: []string{"-i", "in.mp4", "out.mp4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitCommand(tc.in)
			if err != nil {
				t.Fatalf("SplitCommand(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitCommand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitCommandRejectsUnterminatedQuotes(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "unterminated single quote", in: "ffmpeg -i 'in.mp4 out.mp4"},
		{name: "unterminated double quote", in: `ffmpeg -i "in.mp4 out.mp4`},
		{name: "trailing backslash", in: `ffmpeg -i in.mp4 \`},
		{name: "trailing backslash in double quotes", in: `ffmpeg -i "in.mp4\`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitCommand(tc.in)
			if err == nil {
				t.Fatalf("SplitCommand(%q) succeeded, want an error", tc.in)
			}
			if !errors.Is(err, ErrUnterminatedQuote) {
				t.Errorf("SplitCommand(%q) = %v, want ErrUnterminatedQuote", tc.in, err)
			}
		})
	}
}

// TestSplitCommandUndoesQuote is the property that matters: the user edits the
// text the review screen shows, so Quote then SplitCommand has to reproduce the
// argument slice exactly (plan.md §6.6).
func TestSplitCommandUndoesQuote(t *testing.T) {
	args := []string{
		"-n", "-i", "/media/a b/clip's.mp4", "-c:v", "libx264", "-vf", "scale=640:-2",
		"-metadata", "title=a=b", "-map", "0:s:0?", "/out/ünïcode & more.mkv",
	}
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, "ffmpeg")
	for _, a := range args {
		quoted = append(quoted, Quote(a))
	}

	got, err := SplitCommand(strings.Join(quoted, " "))
	if err != nil {
		t.Fatalf("SplitCommand: %v", err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("round trip = %q, want %q", got, args)
	}
}

func TestRawCommandKeepsTheArgsAndAddsTheExecOnlyFlags(t *testing.T) {
	raw := []string{"-y", "-i", "in.mp4", "-c:v", "copy", "out.mp4"}
	cmd := RawCommand(raw)

	wantArgs := append(append([]string{}, execOnlyArgs...), raw...)
	if got := cmd.Args(); !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("Args() = %q, want %q", got, wantArgs)
	}
	// The display and the copied text must stay readable: the exec-only flags
	// are meaningless in a pasted command (DECISIONS D5).
	if got := cmd.DisplayArgs(); !reflect.DeepEqual(got, raw) {
		t.Errorf("DisplayArgs() = %q, want %q", got, raw)
	}
	if !cmd.Edited() {
		t.Error("Edited() = false for a hand-edited command")
	}
	if !cmd.Overwrites() {
		t.Error("Overwrites() = false for a command carrying -y")
	}
	if cmd.ExpectedDuration() != 0 {
		t.Error("a hand-edited command must not claim to know the output's length")
	}
}

// RawCommand copies its argument: the caller's slice is usually the one the
// parser just produced and the model keeps, and a command silently following a
// later edit of that slice would be a nasty way to run the wrong thing.
func TestRawCommandCopiesItsArgs(t *testing.T) {
	raw := []string{"-i", "in.mp4", "out.mp4"}
	cmd := RawCommand(raw)

	raw[0] = "-y"
	if got := cmd.DisplayArgs()[0]; got != "-i" {
		t.Errorf("the command followed the caller's slice: got %q, want -i", got)
	}
}

func TestRawCommandWithoutOverwrite(t *testing.T) {
	cmd := RawCommand([]string{"-n", "-i", "in.mp4", "out.mp4"})
	if cmd.Overwrites() {
		t.Error("Overwrites() = true for a command carrying -n")
	}
}

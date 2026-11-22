package ffmpeg

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func mustBuild(t *testing.T, req Request) *Command {
	t.Helper()
	cmd, err := Build(req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return cmd
}

func TestBuildArgumentOrder(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{
			name: "simple convert",
			req: Request{
				Inputs:     []InputSpec{{Path: "in.mp4"}},
				Output:     "out.mp4",
				OutputArgs: []string{"-c:v", "libx264", "-crf", "23", "-c:a", "copy"},
			},
			want: []string{"-n", "-i", "in.mp4", "-c:v", "libx264", "-crf", "23", "-c:a", "copy", "out.mp4"},
		},
		{
			name: "overwrite adds -y instead of -n",
			req: Request{
				Inputs:    []InputSpec{{Path: "in.mp4"}},
				Output:    "out.mp4",
				Overwrite: true,
			},
			want: []string{"-y", "-i", "in.mp4", "out.mp4"},
		},
		{
			name: "input options come before -i, output options after",
			req: Request{
				Inputs:     []InputSpec{{Path: "in.mp4", Args: []string{"-ss", "00:00:10"}}},
				OutputArgs: []string{"-t", "5", "-c", "copy"},
				Output:     "clip.mp4",
			},
			want: []string{"-n", "-ss", "00:00:10", "-i", "in.mp4", "-t", "5", "-c", "copy", "clip.mp4"},
		},
		{
			name: "filters are merged into one -vf and one -af",
			req: Request{
				Inputs:       []InputSpec{{Path: "in.mp4"}},
				VideoFilters: []string{"scale=1280:-2", "fps=30"},
				AudioFilters: []string{"loudnorm"},
				OutputArgs:   []string{"-c:v", "libx264"},
				Output:       "out.mp4",
			},
			want: []string{
				"-n", "-i", "in.mp4",
				"-vf", "scale=1280:-2,fps=30",
				"-af", "loudnorm",
				"-c:v", "libx264", "out.mp4",
			},
		},
		{
			name: "filter_complex is followed by explicit maps",
			req: Request{
				Inputs:        []InputSpec{{Path: "in.mp4"}},
				FilterComplex: []string{"fps=12,scale=480:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse"},
				Maps:          []string{"[out]"},
				Output:        "out.gif",
			},
			want: []string{
				"-n", "-i", "in.mp4",
				"-filter_complex", "fps=12,scale=480:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse",
				"-map", "[out]",
				"out.gif",
			},
		},
		{
			name: "format and multiple maps",
			req: Request{
				Inputs:     []InputSpec{{Path: "in.mkv"}},
				Maps:       []string{"0:v:0", "0:a:0"},
				Format:     "mp4",
				OutputArgs: []string{"-c", "copy"},
				Output:     "out.mp4",
			},
			want: []string{"-n", "-i", "in.mkv", "-map", "0:v:0", "-map", "0:a:0", "-f", "mp4", "-c", "copy", "out.mp4"},
		},
		{
			name: "multiple inputs for concatenation",
			req: Request{
				Inputs:     []InputSpec{{Path: "one.mp4"}, {Path: "two.mp4"}},
				OutputArgs: []string{"-c", "copy"},
				Output:     "joined.mp4",
			},
			want: []string{"-n", "-i", "one.mp4", "-i", "two.mp4", "-c", "copy", "joined.mp4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := mustBuild(t, tc.req)
			if got := cmd.DisplayArgs(); !slices.Equal(got, tc.want) {
				t.Errorf("DisplayArgs()\n got: %q\nwant: %q", got, tc.want)
			}

			// The executed form is the displayed form plus the execution-only
			// prefix, in that order.
			wantExec := append(slices.Clone(execOnlyArgs), tc.want...)
			if got := cmd.Args(); !slices.Equal(got, wantExec) {
				t.Errorf("Args()\n got: %q\nwant: %q", got, wantExec)
			}
		})
	}
}

func TestExecOnlyFlagsAreHiddenFromDisplay(t *testing.T) {
	cmd := mustBuild(t, Request{
		Inputs: []InputSpec{{Path: "in.mp4"}},
		Output: "out.mp4",
	})

	display := strings.Join(cmd.DisplayArgs(), " ")
	rendered := cmd.String()
	for _, hidden := range []string{"-hide_banner", "-nostdin", "-progress", "-nostats"} {
		if strings.Contains(display, hidden) {
			t.Errorf("DisplayArgs contains execution-only flag %q: %s", hidden, display)
		}
		if strings.Contains(rendered, hidden) {
			t.Errorf("String() contains execution-only flag %q: %s", hidden, rendered)
		}
	}

	// ...but they must all be present in the executed form.
	args := strings.Join(cmd.Args(), " ")
	for _, want := range []string{"-nostdin", "-hide_banner", "-progress pipe:1", "-nostats"} {
		if !strings.Contains(args, want) {
			t.Errorf("Args() is missing %q: %s", want, args)
		}
	}
}

func TestBuildRejectsFilterComplexWithoutMaps(t *testing.T) {
	// Without a -map, ffmpeg fails with "Output file does not contain any
	// stream". Catching it at build time turns a confusing runtime error into a
	// clear one (DECISIONS D2).
	_, err := Build(Request{
		Inputs:        []InputSpec{{Path: "in.mp4"}},
		FilterComplex: []string{"split[a][b]"},
		Output:        "out.mp4",
	})
	if err == nil {
		t.Fatal("Build accepted filter_complex with no maps")
	}
	if !strings.Contains(err.Error(), "map") {
		t.Errorf("error %q should mention the missing map", err)
	}
}

func TestBuildValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr error
	}{
		{"no inputs", Request{Output: "out.mp4"}, ErrNoInputs},
		{"no output", Request{Inputs: []InputSpec{{Path: "in.mp4"}}}, nil},
		{"blank output", Request{Inputs: []InputSpec{{Path: "in.mp4"}}, Output: "   "}, nil},
		{
			"output would clobber the input",
			Request{Inputs: []InputSpec{{Path: "in.mp4"}}, Output: "in.mp4"},
			ErrSameFile,
		},
		{
			"output clobbers the input via a different spelling",
			Request{Inputs: []InputSpec{{Path: "media/in.mp4"}}, Output: "./media/in.mp4"},
			ErrSameFile,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.req)
			if err == nil {
				t.Fatal("Build succeeded, want an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}

func TestBuildAllowsDifferentOutputPath(t *testing.T) {
	if _, err := Build(Request{
		Inputs: []InputSpec{{Path: "media/in.mp4"}},
		Output: "media/in_out.mp4",
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestStringQuoting(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"plain", "out.mp4", "ffmpeg -n -i in.mp4 out.mp4"},
		{"space", "my video.mp4", "ffmpeg -n -i in.mp4 'my video.mp4'"},
		{"single quote", "it's.mp4", `ffmpeg -n -i in.mp4 'it'\''s.mp4'`},
		{"ampersand", "a&b.mp4", "ffmpeg -n -i in.mp4 'a&b.mp4'"},
		{"dollar", "$HOME.mp4", "ffmpeg -n -i in.mp4 '$HOME.mp4'"},
		{"unicode is quoted for safety", "ünïcodé.mp4", "ffmpeg -n -i in.mp4 'ünïcodé.mp4'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := mustBuild(t, Request{
				Inputs: []InputSpec{{Path: "in.mp4"}},
				Output: tc.path,
			})
			if got := cmd.String(); got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},
		{"plain", "plain"},
		{"scale=1280:-2", "scale=1280:-2"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
		{"a;b", "'a;b'"},
		{"has\nnewline", "'has\nnewline'"},
	}
	for _, tc := range tests {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGuardPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"in.mp4", "in.mp4"},
		{"-leading-dash.mp4", "./-leading-dash.mp4"},
		{"--flag-looking", "./--flag-looking"},
		{"/abs/-dash.mp4", "/abs/-dash.mp4"},
		{"media/-dash.mp4", "media/-dash.mp4"},
	}
	for _, tc := range tests {
		if got := GuardPath(tc.in); got != tc.want {
			t.Errorf("GuardPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMergeFiltersJoinsInOrder(t *testing.T) {
	tests := []struct {
		name  string
		chain []string
		want  string
	}{
		{"empty", nil, ""},
		{"single", []string{"scale=1280:-2"}, "scale=1280:-2"},
		{"order preserved", []string{"scale=1280:-2", "fps=30"}, "scale=1280:-2,fps=30"},
		{
			// An atempo chain for 0.25x is the same filter twice, and that is
			// the point: deduplicating would change the playback speed.
			"repeats are kept",
			[]string{"atempo=0.5", "atempo=0.5"},
			"atempo=0.5,atempo=0.5",
		},
		{"blanks skipped", []string{"", "  ", "hflip"}, "hflip"},
		{"whitespace trimmed", []string{"  hflip  "}, "hflip"},
		{"similar but different kept", []string{"scale=1280:-2", "scale=640:-2"}, "scale=1280:-2,scale=640:-2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeFilters(tc.chain); got != tc.want {
				t.Errorf("mergeFilters(%q) = %q, want %q", tc.chain, got, tc.want)
			}
		})
	}
}

func TestExpectedDurationAndOverwrite(t *testing.T) {
	cmd := mustBuild(t, Request{
		Inputs:           []InputSpec{{Path: "in.mp4"}},
		Output:           "out.mp4",
		ExpectedDuration: 5 * time.Second,
	})
	if got := cmd.ExpectedDuration(); got != 5*time.Second {
		t.Errorf("ExpectedDuration() = %v, want 5s", got)
	}
	if cmd.Overwrites() {
		t.Error("Overwrites() = true, want false by default")
	}

	cmd = mustBuild(t, Request{
		Inputs:    []InputSpec{{Path: "in.mp4"}},
		Output:    "out.mp4",
		Overwrite: true,
	})
	if !cmd.Overwrites() {
		t.Error("Overwrites() = false, want true")
	}
}

func TestScript(t *testing.T) {
	cmd := mustBuild(t, Request{
		Inputs:     []InputSpec{{Path: "in.mp4"}},
		OutputArgs: []string{"-c", "copy"},
		Output:     "out.mp4",
	})
	want := "#!/bin/sh\nset -e\n\nffmpeg -n -i in.mp4 -c copy out.mp4\n"
	if got := cmd.Script(); got != want {
		t.Errorf("Script() = %q, want %q", got, want)
	}
}

// TestScriptNotesTheFilesItReads: a command built around a file ffwiz writes -
// the concatenation list - is not self-contained, and the directory it lives in
// is deleted when ffwiz exits (DECISIONS D10), so the script has to say so
// rather than fail later with "No such file or directory".
func TestScriptNotesTheFilesItReads(t *testing.T) {
	cmd := mustBuild(t, Request{
		Inputs:    []InputSpec{{Path: "list.txt", Args: []string{"-f", "concat", "-safe", "0"}}},
		Output:    "out.mp4",
		Artifacts: []Artifact{{Path: "/tmp/ffwiz-x/concat-abc123.txt", Content: "file 'a.mp4'\n"}},
	})

	script := cmd.Script()
	for _, want := range []string{"#!/bin/sh", "set -e", "/tmp/ffwiz-x/concat-abc123.txt"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not mention %q:\n%s", want, script)
		}
	}
	// The comment stays a comment: the command itself has to be the last line,
	// or `set -e` would not cover it.
	if !strings.HasSuffix(script, "out.mp4\n") {
		t.Errorf("the command is not the end of the script:\n%s", script)
	}
}

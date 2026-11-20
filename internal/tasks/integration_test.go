package tasks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// These tests run the commands the engine produces against the real ffmpeg. They
// skip when ffmpeg or the generated sample media are missing, so the suite still
// passes on a machine without either.
//
// The point is the acceptance criterion in plan.md §11: a generated command must
// be *correct*, not merely well-formed. Each case is checked by probing the
// output rather than by trusting the exit status alone.

const sampleDir = "../ffmpeg/testdata/generated"

func requireTools(t *testing.T) ffmpeg.Tools {
	t.Helper()
	tools, err := ffmpeg.Locate(context.Background(), ffmpeg.Overrides{})
	if err != nil {
		t.Skipf("ffmpeg/ffprobe not available: %v", err)
	}
	return tools
}

func requireSample(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(sampleDir, name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample media missing (run `make samples`): %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestIntegrationRunGeneratedCommands(t *testing.T) {
	tools := requireTools(t)
	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")

	tests := []struct {
		name      string
		answers   map[string]string
		input     string
		outExt    string
		wantVideo string
		wantAudio string
	}{
		{
			name:      "defaults produce an H.264/AAC MP4",
			input:     "av.mp4",
			outExt:    "mp4",
			wantVideo: "h264",
			wantAudio: "aac",
		},
		{
			name:      "H.265 in MKV",
			answers:   map[string]string{"container": "mkv", "vcodec": "h265", "audio": "aac"},
			input:     "av.mp4",
			outExt:    "mkv",
			wantVideo: "hevc",
			wantAudio: "aac",
		},
		{
			name:      "WebM gets VP9 and Opus",
			answers:   map[string]string{"container": "webm"},
			input:     "av.mp4",
			outExt:    "webm",
			wantVideo: "vp9",
			wantAudio: "opus",
		},
		{
			name:      "stream copy",
			answers:   map[string]string{"vcodec": "copy"},
			input:     "av.mp4",
			outExt:    "mp4",
			wantVideo: "h264",
			wantAudio: "aac",
		},
		{
			name:      "audio removed",
			answers:   map[string]string{"audio": "none"},
			input:     "av.mp4",
			outExt:    "mp4",
			wantVideo: "h264",
		},
		{
			name:      "a file with no audio stream",
			input:     "video_only.mp4",
			outExt:    "mp4",
			wantVideo: "h264",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := requireSample(t, tc.input)
			info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, input)
			if err != nil {
				t.Fatalf("probing %s: %v", input, err)
			}

			caps, err := tools.Capabilities(context.Background())
			if err != nil {
				t.Fatalf("Capabilities: %v", err)
			}

			output := filepath.Join(t.TempDir(), "out."+tc.outExt)
			eng := &Engine{Task: task, Info: info, Caps: &caps}
			cmd, _, err := eng.Build(tc.answers, tasksBuildOptions(input, output))
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			runFFmpeg(t, tools.FFmpeg, cmd.Args())

			got, err := ffmpeg.Probe(context.Background(), tools.FFprobe, output)
			if err != nil {
				t.Fatalf("probing the output: %v", err)
			}
			if v, ok := got.FirstStream(ffmpeg.StreamVideo); !ok {
				t.Error("the output has no video stream")
			} else if v.CodecName != tc.wantVideo {
				t.Errorf("video codec = %q, want %q", v.CodecName, tc.wantVideo)
			}
			a, hasAudio := got.FirstStream(ffmpeg.StreamAudio)
			if tc.wantAudio == "" {
				if hasAudio {
					t.Errorf("the output has an audio stream (%s), want none", a.CodecName)
				}
			} else if !hasAudio {
				t.Errorf("the output has no audio stream, want %s", tc.wantAudio)
			} else if a.CodecName != tc.wantAudio {
				t.Errorf("audio codec = %q, want %q", a.CodecName, tc.wantAudio)
			}

			// The declared output name must match what the command wrote, or the
			// review screen is lying to the user.
			if args := cmd.DisplayArgs(); args[len(args)-1] != output {
				t.Errorf("the command writes %q, but the output path is %q", args[len(args)-1], output)
			}
		})
	}
}

// TestIntegrationRunAwkwardFilenames covers plan.md §11: spaces, quotes, an
// ampersand and non-ASCII characters must work in every task.
func TestIntegrationRunAwkwardFilenames(t *testing.T) {
	tools := requireTools(t)
	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")
	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	for _, name := range []string{"sp ace.mp4", "quote's.mp4", "&and.mp4", "ünïcodé.mp4"} {
		t.Run(name, func(t *testing.T) {
			input := requireSample(t, name)
			info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, input)
			if err != nil {
				t.Fatalf("probing %s: %v", input, err)
			}

			// Put the output next to the input so the awkward directory name is
			// exercised too, then clean up.
			output := filepath.Join(filepath.Dir(input), "awkward_out.mp4")
			t.Cleanup(func() { os.Remove(output) })

			eng := &Engine{Task: task, Info: info, Caps: &caps}
			cmd, _, err := eng.Build(nil, tasksBuildOptions(input, output))
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			runFFmpeg(t, tools.FFmpeg, cmd.Args())

			if _, err := os.Stat(output); err != nil {
				t.Fatalf("the output was not created: %v", err)
			}
			// The rendered command must be pasteable, which is what the test
			// would otherwise miss: the executed form never goes through a
			// shell, but the displayed one does. A name containing a quote
			// appears in its escaped form, so both spellings are accepted.
			base := filepath.Base(input)
			escaped := strings.ReplaceAll(base, "'", `'\''`)
			if got := cmd.String(); !strings.Contains(got, base) && !strings.Contains(got, escaped) {
				t.Errorf("the displayed command does not name the input: %s", got)
			}
		})
	}
}

// TestIntegrationRunUnixPasteableCommand runs the *displayed* command through
// /bin/sh, which is what the user does when they paste the preview into a
// terminal. This is the only test that exercises ffmpeg.Quote.
func TestIntegrationRunUnixPasteableCommand(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh is not available")
	}
	tools := requireTools(t)
	input := requireSample(t, "quote's.mp4")

	info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, input)
	if err != nil {
		t.Fatalf("probing %s: %v", input, err)
	}
	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")
	output := filepath.Join(t.TempDir(), "pasted out.mp4")

	eng := &Engine{Task: task, Info: info, Caps: &caps}
	cmd, _, err := eng.Build(nil, tasksBuildOptions(input, output))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Replace the program name with the located binary, then run the rest of
	// the rendered line verbatim through a shell.
	rendered := strings.Replace(cmd.String(), "ffmpeg", tools.FFmpeg, 1)
	runFFmpeg(t, "/bin/sh", []string{"-c", rendered})

	if _, err := os.Stat(output); err != nil {
		t.Fatalf("pasting the command into a shell did not create the output: %v", err)
	}
}

// tasksBuildOptions is BuildOptions for the integration tests: the output path is
// always explicit so the test knows where to look.
func tasksBuildOptions(input, output string) BuildOptions {
	return BuildOptions{Input: input, Output: output}
}

// runFFmpeg executes a command and fails the test with ffmpeg's own stderr, so a
// failure is diagnosable without re-running by hand.
func runFFmpeg(t *testing.T, program string, args []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, program, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("running\n  %s %s\nfailed: %v\n%s", program, strings.Join(args, " "), err, stderr.String())
	}
}

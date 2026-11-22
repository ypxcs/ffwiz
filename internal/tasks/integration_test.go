package tasks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
			t.Cleanup(func() { _ = os.Remove(output) })

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

// TestIntegrationRunCatalogueTasks runs the tasks plan.md §5 asks for against
// the real ffmpeg and checks what came out, rather than trusting the exit
// status. Each case names the output extension (or sequence pattern) so the test
// knows what the command was supposed to write.
func TestIntegrationRunCatalogueTasks(t *testing.T) {
	tools := requireTools(t)
	set := loadBuiltin(t)
	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	// The concat task joins clips by path, so its answer is absolute paths to the
	// two sample clips. They are resolved once, before the table: the samples may
	// be missing, and requireSample is what skips the case then.
	part1, err := filepath.Abs(filepath.Join(sampleDir, "part1.mp4"))
	if err != nil {
		t.Fatalf("resolving the sample clip path: %v", err)
	}
	part2, err := filepath.Abs(filepath.Join(sampleDir, "part2.mp4"))
	if err != nil {
		t.Fatalf("resolving the sample clip path: %v", err)
	}

	tests := []struct {
		name    string
		task    string
		answers map[string]string
		input   string
		// out is appended to "out", so "mp3" writes out.mp3 and
		// "%04d.png" writes out.%04d.png.
		out string
		// wantFiles is how many files the run should leave behind; 0 means
		// "at least one, however many the run decides".
		wantFiles int
		// check is run on the probed output of an ordinary file.
		check func(t *testing.T, got *ffmpeg.MediaInfo)
		// needFilter skips the case when this ffmpeg filter is missing. It is
		// how a task whose whole purpose is one filter is tested where that
		// filter exists, without failing where it does not (DECISIONS D17).
		needFilter string
	}{
		{
			name:  "compress: the default CRF encode",
			task:  "compress_video",
			input: "av.mp4",
			out:   "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "h264")
				wantStream(t, got, ffmpeg.StreamAudio, "aac")
			},
		},
		{
			name:    "compress: a target size is not exceeded",
			task:    "compress_video",
			answers: map[string]string{"method": "size", "size": "10"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "h264")
				// The bitrate is worked out from the target, so the file can
				// come in under it but must not overshoot it (DECISIONS D8).
				if got.Format.Size > 10*1024*1024 {
					t.Errorf("the output is %d bytes, over the 10 MiB target", got.Format.Size)
				}
			},
		},
		{
			name:  "extract audio: MP3",
			task:  "extract_audio",
			input: "av.mp4",
			out:   "mp3",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamAudio, "mp3")
				noStream(t, got, ffmpeg.StreamVideo)
			},
		},
		{
			name:    "extract audio: FLAC",
			task:    "extract_audio",
			answers: map[string]string{"format": "flac"},
			input:   "av.mp4",
			out:     "flac",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamAudio, "flac")
			},
		},
		{
			name:    "extract audio: WAV",
			task:    "extract_audio",
			answers: map[string]string{"format": "wav"},
			input:   "av.mp4",
			out:     "wav",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamAudio, "pcm_s16le")
			},
		},
		{
			name:    "extract audio: M4A",
			task:    "extract_audio",
			answers: map[string]string{"format": "m4a", "bitrate": "128"},
			input:   "av.mp4",
			out:     "m4a",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamAudio, "aac")
				noStream(t, got, ffmpeg.StreamVideo)
			},
		},
		{
			name:    "trim: fast copy",
			task:    "trim",
			answers: map[string]string{"range": "00:00:00.500,00:00:01.500", "mode": "fast"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				// The fast branch starts on a keyframe, so the clip can be a
				// little long rather than exactly one second (plan.md §5.4).
				wantDurationNear(t, got, time.Second, 600*time.Millisecond)
			},
		},
		{
			name:    "trim: accurate",
			task:    "trim",
			answers: map[string]string{"range": "00:00:00.500,00:00:01.500"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantDurationNear(t, got, time.Second, 200*time.Millisecond)
			},
		},
		{
			name:    "resize: 480p keeps the aspect ratio and an even width",
			task:    "resize",
			answers: map[string]string{"height": "480"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				v, ok := got.FirstStream(ffmpeg.StreamVideo)
				if !ok {
					t.Fatal("the output has no video stream")
				}
				if v.Height != 480 {
					t.Errorf("height = %d, want 480", v.Height)
				}
				if v.Width%2 != 0 {
					t.Errorf("width = %d, want an even number", v.Width)
				}
				if v.Width != 640 {
					t.Errorf("width = %d, want 640 for a 4:3 input", v.Width)
				}
			},
		},
		{
			name:    "speed: twice as fast halves the length",
			task:    "change_speed",
			answers: map[string]string{"speed": "2"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantDurationNear(t, got, time.Second, 300*time.Millisecond)
			},
		},
		{
			name:    "speed: half as fast doubles the length, sound intact",
			task:    "change_speed",
			answers: map[string]string{"speed": "0.5"},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantDurationNear(t, got, 4*time.Second, 400*time.Millisecond)
				wantStream(t, got, ffmpeg.StreamAudio, "aac")
			},
		},
		{
			name:  "strip metadata: copying the streams",
			task:  "strip_metadata",
			input: "av.mp4",
			out:   "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "h264")
				if title := got.Format.Tags["title"]; title != "" {
					t.Errorf("the output still has a title tag: %q", title)
				}
			},
		},
		{
			name:  "frames: one thumbnail",
			task:  "extract_frames",
			input: "av.mp4",
			out:   "png",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "png")
			},
		},
		{
			name:      "frames: a sequence every second",
			task:      "extract_frames",
			answers:   map[string]string{"mode": "every", "every": "1"},
			input:     "av.mp4",
			out:       "%04d.png",
			wantFiles: 2, // the sample is two seconds long
		},
		{
			name:  "gif: a palette animation with no audio stream",
			task:  "gif",
			input: "av.mp4",
			out:   "gif",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "gif")
				noStream(t, got, ffmpeg.StreamAudio)
			},
		},
		{
			name:    "gif: a time range limits the length",
			task:    "gif",
			answers: map[string]string{"clip": "yes", "range": "00:00:00.500,00:00:01.500"},
			input:   "av.mp4",
			out:     "gif",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantDurationNear(t, got, time.Second, 300*time.Millisecond)
			},
		},
		{
			// The two sample clips are one second each and share their codecs,
			// so the concat demuxer copies them and the join is their sum. The
			// list file has to be written first, which the harness does below.
			// They are written with -an, so the joined file has no sound.
			name:    "concat: two clips joined end to end",
			task:    "concat",
			answers: map[string]string{"clips": part1 + "\n" + part2},
			input:   "av.mp4",
			out:     "mp4",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "h264")
				noStream(t, got, ffmpeg.StreamAudio)
				wantDurationNear(t, got, 2*time.Second, 300*time.Millisecond)
			},
		},
		{
			// The escaped path is the point of this case. `make samples` writes
			// subs.srt with plain content; the awkward-name coverage for filter
			// arguments lives in internal/ffmpeg, which asks ffmpeg about every
			// character at once through the movie filter.
			name:       "burn subtitles: re-encoded with the text drawn on",
			task:       "burn_subtitles",
			answers:    map[string]string{"subs": "../../ffmpeg/testdata/generated/subs.srt"},
			input:      "av.mp4",
			out:        "mp4",
			needFilter: "subtitles",
			check: func(t *testing.T, got *ffmpeg.MediaInfo) {
				wantStream(t, got, ffmpeg.StreamVideo, "h264")
				wantStream(t, got, ffmpeg.StreamAudio, "aac")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needFilter != "" && !caps.HasFilter(tc.needFilter) {
				t.Skipf("this ffmpeg has no %s filter (usually a build without libass)", tc.needFilter)
			}
			input := requireSample(t, tc.input)
			info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, input)
			if err != nil {
				t.Fatalf("probing %s: %v", input, err)
			}

			output := filepath.Join(t.TempDir(), "out."+tc.out)
			task, ok := set.Get(tc.task)
			if !ok {
				t.Fatalf("%s is not in the catalogue", tc.task)
			}
			eng := &Engine{Task: task, Info: info, Caps: &caps, TempDir: t.TempDir()}
			cmd, _, err := eng.Build(tc.answers, tasksBuildOptions(input, output))
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// A command may name a file ffwiz writes for it - the concat list.
			// The runner normally writes it inside Start; this harness runs the
			// argument slice directly, so it has to do it here (DECISIONS D41).
			if err := ffmpeg.WriteArtifacts(cmd); err != nil {
				t.Fatalf("writing the files the command reads: %v", err)
			}

			runFFmpeg(t, tools.FFmpeg, cmd.Args())

			files := ffmpeg.OutputFiles(output)
			if len(files) == 0 {
				t.Fatalf("the command wrote nothing at %s", output)
			}
			if tc.wantFiles > 0 && len(files) != tc.wantFiles {
				t.Errorf("the run left %d files, want %d: %v", len(files), tc.wantFiles, files)
			}
			if tc.check == nil {
				return
			}
			got, err := ffmpeg.Probe(context.Background(), tools.FFprobe, files[0])
			if err != nil {
				t.Fatalf("probing the output: %v", err)
			}
			tc.check(t, got)
		})
	}
}

// TestIntegrationRunHardwareEncoder runs the command the wizard builds when the
// user picks a hardware encoder (plan.md §7).
//
// It runs only where the machine really has one, which is the same pair of
// conditions the task file gates the choice on: this build has the encoder
// (`requires_encoder`) and it belongs to this platform (`platform`, DECISIONS
// D33). An encoder that is listed but cannot open a device - a build with CUDA
// support on a machine with no Nvidia card - is skipped rather than failed,
// because plan.md §7 says a listing does not guarantee a working device.
func TestIntegrationRunHardwareEncoder(t *testing.T) {
	tools := requireTools(t)
	set := loadBuiltin(t)
	task, ok := set.Get("convert_video")
	if !ok {
		t.Fatal("convert_video is not in the catalogue")
	}
	input := requireSample(t, "av.mp4")
	info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, input)
	if err != nil {
		t.Fatalf("probing %s: %v", input, err)
	}
	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	encoder := caps.PreferredHardwareEncoder("h264", runtime.GOOS)
	if encoder == "" {
		t.Skipf("this ffmpeg has no H.264 hardware encoder for %s", runtime.GOOS)
	}
	// A choice's value is the encoder's name, so the answer this test gives is
	// the one the capability implies rather than a name written out per machine.
	// A known encoder the task file does not offer - AMF or Media Foundation,
	// which this pass left out - has nothing to test.
	if !offeredChoice(task, "vcodec", encoder) {
		t.Skipf("convert_video does not offer %s", encoder)
	}

	output := filepath.Join(t.TempDir(), "out.mp4")
	eng := &Engine{Task: task, Info: info, Caps: &caps}
	cmd, _, err := eng.Build(map[string]string{"vcodec": encoder}, tasksBuildOptions(input, output))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The command must actually name the hardware encoder and must not also name
	// a software one: two -c:v options read as a mistake (DECISIONS D42).
	if got := strings.Join(cmd.Args(), " "); !strings.Contains(got, encoder) {
		t.Fatalf("the command does not use %s: %s", encoder, got)
	} else if strings.Contains(got, "libx264") {
		t.Errorf("the command names both a hardware and a software encoder: %s", got)
	}

	if err := runHardwareEncode(t, tools.FFmpeg, cmd.Args()); err != nil {
		t.Skipf("%s is listed but did not run here: %v", encoder, err)
	}

	got, err := ffmpeg.Probe(context.Background(), tools.FFprobe, output)
	if err != nil {
		t.Fatalf("probing the output: %v", err)
	}
	wantStream(t, got, ffmpeg.StreamVideo, "h264")
	wantStream(t, got, ffmpeg.StreamAudio, "aac")
}

// offeredChoice reports whether a task's step offers a choice value.
func offeredChoice(task Task, stepID, value string) bool {
	for _, step := range task.Steps {
		if step.ID != stepID {
			continue
		}
		for _, c := range step.Choices {
			if c.Value == value {
				return true
			}
		}
	}
	return false
}

// runHardwareEncode runs a hardware-encoder command and returns ffmpeg's error
// with its stderr, rather than failing the test: whether the device is usable is
// exactly what the caller cannot know in advance.
func runHardwareEncode(t *testing.T, ffmpegPath string, args []string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var out strings.Builder
	cmd.Stderr = &out
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// TestIntegrationRunPresets runs every preset the catalogue ships against the
// real ffmpeg (plan.md §5).
//
// A preset is a promise about a whole outcome - "an MP4 that fits under Discord's
// 25 MB limit" - and a promise that is only ever built, never run, rots the
// moment a step or a flag underneath it changes. So each preset is built from its
// own answers and executed, and the file it said it would write has to appear and
// be readable.
func TestIntegrationRunPresets(t *testing.T) {
	tools := requireTools(t)
	set := loadBuiltin(t)
	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	ran := 0
	for _, task := range set.Tasks {
		if len(task.Presets) == 0 {
			continue
		}
		sample := "av.mp4"
		if !task.Requires.Video {
			sample = "audio_only.mp3"
		}
		// The engine names the output after its input, so the run works on a copy
		// in the test's own directory rather than writing into testdata (and the
		// copy proves the "next to the input" rule at the same time).
		work := t.TempDir()
		local := filepath.Join(work, filepath.Base(sample))
		data, err := os.ReadFile(requireSample(t, sample))
		if err != nil {
			t.Fatalf("copying the sample: %v", err)
		}
		if err := os.WriteFile(local, data, 0o600); err != nil {
			t.Fatalf("copying the sample: %v", err)
		}
		info, err := ffmpeg.Probe(context.Background(), tools.FFprobe, local)
		if err != nil {
			t.Fatalf("probing %s: %v", local, err)
		}

		for _, preset := range task.Presets {
			t.Run(task.ID+"/"+preset.ID, func(t *testing.T) {
				ran++
				dir := t.TempDir()
				// No output path is given, so the engine names it exactly as the
				// wizard would: the preset's container choice decides the
				// extension (plan.md §6.5, DECISIONS D24).
				eng := &Engine{Task: task, Info: info, Caps: &caps, TempDir: dir}
				cmd, res, err := eng.Build(preset.Answers, BuildOptions{Input: local})
				if err != nil {
					t.Fatalf("Build with the preset's answers %v: %v", preset.Answers, err)
				}
				output, err := DefaultOutput(local, res)
				if err != nil {
					t.Fatalf("DefaultOutput: %v", err)
				}
				if err := ffmpeg.WriteArtifacts(cmd); err != nil {
					t.Fatalf("writing the files the command reads: %v", err)
				}

				runFFmpeg(t, tools.FFmpeg, cmd.Args())

				files := ffmpeg.OutputFiles(output)
				if len(files) == 0 {
					t.Fatalf("the preset wrote nothing at %s", output)
				}
				got, err := ffmpeg.Probe(context.Background(), tools.FFprobe, files[0])
				if err != nil {
					t.Fatalf("probing the output: %v", err)
				}
				// A preset whose name quotes a size is making a claim about the
				// file, so the claim is checked rather than assumed.
				if target := preset.Answers["size"]; target != "" {
					mib, err := strconv.ParseFloat(target, 64)
					if err != nil {
						t.Fatalf("the preset's target size %q is not a number: %v", target, err)
					}
					if float64(got.Format.Size) > mib*1024*1024 {
						t.Errorf("the output is %d bytes, over the %s MiB the preset targets", got.Format.Size, target)
					}
				}
				t.Logf("%s: %s", preset.Label, strings.Join(cmd.DisplayArgs(), " "))
			})
		}
	}
	if ran == 0 {
		t.Fatal("the catalogue ships no presets, so this test proves nothing")
	}
}

// wantStream asserts the output has a stream of the given kind and codec.
func wantStream(t *testing.T, got *ffmpeg.MediaInfo, kind ffmpeg.StreamType, codec string) {
	t.Helper()
	s, ok := got.FirstStream(kind)
	if !ok {
		t.Errorf("the output has no %s stream, want %s", kind, codec)
		return
	}
	if s.CodecName != codec {
		t.Errorf("%s codec = %q, want %q", kind, s.CodecName, codec)
	}
}

func noStream(t *testing.T, got *ffmpeg.MediaInfo, kind ffmpeg.StreamType) {
	t.Helper()
	if s, ok := got.FirstStream(kind); ok {
		t.Errorf("the output has a %s stream (%s), want none", kind, s.CodecName)
	}
}

// wantDurationNear asserts the output lasts about as long as expected. The
// tolerance is generous: frame boundaries and stream copy make an exact match
// meaningless.
func wantDurationNear(t *testing.T, got *ffmpeg.MediaInfo, want, tolerance time.Duration) {
	t.Helper()
	delta := got.Duration() - want
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		t.Errorf("duration = %s, want %s within %s", got.Duration(), want, tolerance)
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

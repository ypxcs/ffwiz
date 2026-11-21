package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// The golden tests in this file cover the tasks plan.md §5 asks for beyond
// convert_video. Each expected command was checked against `ffmpeg -h` and then
// run for real by TestIntegrationRunGeneratedCommands, which skips when ffmpeg
// is not on the machine.

// buildTask resolves a task's answers and returns the command as the user sees
// it. The engine is built per call so one case cannot leak into the next.
func buildTask(t *testing.T, set *Set, taskID string, info *ffmpeg.MediaInfo, answers map[string]string) *ffmpeg.Command {
	t.Helper()
	task, ok := set.Get(taskID)
	if !ok {
		t.Fatalf("%s is missing from the built-in set", taskID)
	}
	eng := &Engine{Task: task, Info: info, Caps: machineCaps()}
	cmd, _, err := eng.Build(answers, BuildOptions{Input: "av.mp4"})
	if err != nil {
		t.Fatalf("Build(%s, %v): %v", taskID, answers, err)
	}
	return cmd
}

func TestCatalogueCoversThePlannedTasks(t *testing.T) {
	set := loadBuiltin(t)

	// plan.md §5 lists eleven tasks. The ones still missing are recorded in
	// DECISIONS.md as outstanding, so this test states which those are rather
	// than quietly passing.
	have := map[string]bool{}
	for _, id := range set.IDs() {
		have[id] = true
	}
	for _, id := range []string{
		"convert_video", "compress_video", "extract_audio", "trim", "resize",
		"change_speed", "strip_metadata", "extract_frames", "gif",
	} {
		if !have[id] {
			t.Errorf("%s is missing from the catalogue (plan.md §5)", id)
		}
	}
}

func TestCompressVideoCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		info    *ffmpeg.MediaInfo
		answers map[string]string
		want    string
	}{
		{
			name: "defaults: a CRF encode with the audio left alone",
			want: "ffmpeg -n -i av.mp4 -c:v libx264 -crf 26 -c:a copy av_compress_video.mp4",
		},
		{
			name:    "H.265 with the audio re-encoded, in an MKV",
			answers: map[string]string{"codec": "h265", "audio": "mp3", "container": "mkv"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx265 -crf 26 -c:a libmp3lame -b:a 192k av_compress_video.mkv",
		},
		{
			// 10 MiB over two seconds, minus 3% muxing reserve, minus the
			// 128 kbit/s the copied audio stream costs: 39603 kbit/s
			// (DECISIONS D8).
			name:    "a target size computes the video bitrate",
			answers: map[string]string{"method": "size", "size": "10"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx264 -c:a copy -b:v 39603k -maxrate 39603k -bufsize 79206k av_compress_video.mp4",
		},
		{
			name:    "a target size with re-encoded audio subtracts its bitrate",
			answers: map[string]string{"method": "size", "size": "25", "audio": "aac128"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx264 -c:a aac -b:a 128k -b:v 99200k -maxrate 99200k -bufsize 198400k av_compress_video.mp4",
		},
		{
			name: "no audio stream means no audio question and no -c:a",
			info: videoWithoutAudio(),
			want: "ffmpeg -n -i av.mp4 -c:v libx264 -crf 26 av_compress_video.mp4",
		},
		{
			// videoWithoutAudio is one second long, so the same 25 MiB target
			// is a much higher bitrate - and nothing is subtracted for audio.
			name:    "a target size on a file with no audio",
			info:    videoWithoutAudio(),
			answers: map[string]string{"method": "size", "size": "25"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx264 -b:v 198528k -maxrate 198528k -bufsize 397056k av_compress_video.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info
			if info == nil {
				info = videoWithAudio()
			}
			cmd := buildTask(t, set, "compress_video", info, tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestExtractAudioCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			name: "defaults to MP3 at 192 kbit/s",
			want: "ffmpeg -n -i av.mp4 -vn -c:a libmp3lame -b:a 192k av_extract_audio.mp3",
		},
		{
			name:    "M4A with a chosen bitrate",
			answers: map[string]string{"format": "m4a", "bitrate": "128"},
			want:    "ffmpeg -n -i av.mp4 -vn -c:a aac -b:a 128k av_extract_audio.m4a",
		},
		{
			// Lossless formats have no bitrate to choose, so the step is not
			// asked and contributes nothing (plan.md §5.3).
			name:    "FLAC is not given a bitrate",
			answers: map[string]string{"format": "flac", "bitrate": "320"},
			want:    "ffmpeg -n -i av.mp4 -vn -c:a flac av_extract_audio.flac",
		},
		{
			name:    "WAV is uncompressed",
			answers: map[string]string{"format": "wav"},
			want:    "ffmpeg -n -i av.mp4 -vn -c:a pcm_s16le av_extract_audio.wav",
		},
		{
			name:    "Opus",
			answers: map[string]string{"format": "opus", "bitrate": "320"},
			want:    "ffmpeg -n -i av.mp4 -vn -c:a libopus -b:a 320k av_extract_audio.opus",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildTask(t, set, "extract_audio", videoWithAudio(), tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestTrimCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			// The seek is an input option and the duration is an output one:
			// -t is measured from where the seek landed (DECISIONS D6, D19).
			name:    "the accurate default re-encodes from the seek point",
			answers: map[string]string{"range": "00:00:00.500,00:00:01.500"},
			want:    "ffmpeg -n -ss 00:00:00.500 -i av.mp4 -t 00:00:01.000 -c:v libx264 -crf 20 -c:a aac -b:a 192k av_trim.mp4",
		},
		{
			name:    "fast copies the streams and writes nothing else",
			answers: map[string]string{"range": "00:00:00.500,00:00:01.500", "mode": "fast"},
			want:    "ffmpeg -n -ss 00:00:00.500 -i av.mp4 -t 00:00:01.000 -c copy av_trim.mp4",
		},
		{
			name:    "plain seconds are accepted",
			answers: map[string]string{"range": "1,2", "mode": "fast"},
			want:    "ffmpeg -n -ss 00:00:01.000 -i av.mp4 -t 00:00:01.000 -c copy av_trim.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildTask(t, set, "trim", videoWithAudio(), tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestResizeCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		info    *ffmpeg.MediaInfo
		answers map[string]string
		want    string
	}{
		{
			// -2 lets the width follow the aspect ratio and rounds it to an
			// even number, which H.264 needs (plan.md §5.5).
			name: "720p by default, with the audio copied",
			want: "ffmpeg -n -i av.mp4 -vf scale=-2:720 -c:a copy av_resize.mp4",
		},
		{
			name:    "480p in an MKV",
			answers: map[string]string{"height": "480", "container": "mkv"},
			want:    "ffmpeg -n -i av.mp4 -vf scale=-2:480 -c:a copy av_resize.mkv",
		},
		{
			name:    "a height that is not a preset is allowed",
			answers: map[string]string{"height": "1000"},
			want:    "ffmpeg -n -i av.mp4 -vf scale=-2:1000 -c:a copy av_resize.mp4",
		},
		{
			name: "a file with no audio is asked nothing about it",
			info: videoWithoutAudio(),
			want: "ffmpeg -n -i av.mp4 -vf scale=-2:720 av_resize.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info
			if info == nil {
				info = videoWithAudio()
			}
			cmd := buildTask(t, set, "resize", info, tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestResizeRejectsAHeightItCannotUse(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("resize")

	eng := &Engine{Task: task, Info: videoWithAudio(), Caps: machineCaps()}
	res := eng.Resolve(map[string]string{"height": "9000"})
	// A value outside the validate range is not an answer the engine can use,
	// so the default stands rather than a 9000-line video being produced.
	if got := res.Answers["height"]; got != "720" {
		t.Errorf("height = %q, want the default 720 for an out-of-range value", got)
	}
}

func TestChangeSpeedCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		info    *ffmpeg.MediaInfo
		answers map[string]string
		want    string
	}{
		{
			// One choice contributes to both chains: setpts retimes the video
			// and atempo the audio (plan.md §5.6).
			name: "2x plays faster, sound and picture together",
			want: "ffmpeg -n -i av.mp4 -vf setpts=PTS/2 -af atempo=2.0 -c:v libx264 -c:a aac -b:a 192k av_change_speed.mp4",
		},
		{
			// 0.25x needs atempo chained: the filter refuses factors outside
			// 0.5-2.0. The repeat is deliberate, so it must not be deduplicated.
			name:    "a quarter of the speed chains atempo",
			answers: map[string]string{"speed": "0.25"},
			want:    "ffmpeg -n -i av.mp4 -vf setpts=PTS/0.25 -af atempo=0.5,atempo=0.5 -c:v libx264 -c:a aac -b:a 192k av_change_speed.mp4",
		},
		{
			name:    "four times the speed chains atempo the other way",
			answers: map[string]string{"speed": "4", "container": "mkv"},
			want:    "ffmpeg -n -i av.mp4 -vf setpts=PTS/4 -af atempo=2.0,atempo=2.0 -c:v libx264 -c:a aac -b:a 192k av_change_speed.mkv",
		},
		{
			// Measured: -af on a file with no audio stream is accepted by
			// ffmpeg and simply has nothing to act on, so the chain stays.
			name: "a file with no audio keeps its audio chain",
			info: videoWithoutAudio(),
			want: "ffmpeg -n -i av.mp4 -vf setpts=PTS/2 -af atempo=2.0 -c:v libx264 -c:a aac -b:a 192k av_change_speed.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info
			if info == nil {
				info = videoWithAudio()
			}
			cmd := buildTask(t, set, "change_speed", info, tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// TestChangeSpeedShortensTheExpectedDuration: plan.md §6.7 has a progress bar
// sized by the expected output duration, and speeding a clip up is exactly the
// case where the input's length is the wrong number (DECISIONS D3).
func TestChangeSpeedShortensTheExpectedDuration(t *testing.T) {
	set := loadBuiltin(t)
	for _, tc := range []struct {
		speed string
		want  string
	}{
		{"2", "1s"},
		{"4", "500ms"},
		{"0.5", "4s"},
	} {
		cmd := buildTask(t, set, "change_speed", videoWithAudio(), map[string]string{"speed": tc.speed})
		if got := cmd.ExpectedDuration().String(); got != tc.want {
			t.Errorf("speed %sx: expected duration = %s, want %s", tc.speed, got, tc.want)
		}
	}
}

func TestStripMetadataCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			name: "copying the streams is the default",
			want: "ffmpeg -n -i av.mp4 -map_metadata -1 -c copy av_strip_metadata.mp4",
		},
		{
			name:    "re-encoding always works",
			answers: map[string]string{"mode": "reencode"},
			want:    "ffmpeg -n -i av.mp4 -map_metadata -1 -c:v libx264 -crf 20 -c:a aac -b:a 192k av_strip_metadata.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildTask(t, set, "strip_metadata", videoWithAudio(), tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// The palette recipe each GIF quality contributes. They are written out in the
// tests rather than imported, so a change to the YAML has to be seen here.
const (
	gifBalanced = "split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse"
	gifBest     = "split[s0][s1];[s0]palettegen=max_colors=256:stats_mode=diff[p];[s1][p]paletteuse=dither=sierra2_4a"
	gifSmall    = "split[s0][s1];[s0]palettegen=max_colors=128[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5"
)

func TestGIFCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			// The frame rate, the scale and the palette are three answers that
			// merge into one -vf chain in step order (plan.md §5.7, D37).
			name: "defaults to 10 fps at 320 px with a palette built from the clip",
			want: "ffmpeg -n -i av.mp4 -vf 'fps=10,scale=320:-1:flags=lanczos," + gifBalanced + "' av_gif.gif",
		},
		{
			name:    "a chosen width and palette",
			answers: map[string]string{"width": "480", "quality": "best"},
			want:    "ffmpeg -n -i av.mp4 -vf 'fps=10,scale=480:-1:flags=lanczos," + gifBest + "' av_gif.gif",
		},
		{
			// The seek is an input option and the length an output one, so
			// nothing is decoded before the seek point (DECISIONS D6, D19).
			name:    "a time range seeks before the input and limits the length after it",
			answers: map[string]string{"clip": "yes", "range": "00:00:00.500,00:00:01.500", "width": "640", "fps": "15", "quality": "best"},
			want:    "ffmpeg -n -ss 00:00:00.500 -i av.mp4 -vf 'fps=15,scale=640:-1:flags=lanczos," + gifBest + "' -t 00:00:01.000 av_gif.gif",
		},
		{
			// A GIF is written by the gif muxer, which carries no audio; the
			// answer for "keep the size" still has to keep the sides even.
			name:    "the original size is only rounded to even sides",
			answers: map[string]string{"width": "0", "quality": "small"},
			want:    "ffmpeg -n -i av.mp4 -vf 'fps=10,scale=trunc(iw/2)*2:trunc(ih/2)*2," + gifSmall + "' av_gif.gif",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildTask(t, set, "gif", videoWithAudio(), tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestExtractFramesCommands(t *testing.T) {
	set := loadBuiltin(t)
	tests := []struct {
		name    string
		answers map[string]string
		want    string
	}{
		{
			name: "a thumbnail at the default time",
			want: "ffmpeg -n -ss 00:00:01 -i av.mp4 -frames:v 1 av_extract_frames.png",
		},
		{
			name:    "a thumbnail at a chosen time, as JPEG",
			answers: map[string]string{"at": "00:00:01.500", "image": "jpg"},
			want:    "ffmpeg -n -ss 00:00:01.500 -i av.mp4 -frames:v 1 av_extract_frames.jpg",
		},
		{
			// The image2 muxer needs a numbered name; the pattern is in the
			// output extension so the derived name carries it (DECISIONS D34).
			name:    "a PNG sequence every five seconds",
			answers: map[string]string{"mode": "every", "every": "5"},
			want:    "ffmpeg -n -i av.mp4 -vf fps=1/5 av_extract_frames.%04d.png",
		},
		{
			name:    "a JPEG sequence every second",
			answers: map[string]string{"mode": "every", "every": "1", "image": "jpg_seq"},
			want:    "ffmpeg -n -i av.mp4 -vf fps=1 av_extract_frames.%04d.jpg",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildTask(t, set, "extract_frames", videoWithAudio(), tc.answers)
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// TestExtractFramesOutputNames: the sequence modes have to name a file image2
// will accept, which means a printf-style number in the extension.
func TestExtractFramesOutputNames(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("extract_frames")

	eng := &Engine{Task: task, Info: videoWithAudio(), Caps: machineCaps()}
	cmd, _, err := eng.Build(map[string]string{"mode": "every", "every": "2"}, BuildOptions{Input: "/films/My Movie.mp4"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := cmd.DisplayArgs()
	got := args[len(args)-1]
	if want := "/films/My Movie_extract_frames.%04d.png"; got != want {
		t.Errorf("output path = %q, want %q", got, want)
	}
	if !ffmpeg.IsSequence(got) {
		t.Errorf("%q is not recognised as a numbered sequence", got)
	}
}

// TestTasksHideOptionsTheBuildCannotDo: capability gating is a correctness
// requirement, not a nicety (DECISIONS D17). Without libmp3lame the MP3 answer
// would look like a working choice and then fail at the last moment.
func TestTasksHideOptionsTheBuildCannotDo(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("extract_audio")

	caps := machineCaps()
	delete(caps.Encoders, "libmp3lame")
	eng := &Engine{Task: task, Info: videoWithAudio(), Caps: caps}

	res := eng.Resolve(nil)
	var hidden []string
	for _, c := range res.HiddenChoices["format"] {
		hidden = append(hidden, c.Value)
	}
	if !contains(hidden, "mp3") {
		t.Errorf("mp3 is still offered without libmp3lame (hidden: %v)", hidden)
	}
	if got := res.Answers["format"]; got != "m4a" {
		t.Errorf("format = %q, want the next available one (m4a)", got)
	}
}

// TestEveryTaskCanBeBuiltFromItsDefaults: a task whose defaults cannot produce a
// command would fail with no answer the user could give. plan.md §4.7 asks for
// the invalid combinations to be caught at load time; this catches the ones that
// only show up once an input is in hand.
//
// A step the user has to fill in - a start time, say - is legitimately pending,
// so only a *choice* that nothing can answer is a failure here.
func TestEveryTaskCanBeBuiltFromItsDefaults(t *testing.T) {
	set := loadBuiltin(t)
	for _, task := range set.Tasks {
		if task.Disabled {
			continue
		}
		for _, info := range []*ffmpeg.MediaInfo{videoWithAudio(), videoWithoutAudio()} {
			if ok, _ := task.Applies(info); !ok {
				continue
			}
			eng := &Engine{Task: task, Info: info, Caps: machineCaps()}
			cmd, res, err := eng.Build(nil, BuildOptions{Input: "av.mp4"})
			if err != nil {
				t.Errorf("%s on %s: %v", task.ID, info.Path, err)
				continue
			}
			for _, step := range res.Active {
				if !res.Pending[step.ID] {
					continue
				}
				if step.Type == StepInput || step.Type == StepTimeRange {
					continue // the user is meant to type this
				}
				t.Errorf("%s on %s: %s (%s) has no answer and no default",
					task.ID, info.Path, step.ID, step.Type)
			}
			if strings.Contains(cmd.String(), "<") {
				t.Errorf("%s on %s produced a placeholder: %s", task.ID, info.Path, cmd.String())
			}
		}
	}
}

// TestTaskUsersCanOverride: a user's own copy of a task replaces the built-in
// one of the same id, which is what makes the built-ins safe to ship
// (DECISIONS D12).
func TestUserTaskCanOverrideABuiltin(t *testing.T) {
	dir := t.TempDir()
	writeUserTask(t, dir, "resize", `
- id: resize
  label: My own resize
  steps:
    - id: height
      prompt: Height
      type: resolution
      template: ["-vf", "scale=-2:{value}"]
      default: "540"
      choices:
        - { label: "540p", value: "540" }
`)
	set, err := Load(LoadOptions{UserDir: dir})
	if err != nil {
		t.Fatalf("loading with a user override: %v", err)
	}
	task, ok := set.Get("resize")
	if !ok {
		t.Fatal("resize disappeared from the set")
	}
	if task.Label != "My own resize" {
		t.Errorf("label = %q, want the user's own task", task.Label)
	}
	eng := &Engine{Task: task, Info: videoWithAudio(), Caps: machineCaps()}
	cmd, _, err := eng.Build(nil, BuildOptions{Input: "av.mp4"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := cmd.String(), "ffmpeg -n -i av.mp4 -vf scale=-2:540 -c:a copy av_resize.mp4"; got != want {
		t.Errorf("command =\n  %s\nwant\n  %s", got, want)
	}
}

// writeUserTask drops a YAML task file into a config directory.
func writeUserTask(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the user task: %v", err)
	}
}

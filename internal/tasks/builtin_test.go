package tasks

import (
	"strings"
	"testing"
	"time"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// loadBuiltin loads the real embedded catalogue. It is the test that keeps the
// shipped YAML valid, so a bad edit there fails here rather than in the TUI.
func loadBuiltin(t *testing.T) *Set {
	t.Helper()
	set, err := Load(LoadOptions{UserDir: t.TempDir()})
	if err != nil {
		t.Fatalf("loading the built-in tasks: %v", err)
	}
	return set
}

// machineCaps is the capability set of a typical Homebrew ffmpeg on macOS: it
// has libx264, libx265 and libvpx-vp9, but no libaom-av1, and no libass.
func machineCaps() *ffmpeg.Capabilities {
	return &ffmpeg.Capabilities{
		Encoders: map[string]string{
			"libx264": "x264", "libx265": "x265", "libvpx-vp9": "vp9",
			"aac": "AAC", "libopus": "Opus", "libmp3lame": "MP3", "flac": "FLAC",
		},
		Muxers:       map[string]string{"mp4": "MP4", "matroska": "MKV", "webm": "WebM"},
		PixelFormats: map[string]struct{}{"yuv420p": {}},
		Filters:      map[string]struct{}{"scale": {}, "palettegen": {}, "paletteuse": {}},
	}
}

// videoWithAudio is a two-second 320x240 H.264/AAC file, matching the generated
// av.mp4 fixture.
func videoWithAudio() *ffmpeg.MediaInfo {
	return &ffmpeg.MediaInfo{
		Path:   "av.mp4",
		Format: ffmpeg.FormatInfo{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 2 * time.Second, Size: 50685, BitRate: 203000},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamVideo, CodecName: "h264", Width: 320, Height: 240, PixelFormat: "yuv420p", FrameRate: 25},
			{Index: 1, Type: ffmpeg.StreamAudio, CodecName: "aac", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000},
		},
	}
}

// videoWithoutAudio is a one-second file with no audio stream, matching
// video_only.mp4.
func videoWithoutAudio() *ffmpeg.MediaInfo {
	return &ffmpeg.MediaInfo{
		Path:   "video_only.mp4",
		Format: ffmpeg.FormatInfo{Name: "mov,mp4", Duration: time.Second},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamVideo, CodecName: "h264", Width: 320, Height: 240, PixelFormat: "yuv420p", FrameRate: 25},
		},
	}
}

func TestBuiltinCatalogue(t *testing.T) {
	set := loadBuiltin(t)

	task, ok := set.Get("convert_video")
	if !ok {
		t.Fatalf("convert_video is missing from the built-in set: %v", set.IDs())
	}
	if task.Label == "" || task.Description == "" {
		t.Error("convert_video has no label or description to show the user")
	}
	if !task.Requires.Video {
		t.Error("convert_video does not require a video stream")
	}
	// M2 delivers this one task; the rest arrive in M5 (plan.md §10).
	if got := set.IDs(); len(got) != 1 {
		t.Logf("catalogue now holds %v", got)
	}
}

// TestConvertVideoCommands is the golden test for the shipped task. The
// expected commands were checked by hand against `ffmpeg -h` and then actually
// run on the sample media by TestIntegrationRunGeneratedCommands.
func TestConvertVideoCommands(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")

	tests := []struct {
		name    string
		info    *ffmpeg.MediaInfo
		answers map[string]string
		want    string
	}{
		{
			name: "defaults: MP4 with H.264 and copied audio",
			info: videoWithAudio(),
			want: "ffmpeg -n -i av.mp4 -c:v libx264 -crf 23 -c:a copy av_out.mp4",
		},
		{
			name:    "high quality",
			info:    videoWithAudio(),
			answers: map[string]string{"quality_h264": "high"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx264 -crf 18 -c:a copy av_out.mp4",
		},
		{
			name:    "MKV with H.265 re-encoding the audio",
			info:    videoWithAudio(),
			answers: map[string]string{"container": "mkv", "vcodec": "h265", "audio": "aac"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx265 -crf 28 -c:a aac -b:a 192k av_out.mkv",
		},
		{
			name:    "WebM switches to VP9 and Opus without being asked",
			info:    videoWithAudio(),
			answers: map[string]string{"container": "webm"},
			want:    "ffmpeg -n -i av.mp4 -c:v libvpx-vp9 -b:v 0 -crf 32 -c:a libopus -b:a 128k av_out.webm",
		},
		{
			name:    "stream copy keeps everything and skips quality",
			info:    videoWithAudio(),
			answers: map[string]string{"vcodec": "copy"},
			want:    "ffmpeg -n -i av.mp4 -c:v copy -c:a copy av_out.mp4",
		},
		{
			name:    "VP9 in an MKV, chosen explicitly",
			info:    videoWithAudio(),
			answers: map[string]string{"vcodec": "vp9", "quality_vp9": "small", "audio": "aac"},
			want:    "ffmpeg -n -i av.mp4 -c:v libvpx-vp9 -b:v 0 -crf 40 -c:a aac -b:a 192k av_out.mp4",
		},
		{
			name:    "remove the audio track",
			info:    videoWithAudio(),
			answers: map[string]string{"audio": "none"},
			want:    "ffmpeg -n -i av.mp4 -c:v libx264 -crf 23 -an av_out.mp4",
		},
		{
			name: "a file with no audio stream asks nothing about audio",
			info: videoWithoutAudio(),
			want: "ffmpeg -n -i av.mp4 -c:v libx264 -crf 23 av_out.mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := &Engine{Task: task, Info: tc.info, Caps: machineCaps()}
			cmd, _, err := eng.Build(tc.answers, BuildOptions{Input: "av.mp4"})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := cmd.String(); got != tc.want {
				t.Errorf("command =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestConvertVideoHidesWhatTheBuildCannotDo(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")

	t.Run("no libx265", func(t *testing.T) {
		caps := machineCaps()
		delete(caps.Encoders, "libx265")
		eng := &Engine{Task: task, Info: videoWithAudio(), Caps: caps}

		res := eng.Resolve(nil)
		found := false
		for _, c := range res.HiddenChoices["vcodec"] {
			found = found || c.Value == "h265"
		}
		if !found {
			t.Errorf("h265 is still offered without the libx265 encoder: %v", res.HiddenChoices["vcodec"])
		}
		if got := res.Answers["vcodec"]; got != "h264" {
			t.Errorf("vcodec = %q, want the fallback to h264", got)
		}
	})

	t.Run("no mp4 muxer", func(t *testing.T) {
		caps := machineCaps()
		delete(caps.Muxers, "mp4")
		eng := &Engine{Task: task, Info: videoWithAudio(), Caps: caps}

		res := eng.Resolve(nil)
		if got := res.Answers["container"]; got == "mp4" {
			t.Error("mp4 is still the default container without the mp4 muxer")
		}
		if got := res.Answers["container"]; got != "mkv" {
			t.Errorf("container = %q, want the next available one (mkv)", got)
		}
	})

	t.Run("no VP9 encoder in WebM", func(t *testing.T) {
		caps := machineCaps()
		delete(caps.Encoders, "libvpx-vp9")
		eng := &Engine{Task: task, Info: videoWithAudio(), Caps: caps}

		res := eng.Resolve(map[string]string{"container": "webm"})
		// Every video codec is unfindable in WebM, so the step drops out
		// entirely rather than offering a command that cannot work.
		if _, answered := res.Answers["vcodec"]; answered {
			t.Errorf("vcodec = %q, want the step to be dropped", res.Answers["vcodec"])
		}
		if reason := res.Skipped["vcodec"]; !strings.Contains(reason, "no option") {
			t.Errorf("vcodec skip reason = %q", reason)
		}
	})
}

func TestConvertVideoOutputNames(t *testing.T) {
	set := loadBuiltin(t)
	task, _ := set.Get("convert_video")

	for _, tc := range []struct {
		container string
		input     string
		want      string
	}{
		{"mp4", "clip.mkv", "clip_out.mp4"},
		{"mkv", "/films/My Movie (2019).mp4", "/films/My Movie (2019)_out.mkv"},
		{"webm", "ünïcodé-ähm.mp4", "ünïcodé-ähm_out.webm"},
	} {
		eng := &Engine{Task: task, Info: videoWithAudio(), Caps: machineCaps()}
		cmd, _, err := eng.Build(map[string]string{"container": tc.container}, BuildOptions{Input: tc.input})
		if err != nil {
			t.Fatalf("Build(%s): %v", tc.input, err)
		}
		args := cmd.DisplayArgs()
		if got := args[len(args)-1]; got != tc.want {
			t.Errorf("output path = %q, want %q", got, tc.want)
		}
	}
}

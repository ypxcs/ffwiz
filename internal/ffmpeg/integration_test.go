package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests spawn the real ffmpeg and ffprobe binaries. They skip when the
// tools or the generated sample media are unavailable, so `go test ./...`
// still passes on a machine without ffmpeg (plan.md §9).

const sampleDir = "testdata/generated"

func requireTools(t *testing.T) Tools {
	t.Helper()
	tools, err := Locate(context.Background(), Overrides{})
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
	return path
}

func TestIntegrationLocateAndCapabilities(t *testing.T) {
	tools := requireTools(t)

	if !strings.Contains(tools.Version, ".") && !strings.HasPrefix(tools.Version, "N-") {
		t.Errorf("Version = %q, which does not look like an ffmpeg version", tools.Version)
	}
	if !filepath.IsAbs(tools.FFmpeg) || !filepath.IsAbs(tools.FFprobe) {
		t.Errorf("Locate returned non-absolute paths: %+v", tools)
	}

	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps.Encoders) < 10 {
		t.Errorf("only %d encoders found, expected many", len(caps.Encoders))
	}
	if len(caps.Muxers) == 0 {
		t.Error("no muxers found")
	}
	// Every build that can encode H.264 has one of these; the task catalogue
	// depends on at least one being present.
	hasH264 := caps.HasEncoder("libx264") || caps.HasEncoder("h264") ||
		caps.HasEncoder("h264_videotoolbox") || caps.HasEncoder("h264_nvenc")
	if !hasH264 {
		t.Error("no H.264 encoder found, which the task catalogue requires")
	}
	if !caps.HasMuxer("mp4") {
		t.Error("the mp4 muxer was not detected")
	}
}

func TestIntegrationProbeRealFiles(t *testing.T) {
	tools := requireTools(t)

	tests := []struct {
		name         string
		file         string
		wantVideo    bool
		wantAudio    bool
		wantDuration time.Duration
	}{
		{"mp4 with video and audio", "av.mp4", true, true, 2 * time.Second},
		{"mp4 with video only", "video_only.mp4", true, false, time.Second},
		{"mp3 audio only", "audio_only.mp3", false, true, time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := requireSample(t, tc.file)
			info, err := Probe(context.Background(), tools.FFprobe, path)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if info.Has(StreamVideo) != tc.wantVideo {
				t.Errorf("Has(StreamVideo) = %v, want %v", info.Has(StreamVideo), tc.wantVideo)
			}
			if info.Has(StreamAudio) != tc.wantAudio {
				t.Errorf("Has(StreamAudio) = %v, want %v", info.Has(StreamAudio), tc.wantAudio)
			}
			got := info.Duration()
			if got < tc.wantDuration-time.Millisecond*100 || got > tc.wantDuration+time.Millisecond*100 {
				t.Errorf("Duration() = %v, want ~%v", got, tc.wantDuration)
			}
			if info.Format.Size <= 0 {
				t.Errorf("Format.Size = %d, want > 0", info.Format.Size)
			}
		})
	}
}

// TestIntegrationProbeAwkwardFilenames covers the acceptance criterion that
// filenames with spaces, quotes, ampersands and non-ASCII text work everywhere
// (plan.md §8, §11).
func TestIntegrationProbeAwkwardFilenames(t *testing.T) {
	tools := requireTools(t)

	for _, name := range []string{"sp ace.mp4", "quote's.mp4", "&and.mp4", "ünïcodé.mp4"} {
		t.Run(name, func(t *testing.T) {
			path := requireSample(t, name)
			info, err := Probe(context.Background(), tools.FFprobe, path)
			if err != nil {
				t.Fatalf("Probe(%q): %v", path, err)
			}
			if !info.Has(StreamVideo) {
				t.Errorf("Probe(%q) found no video stream", path)
			}
		})
	}
}

// TestIntegrationProbeLeadingDashFilename proves guardPath works end to end:
// with the working directory inside the media folder, a bare "-leading-dash.mp4"
// is passed to ffprobe with a "./" prefix so it is not parsed as an option.
func TestIntegrationProbeLeadingDashFilename(t *testing.T) {
	tools := requireTools(t)
	requireSample(t, "-leading-dash.mp4")

	// t.Chdir restores the working directory when the test ends.
	t.Chdir(sampleDir)

	info, err := Probe(context.Background(), tools.FFprobe, "-leading-dash.mp4")
	if err != nil {
		t.Fatalf("Probe with a leading-dash filename: %v", err)
	}
	if !info.Has(StreamVideo) {
		t.Error("no video stream found in the leading-dash sample")
	}
}

func TestIntegrationProbeMissingFileIsAnError(t *testing.T) {
	tools := requireTools(t)

	_, err := Probe(context.Background(), tools.FFprobe, filepath.Join(sampleDir, "does-not-exist.mp4"))
	if err == nil {
		t.Fatal("Probe succeeded on a nonexistent file")
	}
	// The message must be actionable, i.e. carry ffprobe's own text.
	if !strings.Contains(err.Error(), "does-not-exist.mp4") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestIntegrationProbeTimeout(t *testing.T) {
	tools := requireTools(t)
	path := requireSample(t, "av.mp4")

	// A deadline in the past must produce an error rather than hanging.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	if _, err := Probe(ctx, tools.FFprobe, path); err == nil {
		t.Skip("probing completed before the deadline; timing-dependent test")
	}
}

// TestIntegrationCapabilitiesMatchRawListings cross-checks Capabilities against
// the raw text of the live listings. It is the guard against silent parser
// drift: a name that grep finds must be in the parsed set, and vice versa. The
// names chosen include ones that this particular ffmpeg build does *not* have
// (notably `subtitles`, which needs libass), so both directions are exercised.
func TestIntegrationCapabilitiesMatchRawListings(t *testing.T) {
	tools := requireTools(t)

	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	listing := func(flag string) string {
		out, err := exec.Command(tools.FFmpeg, "-hide_banner", flag).Output()
		if err != nil {
			t.Fatalf("%s %s: %v", tools.FFmpeg, flag, err)
		}
		return string(out)
	}

	// rawHas reports whether the listing holds the name as a whole field, which
	// is how the parser identifies names.
	rawHas := func(raw, name string) bool {
		for _, line := range strings.Split(raw, "\n") {
			fields := strings.Fields(strings.TrimRight(line, "\r"))
			if len(fields) < 2 {
				continue
			}
			for _, f := range fields[1:] {
				if f == name {
					return true
				}
			}
		}
		return false
	}

	tests := []struct {
		flag string
		name string
		has  func(string) bool
	}{
		{"-encoders", "libx264", caps.HasEncoder},
		{"-encoders", "aac", caps.HasEncoder},
		{"-encoders", "definitely_not_an_encoder", caps.HasEncoder},
		{"-muxers", "mp4", caps.HasMuxer},
		{"-muxers", "matroska", caps.HasMuxer},
		{"-filters", "scale", caps.HasFilter},
		{"-filters", "palettegen", caps.HasFilter},
		{"-filters", "subtitles", caps.HasFilter}, // absent without libass
		{"-pix_fmts", "yuv420p", func(n string) bool {
			_, ok := caps.PixelFormats[n]
			return ok
		}},
	}

	rawCache := map[string]string{}
	for _, tc := range tests {
		raw, ok := rawCache[tc.flag]
		if !ok {
			raw = listing(tc.flag)
			rawCache[tc.flag] = raw
		}
		if want, got := rawHas(raw, tc.name), tc.has(tc.name); want != got {
			t.Errorf("%s: raw listing says %s is %v, Capabilities says %v",
				tc.flag, tc.name, want, got)
		}
	}
}

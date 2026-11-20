package wizard

import (
	"strings"
	"testing"
	"time"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// TestMediaSummaryNilIsSafe is the regression test for a nil dereference found
// by running the M1 build on a real pty: View is called before the probe command
// returns, so an unfinished probe must render rather than panic (DECISIONS D18).
func TestMediaSummaryNilIsSafe(t *testing.T) {
	got, err := mediaSummary(nil)
	if err == nil {
		t.Fatal("mediaSummary(nil) succeeded, want an error")
	}
	if got != "" {
		t.Errorf("mediaSummary(nil) = %q, want an empty string", got)
	}

	// A probe result with nothing in it must render too: ffprobe can return a
	// format and no streams at all.
	empty, err := mediaSummary(&ffmpeg.MediaInfo{})
	if err != nil {
		t.Fatalf("mediaSummary of an empty result: %v", err)
	}
	if !strings.Contains(empty, "container") {
		t.Errorf("mediaSummary of an empty result = %q", empty)
	}
}

func TestMediaSummaryDescribesEveryStream(t *testing.T) {
	info := &ffmpeg.MediaInfo{
		Path:   "av.mp4",
		Format: ffmpeg.FormatInfo{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 2 * time.Second, Size: 50685, BitRate: 203000},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamVideo, CodecName: "h264", Width: 320, Height: 240, PixelFormat: "yuv420p", FrameRate: 25},
			{Index: 1, Type: ffmpeg.StreamAudio, CodecName: "aac", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000, Language: "eng"},
			{Index: 2, Type: ffmpeg.StreamSubtitle, CodecName: "srt", Language: "spa"},
		},
	}
	got, err := mediaSummary(info)
	if err != nil {
		t.Fatalf("mediaSummary: %v", err)
	}
	for _, want := range []string{
		"container  mov,mp4,m4a,3gp,3g2,mj2",
		"duration   2s",
		"size       49.5 KiB",
		"bitrate    203 kbit/s",
		"video #0   h264 320x240 yuv420p 25 fps",
		"audio #1   aac stereo 48000 Hz",
		"language eng",
		"subs  #2   srt spa",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mediaSummary output does not contain %q:\n%s", want, got)
		}
	}
}

func TestMediaSummaryHandlesMissingFields(t *testing.T) {
	// A stream with no pixel format, no frame rate and no channel layout must
	// still produce a readable line rather than a row of blanks.
	info := &ffmpeg.MediaInfo{
		Streams: []ffmpeg.StreamInfo{
			{Type: ffmpeg.StreamVideo, CodecName: "h264"},
			// ffprobe reports "und" when it cannot tell, which must not be shown
			// as if the file had been tagged with it.
			{Type: ffmpeg.StreamAudio, CodecName: "aac", Channels: 1, Language: "und"},
		},
	}
	got, err := mediaSummary(info)
	if err != nil {
		t.Fatalf("mediaSummary: %v", err)
	}
	if !strings.Contains(got, "(unknown)") {
		t.Errorf("mediaSummary did not mark the missing values:\n%s", got)
	}
	if strings.Contains(got, "und") {
		t.Errorf("mediaSummary printed the undetermined language:\n%s", got)
	}
	if !strings.Contains(got, "1ch") {
		t.Errorf("mediaSummary did not fall back to the channel count:\n%s", got)
	}
	if !strings.Contains(got, "(unknown size)") {
		t.Errorf("mediaSummary rendered an unusable resolution:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("mediaSummary left a trailing newline:\n%q", got)
	}
}

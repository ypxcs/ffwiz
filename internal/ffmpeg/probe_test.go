package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

func TestParseProbeJSONMP4WithAudioAndVideo(t *testing.T) {
	info, err := ParseProbeJSON(readFixture(t, "av.mp4.json"))
	if err != nil {
		t.Fatalf("ParseProbeJSON: %v", err)
	}

	if got, want := info.Format.Name, "mov,mp4,m4a,3gp,3g2,mj2"; got != want {
		t.Errorf("Format.Name = %q, want %q", got, want)
	}
	if got := info.Format.Size; got <= 0 {
		t.Errorf("Format.Size = %d, want > 0", got)
	}
	if got := info.Duration(); got.Seconds() < 1.9 || got.Seconds() > 2.1 {
		t.Errorf("Duration() = %v, want ~2s", got)
	}

	if !info.Has(StreamVideo) || !info.Has(StreamAudio) {
		t.Fatalf("expected both video and audio, got %d streams", len(info.Streams))
	}
	if info.Has(StreamSubtitle) {
		t.Error("Has(StreamSubtitle) = true, want false")
	}

	v, ok := info.FirstStream(StreamVideo)
	if !ok {
		t.Fatal("no video stream")
	}
	if v.CodecName != "h264" {
		t.Errorf("video codec = %q, want h264", v.CodecName)
	}
	if v.Width != 320 || v.Height != 240 {
		t.Errorf("video size = %dx%d, want 320x240", v.Width, v.Height)
	}
	if v.PixelFormat != "yuv420p" {
		t.Errorf("pix_fmt = %q, want yuv420p", v.PixelFormat)
	}
	if v.FrameRate != 25 {
		t.Errorf("FrameRate = %v, want 25", v.FrameRate)
	}

	a, ok := info.FirstStream(StreamAudio)
	if !ok {
		t.Fatal("no audio stream")
	}
	if a.CodecName != "aac" {
		t.Errorf("audio codec = %q, want aac", a.CodecName)
	}
	if a.Channels != 2 {
		t.Errorf("channels = %d, want 2", a.Channels)
	}
	if a.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want 48000", a.SampleRate)
	}
	if a.ChannelLayout != "stereo" {
		t.Errorf("channel layout = %q, want stereo", a.ChannelLayout)
	}
}

func TestParseProbeJSONVideoOnly(t *testing.T) {
	info, err := ParseProbeJSON(readFixture(t, "video_only.mp4.json"))
	if err != nil {
		t.Fatalf("ParseProbeJSON: %v", err)
	}

	if !info.Has(StreamVideo) {
		t.Error("Has(StreamVideo) = false, want true")
	}
	// This is what makes the "Extract audio" task unsupported for this input,
	// so the flag must be reliable.
	if info.Has(StreamAudio) {
		t.Error("Has(StreamAudio) = true, want false")
	}
	// Stream durations are absent for this file's video stream; the container
	// duration must be filled in so per-stream maths works.
	v, _ := info.FirstStream(StreamVideo)
	if v.Duration == 0 {
		t.Error("video stream duration = 0, want the container duration")
	}
}

func TestParseProbeJSONAudioOnly(t *testing.T) {
	info, err := ParseProbeJSON(readFixture(t, "audio_only.mp3.json"))
	if err != nil {
		t.Fatalf("ParseProbeJSON: %v", err)
	}

	if info.Has(StreamVideo) {
		t.Error("Has(StreamVideo) = true, want false")
	}
	a, ok := info.FirstStream(StreamAudio)
	if !ok {
		t.Fatal("no audio stream")
	}
	if a.SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", a.SampleRate)
	}
	if a.Channels != 1 {
		t.Errorf("channels = %d, want 1", a.Channels)
	}
	// "0/0" is what ffprobe reports for audio-only streams; it must not be
	// mistaken for a real frame rate.
	if a.FrameRate != 0 {
		t.Errorf("FrameRate = %v, want 0 for an audio stream", a.FrameRate)
	}
}

func TestParseProbeJSONEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		wantStream int
		check      func(t *testing.T, info *MediaInfo)
	}{
		{
			name: "empty result is not an error",
			json: `{"streams":[],"format":{}}`,
			check: func(t *testing.T, info *MediaInfo) {
				if info.Duration() != 0 {
					t.Errorf("Duration() = %v, want 0", info.Duration())
				}
				if info.Has(StreamVideo) {
					t.Error("Has(StreamVideo) = true for an empty result")
				}
			},
		},
		{
			name: "missing duration falls back to the longest stream",
			json: `{"format":{"format_name":"rawvideo"},
			       "streams":[{"index":0,"codec_type":"video","codec_name":"rawvideo","duration":"3.5"}]}`,
			wantStream: 1,
			check: func(t *testing.T, info *MediaInfo) {
				if got := info.Duration(); got.Seconds() != 3.5 {
					t.Errorf("Duration() = %v, want 3.5s", got)
				}
			},
		},
		{
			name:       "N/A duration is treated as unknown",
			json:       `{"format":{"duration":"N/A"},"streams":[{"index":0,"codec_type":"video"}]}`,
			wantStream: 1,
			check: func(t *testing.T, info *MediaInfo) {
				if info.Duration() != 0 {
					t.Errorf("Duration() = %v, want 0", info.Duration())
				}
			},
		},
		{
			name: "numbers and strings are accepted interchangeably",
			json: `{"format":{"duration":2.5,"bit_rate":128000,"size":"4096"},
			       "streams":[{"index":"0","codec_type":"audio","channels":"2","sample_rate":48000}]}`,
			wantStream: 1,
			check: func(t *testing.T, info *MediaInfo) {
				if got := info.Format.Duration.Seconds(); got != 2.5 {
					t.Errorf("Format.Duration = %v, want 2.5s", got)
				}
				if info.Format.BitRate != 128000 {
					t.Errorf("Format.BitRate = %d, want 128000", info.Format.BitRate)
				}
				if info.Format.Size != 4096 {
					t.Errorf("Format.Size = %d, want 4096", info.Format.Size)
				}
				a, _ := info.FirstStream(StreamAudio)
				if a.Channels != 2 {
					t.Errorf("channels = %d, want 2", a.Channels)
				}
				if a.SampleRate != 48000 {
					t.Errorf("sample rate = %d, want 48000", a.SampleRate)
				}
			},
		},
		{
			name:       "non-string tag values do not break decoding",
			json:       `{"format":{"tags":{"track":7,"title":"ok"}},"streams":[{"index":0,"codec_type":"video"}]}`,
			wantStream: 1,
			check: func(t *testing.T, info *MediaInfo) {
				if got := info.Format.Tags["track"]; got != "7" {
					t.Errorf("tag track = %q, want \"7\"", got)
				}
				if got := info.Format.Tags["title"]; got != "ok" {
					t.Errorf("tag title = %q, want \"ok\"", got)
				}
			},
		},
		{
			name:       "unknown stream types are preserved as unknown",
			json:       `{"streams":[{"index":0,"codec_type":"mystery"},{"index":1,"codec_type":"subtitle","tags":{"language":"eng"}}]}`,
			wantStream: 2,
			check: func(t *testing.T, info *MediaInfo) {
				if info.Streams[0].Type != StreamUnknown {
					t.Errorf("type = %q, want %q", info.Streams[0].Type, StreamUnknown)
				}
				if got := info.StreamsOf(StreamSubtitle); len(got) != 1 || got[0].Language != "eng" {
					t.Errorf("subtitle streams = %+v, want one English stream", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseProbeJSON([]byte(tc.json))
			if err != nil {
				t.Fatalf("ParseProbeJSON: %v", err)
			}
			if tc.wantStream != 0 && len(info.Streams) != tc.wantStream {
				t.Errorf("got %d streams, want %d", len(info.Streams), tc.wantStream)
			}
			if tc.check != nil {
				tc.check(t, info)
			}
		})
	}
}

func TestParseProbeJSONRejectsGarbage(t *testing.T) {
	if _, err := ParseProbeJSON([]byte("this is not json")); err == nil {
		t.Fatal("ParseProbeJSON accepted invalid JSON")
	}
	if _, err := ParseProbeJSON(nil); err == nil {
		t.Fatal("ParseProbeJSON accepted empty input")
	}
}

func TestParseRatio(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"25/1", 25},
		{"30000/1001", 29.97002997002997},
		{"24000/1001", 23.976023976023978},
		{"0/0", 0},
		{"", 0},
		{"N/A", 0},
		{"nonsense", 0},
		{"25", 25},
		{"1/0", 0},
	}
	for _, tc := range tests {
		got := parseRatio(tc.in)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("parseRatio(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFlexInt64AndDuration(t *testing.T) {
	tests := []struct {
		in       flex
		wantInt  int64
		wantSecs float64
	}{
		{"", 0, 0},
		{"null", 0, 0},
		{"123", 123, 123},
		{"2.021000", 2, 2.021},
		{"N/A", 0, 0},
		{"abc", 0, 0},
		{"-1", -1, 0}, // negative durations are clamped to unknown
	}
	for _, tc := range tests {
		if got := tc.in.Int64(); got != tc.wantInt {
			t.Errorf("flex(%q).Int64() = %d, want %d", tc.in, got, tc.wantInt)
		}
		got := tc.in.Duration().Seconds()
		if diff := got - tc.wantSecs; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("flex(%q).Duration() = %v, want %v", tc.in, got, tc.wantSecs)
		}
	}
}

func TestLastFrameTime(t *testing.T) {
	tests := []struct {
		name string
		info *MediaInfo
		want time.Duration
	}{
		{
			name: "a nil probe has no timeline",
			info: nil,
			want: 0,
		},
		{
			// Two seconds at 25 fps: the last frame starts at 1.96s, and a seek
			// to 1.999s already writes nothing (measured, DECISIONS D36).
			name: "one frame is taken off the end",
			info: &MediaInfo{
				Format:  FormatInfo{Duration: 2 * time.Second},
				Streams: []StreamInfo{{Type: StreamVideo, FrameRate: 25}},
			},
			want: 1960 * time.Millisecond,
		},
		{
			name: "an unknown frame rate backs off a millisecond",
			info: &MediaInfo{
				Format:  FormatInfo{Duration: 2 * time.Second},
				Streams: []StreamInfo{{Type: StreamVideo}},
			},
			want: 1999 * time.Millisecond,
		},
		{
			name: "audio only backs off a millisecond",
			info: &MediaInfo{
				Format:  FormatInfo{Duration: 3 * time.Second},
				Streams: []StreamInfo{{Type: StreamAudio}},
			},
			want: 2999 * time.Millisecond,
		},
		{
			name: "no duration at all is unknown",
			info: &MediaInfo{
				Streams: []StreamInfo{{Type: StreamVideo, FrameRate: 25}},
			},
			want: 0,
		},
		{
			// A single-frame image: the frame rate is meaningless, so the limit
			// stays at the duration rather than going negative.
			name: "a clip shorter than one frame",
			info: &MediaInfo{
				Format:  FormatInfo{Duration: 10 * time.Millisecond},
				Streams: []StreamInfo{{Type: StreamVideo, FrameRate: 25}},
			},
			want: 9 * time.Millisecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.LastFrameTime(); got != tc.want {
				t.Errorf("LastFrameTime() = %s, want %s", got, tc.want)
			}
		})
	}
}

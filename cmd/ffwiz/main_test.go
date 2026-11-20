package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

var testTools = ffmpeg.Tools{
	FFmpeg:  "/usr/bin/ffmpeg",
	FFprobe: "/usr/bin/ffprobe",
	Version: "8.1.1",
}

// TestSmokeModelViewStates is the regression test for a nil dereference found by
// running the M1 build on a real pty: View() is called before the probe command
// returns, so an unfinished probe reached summarize(nil) and panicked.
func TestSmokeModelViewStates(t *testing.T) {
	info := &ffmpeg.MediaInfo{
		Path:   "av.mp4",
		Format: ffmpeg.FormatInfo{Name: "mov,mp4", Duration: 2 * time.Second, Size: 50685, BitRate: 203000},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamVideo, CodecName: "h264", Width: 320, Height: 240, PixelFormat: "yuv420p", FrameRate: 25},
			{Index: 1, Type: ffmpeg.StreamAudio, CodecName: "aac", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000},
			{Index: 2, Type: ffmpeg.StreamSubtitle, CodecName: "srt", Language: "eng"},
		},
	}

	tests := []struct {
		name     string
		model    smokeModel
		wantSubs []string
	}{
		{
			name:     "no input file",
			model:    newSmokeModel(testTools, ""),
			wantSubs: []string{"ffwiz", "Pass a media file"},
		},
		{
			name:     "probe still running",
			model:    newSmokeModel(testTools, "av.mp4"),
			wantSubs: []string{"probing av.mp4"},
		},
		{
			name: "probe failed",
			model: func() smokeModel {
				m := newSmokeModel(testTools, "av.mp4")
				m.err = errors.New("boom")
				return m
			}(),
			wantSubs: []string{"probe failed", "boom"},
		},
		{
			name: "probe succeeded",
			model: func() smokeModel {
				m := newSmokeModel(testTools, "av.mp4")
				m.info = info
				return m
			}(),
			wantSubs: []string{"container", "mov,mp4", "duration", "video #0", "h264 320x240", "audio #1", "aac stereo", "subs  #2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The panic in the M1 build happened here.
			content := tc.model.View().Content
			for _, want := range tc.wantSubs {
				if !strings.Contains(content, want) {
					t.Errorf("View() output does not contain %q:\n%s", want, content)
				}
			}
			if !strings.Contains(content, "q quit") {
				t.Errorf("View() output is missing the key hint:\n%s", content)
			}
		})
	}
}

// keyPress builds the message Bubble Tea's input parser produces for a key
// keystroke, so the tests exercise the same strings Update matches on. See the
// v2 upgrade guide: "esc", "ctrl+c" and "space" all have special spellings.
func keyPress(keystroke string) tea.KeyPressMsg {
	switch keystroke {
	case "q":
		return tea.KeyPressMsg{Code: 'q', Text: "q"}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "x":
		return tea.KeyPressMsg{Code: 'x', Text: "x"}
	default:
		panic("keyPress: unhandled keystroke " + keystroke)
	}
}

// TestSmokeModelQuitKeys checks that the quit keys are recognised. Commands are
// funcs and cannot be compared, so the assertion is that Update returns a
// command for the quit keys and none for anything else.
func TestSmokeModelQuitKeys(t *testing.T) {
	for _, keystroke := range []string{"q", "esc", "ctrl+c"} {
		t.Run(keystroke, func(t *testing.T) {
			m := newSmokeModel(testTools, "av.mp4")
			_, cmd := m.Update(keyPress(keystroke))
			if cmd == nil {
				t.Errorf("key %q did not produce a command", keystroke)
			}
		})
	}
}

func TestSmokeModelIgnoresOtherKeys(t *testing.T) {
	m := newSmokeModel(testTools, "av.mp4")
	if _, cmd := m.Update(keyPress("x")); cmd != nil {
		t.Error("an unrelated key produced a command")
	}
}

func TestSummarizeNilIsSafe(t *testing.T) {
	if got := summarize(nil); got != "" {
		t.Errorf("summarize(nil) = %q, want an empty string", got)
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "unknown"},
		{-1, "unknown"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{50685, "49.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	}
	for _, tc := range tests {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanBitRate(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{999, "999 bit/s"},
		{64000, "64 kbit/s"},
		{203000, "203 kbit/s"},
		{5_000_000, "5.0 Mbit/s"},
	}
	for _, tc := range tests {
		if got := humanBitRate(tc.in); got != tc.want {
			t.Errorf("humanBitRate(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRoundDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "unknown"},
		{-time.Second, "unknown"},
		{2 * time.Second, "2s"},
		{1500 * time.Millisecond, "1.5s"},
		{250 * time.Microsecond, "<1ms"},
	}
	for _, tc := range tests {
		if got := roundDuration(tc.in); got != tc.want {
			t.Errorf("roundDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

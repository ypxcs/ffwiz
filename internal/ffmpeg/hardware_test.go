package ffmpeg

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// capsFromListing builds a Capabilities from a recorded `-encoders` listing, so
// the hardware tests use real ffmpeg output rather than a hand-written map.
func capsFromListing(t *testing.T, file string) Capabilities {
	t.Helper()
	out, err := os.ReadFile(filepath.Join("testdata", "generated", file))
	if err != nil {
		t.Skipf("no recorded listing: %v", err)
	}
	caps := Capabilities{Encoders: map[string]string{}}
	for _, entry := range parseListing(out) {
		caps.Encoders[entry.name] = entry.desc
	}
	return caps
}

func TestIsHardwareEncoder(t *testing.T) {
	hardware := []string{
		"h264_nvenc", "hevc_nvenc", "h264_videotoolbox", "hevc_videotoolbox",
		"h264_vaapi", "h264_qsv", "h264_amf", "h264_mf", "h264_v4l2m2m",
	}
	for _, name := range hardware {
		if !IsHardwareEncoder(name) {
			t.Errorf("IsHardwareEncoder(%q) = false, want true", name)
		}
		if HardwareAPI(name) == "" {
			t.Errorf("HardwareAPI(%q) is empty", name)
		}
	}
	software := []string{"libx264", "libx265", "libvpx-vp9", "aac", "libopus", ""}
	for _, name := range software {
		if IsHardwareEncoder(name) {
			t.Errorf("IsHardwareEncoder(%q) = true, want false", name)
		}
	}
}

func TestHardwareLabelNamesTheCodecAndAPI(t *testing.T) {
	tests := map[string]string{
		"h264_nvenc":        "hardware H.264 (NVENC)",
		"hevc_videotoolbox": "hardware H.265 (VideoToolbox)",
		"av1_qsv":           "hardware AV1 (Quick Sync)",
		"libx264":           "",
	}
	for name, want := range tests {
		if got := HardwareLabel(name); got != want {
			t.Errorf("HardwareLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestHardwareEncodersFromRealListing is the test that would catch a table that
// has gone stale: the names have to exist in a real ffmpeg build's listing.
func TestHardwareEncodersFromRealListing(t *testing.T) {
	caps := capsFromListing(t, "listing-encoders.txt")
	got := caps.HardwareEncoders()
	if len(got) == 0 {
		t.Fatal("no hardware encoders found in a real ffmpeg listing")
	}
	for _, name := range got {
		if !IsHardwareEncoder(name) {
			t.Errorf("%q was reported as hardware but is not in the table", name)
		}
		if caps.Encoders[name] == "" {
			t.Errorf("%q has no description from the listing", name)
		}
	}
	if !slices.IsSortedFunc(got, func(a, b string) int {
		ha, hb := hardwareEncoders[a], hardwareEncoders[b]
		if ha.Codec != hb.Codec {
			return strings.Compare(ha.Codec, hb.Codec)
		}
		return ha.Rank - hb.Rank
	}) {
		t.Errorf("HardwareEncoders is not ordered by codec and preference: %v", got)
	}
}

func TestPreferredHardwareEncoderPrefersThePlatformsOwnAPI(t *testing.T) {
	// A build that lists both a VideoToolbox and an NVENC encoder, as a Mac
	// with an eGPU could.
	caps := Capabilities{Encoders: map[string]string{
		"libx264":           "",
		"h264_videotoolbox": "",
		"h264_nvenc":        "",
		"hevc_nvenc":        "",
	}}

	if got := caps.PreferredHardwareEncoder("h264", "darwin"); got != "h264_videotoolbox" {
		t.Errorf("on darwin h264 = %q, want the VideoToolbox encoder", got)
	}
	// VideoToolbox does not exist on Linux, so NVENC wins there even before the
	// platform penalty is considered.
	if got := caps.PreferredHardwareEncoder("h264", "linux"); got != "h264_nvenc" {
		t.Errorf("on linux h264 = %q, want NVENC", got)
	}
	if got := caps.PreferredHardwareEncoder("hevc", "linux"); got != "hevc_nvenc" {
		t.Errorf("on linux hevc = %q, want NVENC", got)
	}
	// No hardware encoder for VP9 in this build.
	if got := caps.PreferredHardwareEncoder("vp9", "linux"); got != "" {
		t.Errorf("vp9 = %q, want none", got)
	}
	// A software-only build offers nothing.
	bare := Capabilities{Encoders: map[string]string{"libx264": ""}}
	if got := bare.PreferredHardwareEncoder("h264", "darwin"); got != "" {
		t.Errorf("a software-only build offered %q", got)
	}
}

// TestPreferredHardwareEncoderIgnoresOtherPlatforms: a VA-API encoder is listed
// by a build on macOS too, where it can never work.
func TestPreferredHardwareEncoderIgnoresOtherPlatforms(t *testing.T) {
	caps := Capabilities{Encoders: map[string]string{"h264_vaapi": ""}}
	if got := caps.PreferredHardwareEncoder("h264", "darwin"); got != "" {
		t.Errorf("a Linux-only encoder was preferred on darwin: %q", got)
	}
	if got := caps.PreferredHardwareEncoder("h264", "linux"); got != "h264_vaapi" {
		t.Errorf("on linux h264 = %q, want the VA-API encoder", got)
	}
}

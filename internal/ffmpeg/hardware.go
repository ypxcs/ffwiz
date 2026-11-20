package ffmpeg

import (
	"sort"
	"strings"
)

// Hardware-accelerated encoders (plan.md §7, "Detection").
//
// Being listed by `ffmpeg -encoders` does not mean the encoder will work: the
// device driver, the GPU and the ffmpeg build all have to agree, and a build
// compiled with CUDA support still fails on a machine with no Nvidia card.
// Detection therefore only decides whether an option may be *offered*; a failure
// at run time is explained rather than prevented (see hintFor).

// hardwareEncoder describes one known hardware encoder.
type hardwareEncoder struct {
	// Codec is the codec it encodes: "h264", "hevc", "av1" and so on.
	Codec string
	// API names the acceleration interface, e.g. "VideoToolbox".
	API string
	// Rank orders the encoders for the same codec; a lower number is preferred.
	// The order is by how likely the encoder is to be the one this user's
	// hardware wants: the platform's own API first, then the discrete GPU ones.
	Rank int
	// Platform marks encoders that only ever exist on one OS, so that a
	// preference list can put the running platform's first. Empty means any.
	Platform string
}

// hardwareEncoders is the set ffwiz knows how to label. A name that is absent
// here is still usable if ffmpeg provides it; it simply cannot be labelled or
// ordered as hardware.
var hardwareEncoders = map[string]hardwareEncoder{
	// macOS / iOS
	"h264_videotoolbox":   {Codec: "h264", API: "VideoToolbox", Rank: 1, Platform: "darwin"},
	"hevc_videotoolbox":   {Codec: "hevc", API: "VideoToolbox", Rank: 1, Platform: "darwin"},
	"prores_videotoolbox": {Codec: "prores", API: "VideoToolbox", Rank: 1, Platform: "darwin"},

	// Nvidia (NVENC)
	"h264_nvenc": {Codec: "h264", API: "NVENC", Rank: 2},
	"hevc_nvenc": {Codec: "hevc", API: "NVENC", Rank: 2},
	"av1_nvenc":  {Codec: "av1", API: "NVENC", Rank: 2},

	// Intel Quick Sync
	"h264_qsv": {Codec: "h264", API: "Quick Sync", Rank: 3},
	"hevc_qsv": {Codec: "hevc", API: "Quick Sync", Rank: 3},
	"av1_qsv":  {Codec: "av1", API: "Quick Sync", Rank: 3},
	"vp9_qsv":  {Codec: "vp9", API: "Quick Sync", Rank: 3},

	// Intel/AMD on Linux (VA-API)
	"h264_vaapi": {Codec: "h264", API: "VA-API", Rank: 4, Platform: "linux"},
	"hevc_vaapi": {Codec: "hevc", API: "VA-API", Rank: 4, Platform: "linux"},
	"av1_vaapi":  {Codec: "av1", API: "VA-API", Rank: 4, Platform: "linux"},
	"vp9_vaapi":  {Codec: "vp9", API: "VA-API", Rank: 4, Platform: "linux"},
	"vp8_vaapi":  {Codec: "vp8", API: "VA-API", Rank: 4, Platform: "linux"},

	// AMD (AMF)
	"h264_amf": {Codec: "h264", API: "AMF", Rank: 5},
	"hevc_amf": {Codec: "hevc", API: "AMF", Rank: 5},
	"av1_amf":  {Codec: "av1", API: "AMF", Rank: 5},

	// Windows Media Foundation
	"h264_mf": {Codec: "h264", API: "Media Foundation", Rank: 6, Platform: "windows"},
	"hevc_mf": {Codec: "hevc", API: "Media Foundation", Rank: 6, Platform: "windows"},
	"av1_mf":  {Codec: "av1", API: "Media Foundation", Rank: 6, Platform: "windows"},

	// Single-board computers (Raspberry Pi and friends)
	"h264_v4l2m2m": {Codec: "h264", API: "V4L2", Rank: 7, Platform: "linux"},
	"hevc_v4l2m2m": {Codec: "hevc", API: "V4L2", Rank: 7, Platform: "linux"},
}

// IsHardwareEncoder reports whether the named encoder is a hardware one ffwiz
// knows about. Its purpose is labelling and ordering, not validity: an unknown
// name is not rejected anywhere.
func IsHardwareEncoder(name string) bool {
	_, ok := hardwareEncoders[name]
	return ok
}

// HardwareAPI names the acceleration interface behind an encoder, e.g. "NVENC",
// or "" when the encoder is not a known hardware one.
func HardwareAPI(name string) string {
	return hardwareEncoders[name].API
}

// HardwareEncoders returns the hardware encoders the build provides, ordered by
// codec and then by preference, so the display is stable across runs.
func (c Capabilities) HardwareEncoders() []string {
	var names []string
	for name := range c.Encoders {
		if IsHardwareEncoder(name) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := hardwareEncoders[names[i]], hardwareEncoders[names[j]]
		if a.Codec != b.Codec {
			return a.Codec < b.Codec
		}
		return a.Rank < b.Rank
	})
	return names
}

// PreferredHardwareEncoder returns the hardware encoder this build offers for a
// codec ("h264"), or "" when there is none. The platform's own API wins over a
// discrete GPU's, because that is the one the machine almost certainly has.
//
// An encoder that belongs to another platform is never returned: ffmpeg lists
// VA-API encoders on macOS too, where they can never open a device.
func (c Capabilities) PreferredHardwareEncoder(codec string, goos string) string {
	best, bestRank := "", 0
	for name := range c.Encoders {
		hw, ok := hardwareEncoders[name]
		if !ok || hw.Codec != codec {
			continue
		}
		if hw.Platform != "" && hw.Platform != goos {
			continue
		}
		rank := hw.Rank
		if hw.Platform == goos {
			// The platform's own API beats a generic one.
			rank -= 10
		}
		if best == "" || rank < bestRank || (rank == bestRank && name < best) {
			best, bestRank = name, rank
		}
	}
	return best
}

// HardwareLabel is a short human name for an encoder, e.g.
// "hardware H.264 (VideoToolbox)", or "" when it is not a known hardware one.
// It is what the wizard shows next to a hardware option so the user knows the
// encode will run on a chip rather than a CPU.
func HardwareLabel(name string) string {
	hw, ok := hardwareEncoders[name]
	if !ok {
		return ""
	}
	codec := strings.ToUpper(hw.Codec)
	if hw.Codec == "hevc" {
		codec = "H.265"
	}
	if hw.Codec == "h264" {
		codec = "H.264"
	}
	return "hardware " + codec + " (" + hw.API + ")"
}

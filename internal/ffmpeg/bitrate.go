package ffmpeg

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BitratePlan is the video bitrate that should make an encode land near a target
// size, along with the rate-control flags that keep it from drifting above it.
//
// plan.md §5.2 asks for "compress to a target size" and for the arithmetic to be
// shown to the user, so the arithmetic lives here, in a package with no TUI
// imports, where it can be unit-tested on its own (plan.md §9).
type BitratePlan struct {
	// VideoKbps is the average video bitrate to ask for.
	VideoKbps int
	// MaxKbps is the peak bitrate. Without it a single-pass average encode can
	// overshoot badly on a hard scene and miss the target entirely.
	MaxKbps int
	// BufsizeKbps is the rate-control buffer, conventionally twice the maxrate.
	BufsizeKbps int
	// TargetMiB and Duration are what the plan was computed from, kept so the UI
	// can show the math without re-deriving it (DECISIONS D8).
	TargetMiB float64
	// TargetBytes is TargetMiB in bytes, so a caller that compares it with free
	// disk space does not have to know how a MiB is spelled (plan.md §8).
	TargetBytes int64
	Duration    time.Duration
	AudioKbps   int
	// Reserve is the fraction of the target kept back for muxing, as it was
	// actually applied.
	Reserve float64
}

// ErrTargetUnreachable means the target is too small for the overhead plus the
// audio, so there is no sensible video bitrate to ask for.
var ErrTargetUnreachable = errors.New("the target size is too small")

// DefaultReserve is the fraction of the target kept back for muxing overhead.
// Container headers, index tables and per-packet framing are not part of the
// elementary stream bitrate, so an encode that hits the video bitrate exactly
// still overshoots the file-size target without this (DECISIONS D8).
const DefaultReserve = 0.03

// DefaultMinKbps is the floor below which a video bitrate is not worth
// producing.
const DefaultMinKbps = 64

// PlanBitrate works out the video bitrate for a target file size.
//
//	target_MiB * 8192 / duration_s = total kbit/s for the whole file
//
// minus the audio and minus the reserve gives the video bitrate. A zero or
// negative duration, or a target that leaves nothing for the video above
// minKbps, is refused rather than producing a 1 kbit/s file (DECISIONS D8).
func PlanBitrate(targetMiB float64, duration time.Duration, audioKbps int, reserve float64, minKbps int) (BitratePlan, error) {
	if targetMiB <= 0 {
		return BitratePlan{}, fmt.Errorf("%w: %g MiB is not a positive size", ErrTargetUnreachable, targetMiB)
	}
	if duration <= 0 {
		return BitratePlan{}, errors.New("cannot plan a bitrate without knowing how long the clip is")
	}
	// A zero or absent reserve means "use the default": a plan with no headroom
	// at all would overshoot the target, which is never what was wanted.
	if reserve <= 0 || reserve >= 1 {
		reserve = DefaultReserve
	}
	if minKbps <= 0 {
		minKbps = DefaultMinKbps
	}
	if audioKbps < 0 {
		audioKbps = 0
	}

	// 8192 = 1024 KiB/MiB * 8 bit/KiB, giving kbit/s directly.
	totalKbps := targetMiB * 8192 / duration.Seconds()
	videoKbps := int(math.Floor(totalKbps*(1-reserve))) - audioKbps
	if videoKbps < minKbps {
		return BitratePlan{}, fmt.Errorf("%w: %.1f MiB is only %d kbit/s once %.0f%% overhead and %d kbit/s of audio are taken out; ask for at least %s",
			ErrTargetUnreachable, targetMiB, max(videoKbps, 0), reserve*100, audioKbps, SuggestSizeMiB(duration, audioKbps, reserve, minKbps))
	}

	return BitratePlan{
		VideoKbps:   videoKbps,
		MaxKbps:     videoKbps,
		BufsizeKbps: videoKbps * 2,
		TargetMiB:   targetMiB,
		TargetBytes: int64(targetMiB * 1024 * 1024),
		Duration:    duration,
		AudioKbps:   audioKbps,
		Reserve:     reserve,
	}, nil
}

// SuggestSizeMiB is the smallest whole-MiB target that still leaves room for the
// video at minKbps, for use in an error message.
func SuggestSizeMiB(duration time.Duration, audioKbps int, reserve float64, minKbps int) string {
	if duration <= 0 {
		return "a larger size"
	}
	if reserve <= 0 || reserve >= 1 {
		reserve = DefaultReserve
	}
	if minKbps <= 0 {
		minKbps = DefaultMinKbps
	}
	// Invert the formula, then round up to the next whole MiB.
	kbps := float64(minKbps+audioKbps) / (1 - reserve)
	mib := kbps * duration.Seconds() / 8192
	return fmt.Sprintf("%d MiB", int(math.Ceil(mib))+1)
}

// leadingNumberRe matches the number at the start of a bitrate string.
var leadingNumberRe = regexp.MustCompile(`^[0-9]*\.?[0-9]+`)

// ParseBitrateKbps reads a bitrate written the way ffmpeg accepts it on the
// command line, e.g. "192k", "1.5M", "128000" or "192 kbit/s", and returns it in
// kbit/s. It is what lets a task name an audio bitrate as "-b:a 192k" and still
// have the video bitrate subtract the same number.
func ParseBitrateKbps(s string) (int, bool) {
	s = strings.TrimSpace(s)
	m := leadingNumberRe.FindString(s)
	if m == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(m, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	// A bare number is bit/s; a k or M suffix changes the unit.
	mult := 1.0 / 1000
	if rest := strings.TrimSpace(strings.TrimPrefix(s, m)); rest != "" {
		switch rest[0] {
		case 'k', 'K':
			mult = 1
		case 'm', 'M':
			mult = 1000
		}
	}
	return int(math.Round(n * mult)), true
}

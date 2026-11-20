// Package ffmpeg wraps the ffmpeg and ffprobe binaries.
//
// Nothing in this package imports a TUI library: it is pure logic so that it
// can be tested without a terminal (see plan.md §3).
package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Overrides lets the user point ffwiz at specific binaries instead of relying
// on PATH. Flags win over environment variables, which win over PATH.
type Overrides struct {
	FFmpeg  string
	FFprobe string
}

// Environment variables checked when the matching CLI flag is not given.
const (
	EnvFFmpeg  = "FFWIZ_FFMPEG"
	EnvFFprobe = "FFWIZ_FFPROBE"
)

// ErrMissingTools means ffmpeg and/or ffprobe could not be located.
var ErrMissingTools = errors.New("ffmpeg/ffprobe not found")

// detectTimeout bounds the small version/listing queries. They are expected to
// take tens of milliseconds; anything slower means something is wrong.
const detectTimeout = 15 * time.Second

// Tools is the located pair of binaries plus the ffmpeg version string.
type Tools struct {
	// FFmpeg and FFprobe are absolute paths (or the override as given).
	FFmpeg  string
	FFprobe string
	// Version is the version token from `ffmpeg -version`, e.g. "8.1.1" or
	// "N-109914-g1a6cfc". It can be empty if the first line was unexpected, in
	// which case detection still succeeds.
	Version string
}

var versionRe = regexp.MustCompile(`(?m)^ffmpeg version (\S+)`)

// Locate finds ffmpeg and ffprobe and reads the ffmpeg version.
//
// It deliberately does *not* enumerate encoders: that spawns further processes
// and is done separately by Capabilities so the UI can show something
// immediately (plan.md §7).
func Locate(ctx context.Context, over Overrides) (Tools, error) {
	var t Tools
	var missing []string

	ffmpegPath, err := resolve(over.FFmpeg, EnvFFmpeg, "ffmpeg")
	if err != nil {
		missing = append(missing, "ffmpeg")
	} else {
		t.FFmpeg = ffmpegPath
	}

	ffprobePath, err := resolve(over.FFprobe, EnvFFprobe, "ffprobe")
	if err != nil {
		missing = append(missing, "ffprobe")
	} else {
		t.FFprobe = ffprobePath
	}

	if len(missing) > 0 {
		return Tools{}, fmt.Errorf("%w: %s", ErrMissingTools, strings.Join(missing, " and "))
	}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, t.FFmpeg, "-version").Output()
	if err != nil {
		// A missing version string is not fatal; the binaries still work.
		return t, nil
	}
	if m := versionRe.FindSubmatch(out); m != nil {
		t.Version = string(m[1])
	}
	return t, nil
}

// resolve applies flag > env > PATH precedence and verifies the result exists.
func resolve(flagValue, envVar, name string) (string, error) {
	candidate := flagValue
	if candidate == "" {
		candidate = lookupEnv(envVar)
	}
	if candidate == "" {
		return exec.LookPath(name)
	}
	if _, err := exec.LookPath(candidate); err != nil {
		// Allow absolute paths that LookPath rejects only if they are not
		// executable; report the original error for a clear message.
		return "", fmt.Errorf("%s: %w", candidate, err)
	}
	return candidate, nil
}

// lookupEnv is a seam for tests.
var lookupEnv = os.Getenv

// InstallHint returns per-OS instructions for installing ffmpeg (plan.md §6.1).
func InstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install it with Homebrew:  brew install ffmpeg"
	case "linux":
		return "Install it with your package manager, e.g.\n" +
			"  Debian/Ubuntu:  sudo apt install ffmpeg\n" +
			"  Fedora:         sudo dnf install ffmpeg\n" +
			"  Arch:           sudo pacman -S ffmpeg"
	case "windows":
		return "Install it with a package manager, e.g.\n" +
			"  winget install Gyan.FFmpeg\n" +
			"or download a build from https://ffmpeg.org/download.html and add it to PATH."
	default:
		return "Install it with your system package manager, or see https://ffmpeg.org/download.html"
	}
}

// Capabilities is the set of encoders/muxers/pixel formats/filters the local
// ffmpeg build supports. The task loader uses it to hide choices that the
// installed build cannot produce (plan.md §7).
type Capabilities struct {
	// Encoders maps encoder name to its description, e.g.
	// "libx264" -> "libx264 H.264 / AVC / MPEG-4 AVC ... (codec h264)".
	Encoders map[string]string
	// Muxers maps muxer name to its description, e.g. "mp4" -> "MP4 ...".
	Muxers map[string]string
	// PixelFormats and Filters are existence sets; their listings carry no
	// useful description in the form we would want to display.
	PixelFormats map[string]struct{}
	Filters      map[string]struct{}
}

// HasEncoder reports whether the build provides the named encoder.
func (c Capabilities) HasEncoder(name string) bool {
	_, ok := c.Encoders[name]
	return ok
}

// HasMuxer reports whether the build provides the named muxer.
func (c Capabilities) HasMuxer(name string) bool {
	_, ok := c.Muxers[name]
	return ok
}

// HasFilter reports whether the build provides the named filter.
func (c Capabilities) HasFilter(name string) bool {
	_, ok := c.Filters[name]
	return ok
}

// Capabilities enumerates what the local ffmpeg build supports.
//
// Five processes are spawned, so callers should run this off the UI goroutine
// (via a tea.Cmd) as plan.md §7 requires. If any single listing fails the error
// is returned but the partial result is still usable, which keeps the wizard
// working on unusual builds.
func (t Tools) Capabilities(ctx context.Context) (Capabilities, error) {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	caps := Capabilities{
		Encoders:     map[string]string{},
		Muxers:       map[string]string{},
		PixelFormats: map[string]struct{}{},
		Filters:      map[string]struct{}{},
	}

	var errs []error
	for _, job := range []struct {
		flag string
		desc bool
		into func(name, desc string)
	}{
		{"-encoders", true, func(n, d string) { caps.Encoders[n] = d }},
		{"-muxers", true, func(n, d string) { caps.Muxers[n] = d }},
		{"-pix_fmts", false, func(n, _ string) { caps.PixelFormats[n] = struct{}{} }},
		{"-filters", false, func(n, _ string) { caps.Filters[n] = struct{}{} }},
	} {
		out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", job.flag).Output()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", job.flag, err))
			continue
		}
		for _, entry := range parseListing(out) {
			job.into(entry.name, entry.desc)
		}
	}
	return caps, errors.Join(errs...)
}

// listingEntry is one parsed row of an `ffmpeg -<listing>` table.
type listingEntry struct {
	name string
	desc string
}

// capabilityRow matches the body rows of the listing tables, e.g.
//
//	" V....D libx264   libx264 H.264 / AVC ... (codec h264)"   // -encoders
//	"  E  3g2   3GP2 (3GPP2 file format)"                      // -muxers
//	" TS aap    AA->A    Apply Affine Projection ..."           // -filters
//	"IO... yuv420p   3   12   8-8-8"                            // -pix_fmts
//
// The flag column is as short as one character: muxer rows use a single "E",
// filter rows use "TS" or "..". We therefore match on the shape of the line
// rather than on fixed column offsets, which differ per listing and between
// ffmpeg versions.
var capabilityRow = regexp.MustCompile(`^\s*([A-Z.]{1,6})\s+(\S+)\s*(.*?)\s*$`)

// capabilityName rejects the legend rows ("V..... = Video", "-----") and any
// token that cannot be a real codec/muxer/pixel-format/filter name.
var capabilityName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)

// columnHeader matches the all-uppercase column titles that some listings emit,
// notably "FLAGS NAME  NB_COMPONENTS  BITS_PER_PIXEL  BIT_DEPTHS" in -pix_fmts
// (where "NAME" would otherwise be captured as a pixel format).
//
// Real names in every listing contain a lowercase letter or a digit: the only
// all-uppercase token in any of ffmpeg 8.1.1's listings is that one header,
// verified with
//
//	ffmpeg -hide_banner -<listing> | awk 'NR>1 {print $2}' | grep -E '^[A-Z]+$'
var columnHeader = regexp.MustCompile(`^[A-Z]{2,}$`)

// parseListing extracts (name, description) rows from an ffmpeg capability
// listing. It is intentionally tolerant: unknown lines are skipped rather than
// causing a failure, because ffmpeg's table layout varies between versions.
func parseListing(out []byte) []listingEntry {
	var entries []listingEntry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		m := capabilityRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// m[1] is the flags column; we only need the name and description.
		_, name, desc := m[1], m[2], m[3]
		if name == "=" {
			// Legend row, e.g. "V..... = Video", " D.. = Demuxing supported".
			continue
		}
		if columnHeader.MatchString(name) {
			// Column title row, e.g. "FLAGS NAME  NB_COMPONENTS  BITS_PER_PIXEL".
			continue
		}
		if !capabilityName.MatchString(name) {
			continue
		}
		entries = append(entries, listingEntry{name: name, desc: desc})
	}
	return entries
}

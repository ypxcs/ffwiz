package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The excerpts below are copied verbatim from ffmpeg 8.1.1, including the legend,
// separator and column-header rows and the exact column widths. The full
// listings are captured into testdata/generated/ by `make samples` and parsed by
// TestIntegrationParseRealListings, so these excerpts only need to cover the
// shapes we must not mis-parse.
//
// Note the flag columns differ in width between listings: encoders and
// pixel formats use six characters ("V....D", "IO..."), muxers use one ("E")
// and filters use one or two ("TS", "..").
const encoderListing = `Encoders:
 V..... = Video
 A..... = Audio
 S..... = Subtitle
 .F.... = Frame-level multithreading
 ..S... = Slice-level multithreading
 ...X.. = Codec is experimental
 ....B. = Supports draw_horiz_band
 .....D = Supports direct rendering method 1
 ------
 V....D a64multi             Multicolor charset for Commodore 64 (codec a64_multi)
 V....D a64multi5            Multicolor charset for Commodore 64, extended with 5th color (colram) (codec a64_multi5)
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)
 V....D libx265              libx265 H.265 / HEVC (codec hevc)
 VF...D png                  PNG (Portable Network Graphics) image
 A....D aac                  AAC (Advanced Audio Coding)
 A....D flac                 FLAC (Free Lossless Audio Codec)
 A....D libmp3lame           libmp3lame MP3 (MPEG audio layer 3) (codec mp3)
 S..... srt                  SubRip subtitle (codec subrip)
`

// muxerListing is verbatim `ffmpeg -hide_banner -muxers` output. The header is
// "Formats:" rather than "Muxers:", and the flag column is a single character.
const muxerListing = `Formats:
 D.. = Demuxing supported
 .E. = Muxing supported
 ..d = Is a device
 ---
  E  3g2             3GP2 (3GPP2 file format)
  E  3gp             3GP (3GPP file format)
  E  ac3             raw AC-3
  E  adts            ADTS AAC (Advanced Audio Coding)
  E  gif             CompuServe Graphics Interchange Format (GIF)
  E  matroska        Matroska
  E  mp3             MP3 (MPEG audio layer 3)
  E  mp4             MP4 (MPEG-4 Part 14)
  E  webm            WebM
`

// pixFmtListing is verbatim `ffmpeg -hide_banner -pix_fmts` output. It is the
// only listing with an all-uppercase column-header row, which would otherwise
// be parsed as a pixel format named "NAME".
const pixFmtListing = `Pixel formats:
I.... = Supported Input  format for conversion
.O... = Supported Output format for conversion
..H.. = Hardware accelerated format
...P. = Paletted format
....B = Bitstream format
FLAGS NAME            NB_COMPONENTS BITS_PER_PIXEL BIT_DEPTHS
-----
IO... yuv420p                3             12      8-8-8
IO... yuyv422                3             16      8-8-8
IO... rgb24                  3             24      8-8-8
IO... bgr24                  3              24      8-8-8
`

// filterListing is verbatim `ffmpeg -hide_banner -filters` output. The third
// column is an in/out signature ("AA->A"), not a description.
const filterListing = `Filters:
  T.. = Timeline support
  .S. = Slice threading
  A = Audio input/output
  V = Video input/output
  N = Dynamic number and/or type of input/output
  | = Source or sink filter
  ------
 TS aap               AA->A      Apply Affine Projection algorithm to first audio stream.
 .. abench            A->A       Benchmark part of a filtergraph.
 .S acrossover        A->N       Split audio into per-bands streams.
 .. scale             V->V       Scale the input video size and/or convert the image format.
 .. subtitles         V->V       Render text subtitles onto input video using the libass library.
 .. palettegen        V->V       Generate one palette for a whole video stream.
 .. paletteuse        V->V       Reduce the palette of input to a palette supplied.
`

func entriesToMap(entries []listingEntry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.name] = e.desc
	}
	return m
}

func TestParseListingEncoders(t *testing.T) {
	got := entriesToMap(parseListing([]byte(encoderListing)))

	for _, want := range []string{
		"libx264", "libx265", "aac", "libmp3lame", "flac", "srt", "png",
		"h264_videotoolbox", "a64multi", "a64multi5",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("encoder %q missing from parsed listing", want)
		}
	}

	for _, unwanted := range []string{"=", "Video", "Audio", "Encoders:", "-", "------"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("legend row %q was parsed as an encoder", unwanted)
		}
	}

	if want := "libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)"; got["libx264"] != want {
		t.Errorf("libx264 description = %q, want %q", got["libx264"], want)
	}
}

func TestParseListingMuxers(t *testing.T) {
	got := entriesToMap(parseListing([]byte(muxerListing)))

	// This is the case a naive six-character flag column gets wrong: muxer rows
	// have a single-character one.
	for _, want := range []string{"mp4", "matroska", "webm", "mp3", "gif", "3g2", "ac3"} {
		if _, ok := got[want]; !ok {
			t.Errorf("muxer %q missing from parsed listing", want)
		}
	}
	if want := "MP4 (MPEG-4 Part 14)"; got["mp4"] != want {
		t.Errorf("mp4 description = %q, want %q", got["mp4"], want)
	}
	for _, unwanted := range []string{"=", "D", "E", "d", "Formats:", "---"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("legend row %q was parsed as a muxer", unwanted)
		}
	}
}

func TestParseListingPixelFormats(t *testing.T) {
	got := entriesToMap(parseListing([]byte(pixFmtListing)))

	for _, want := range []string{"yuv420p", "yuyv422", "rgb24", "bgr24"} {
		if _, ok := got[want]; !ok {
			t.Errorf("pixel format %q missing", want)
		}
	}
	for _, unwanted := range []string{"NAME", "FLAGS", "=", "-"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("header or legend row %q was parsed as a pixel format", unwanted)
		}
	}
}

func TestParseListingFilters(t *testing.T) {
	got := entriesToMap(parseListing([]byte(filterListing)))

	for _, want := range []string{"aap", "abench", "acrossover", "scale", "subtitles", "palettegen", "paletteuse"} {
		if _, ok := got[want]; !ok {
			t.Errorf("filter %q missing", want)
		}
	}
	for _, unwanted := range []string{"=", "A", "V", "N", "T", "S", "|", "Filters:"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("legend row %q was parsed as a filter", unwanted)
		}
	}
}

func TestParseListingHandlesGarbage(t *testing.T) {
	for _, in := range []string{"", "\n\n", "no columns here", "\x00\x01\x02", "-----\n=\n", "   \n\t\n"} {
		if got := parseListing([]byte(in)); len(got) != 0 {
			t.Errorf("parseListing(%q) = %v, want no entries", in, got)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	// A binary guaranteed to exist on any machine running these tests.
	present := "/bin/sh"

	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(EnvFFmpeg, "/nonexistent/from-env")
		got, err := resolve(present, EnvFFmpeg, "definitely-not-a-real-binary")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != present {
			t.Errorf("resolve = %q, want the flag value %q", got, present)
		}
	})

	t.Run("env is used when the flag is empty", func(t *testing.T) {
		t.Setenv(EnvFFmpeg, present)
		got, err := resolve("", EnvFFmpeg, "definitely-not-a-real-binary")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != present {
			t.Errorf("resolve = %q, want the env value %q", got, present)
		}
	})

	t.Run("nonexistent env value is an error", func(t *testing.T) {
		t.Setenv(EnvFFmpeg, "/nonexistent/from-env")
		if _, err := resolve("", EnvFFmpeg, "definitely-not-a-real-binary"); err == nil {
			t.Error("resolve accepted a nonexistent env value")
		}
	})

	t.Run("nonexistent flag is an error", func(t *testing.T) {
		t.Setenv(EnvFFmpeg, "")
		if _, err := resolve("/nonexistent/ffmpeg", EnvFFmpeg, "ffmpeg"); err == nil {
			t.Error("resolve accepted a nonexistent flag value")
		}
	})
}

func TestCapabilitiesHelpers(t *testing.T) {
	caps := Capabilities{
		Encoders:     map[string]string{"libx264": "x264"},
		Muxers:       map[string]string{"mp4": "MP4"},
		PixelFormats: map[string]struct{}{"yuv420p": {}},
		Filters:      map[string]struct{}{"scale": {}},
	}
	if !caps.HasEncoder("libx264") || caps.HasEncoder("libx265") {
		t.Error("HasEncoder is wrong")
	}
	if !caps.HasMuxer("mp4") || caps.HasMuxer("webm") {
		t.Error("HasMuxer is wrong")
	}
	if !caps.HasFilter("scale") || caps.HasFilter("palettegen") {
		t.Error("HasFilter is wrong")
	}
}

func TestInstallHintMentionsFFmpeg(t *testing.T) {
	hint := InstallHint()
	if hint == "" {
		t.Fatal("InstallHint returned an empty string")
	}
	if !strings.Contains(hint, "ffmpeg") {
		t.Errorf("InstallHint does not mention ffmpeg: %q", hint)
	}
}

func TestLocateReportsMissingTools(t *testing.T) {
	// Point both overrides at paths that cannot exist, so Locate fails without
	// depending on whether ffmpeg is installed on the test machine.
	_, err := Locate(context.Background(), Overrides{
		FFmpeg:  "/nonexistent/ffwiz-test-ffmpeg",
		FFprobe: "/nonexistent/ffwiz-test-ffprobe",
	})
	if err == nil {
		t.Fatal("Locate succeeded with nonexistent binaries")
	}
	if !errors.Is(err, ErrMissingTools) {
		t.Errorf("error = %v, want it to wrap ErrMissingTools", err)
	}
	for _, want := range []string{"ffmpeg", "ffprobe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestIntegrationParseRealListings parses the full listings captured by
// `make samples`, which is what keeps the excerpt-based unit tests honest.
func TestIntegrationParseRealListings(t *testing.T) {
	tests := []struct {
		file      string
		minNames  int
		mustHave  []string
		mustNotBe []string
	}{
		{
			file:      "listing-encoders.txt",
			minNames:  50,
			mustHave:  []string{"libx264", "aac"},
			mustNotBe: []string{"=", "Video", "Encoders:"},
		},
		{
			file:      "listing-muxers.txt",
			minNames:  50,
			mustHave:  []string{"mp4", "matroska"},
			mustNotBe: []string{"=", "Formats:"},
		},
		{
			file:      "listing-pix_fmts.txt",
			minNames:  20,
			mustHave:  []string{"yuv420p", "rgb24"},
			mustNotBe: []string{"=", "NAME", "FLAGS"},
		},
		{
			file:     "listing-filters.txt",
			minNames: 100,
			// "subtitles" is absent from a build without libass, so it is
			// deliberately not required here; TestIntegrationCapabilitiesMatchRawListings
			// checks the optional cases against the live binary instead.
			mustHave:  []string{"scale", "fps", "setpts", "atempo", "palettegen", "paletteuse", "concat"},
			mustNotBe: []string{"=", "Filters:"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("testdata", "generated", tc.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("listing not generated (run `make samples`): %v", err)
			}

			got := entriesToMap(parseListing(data))
			if len(got) < tc.minNames {
				t.Errorf("parsed only %d names, expected at least %d", len(got), tc.minNames)
			}
			for _, want := range tc.mustHave {
				if _, ok := got[want]; !ok {
					t.Errorf("%q missing from %s", want, tc.file)
				}
			}
			for _, unwanted := range tc.mustNotBe {
				if _, ok := got[unwanted]; ok {
					t.Errorf("row %q was parsed as a name in %s", unwanted, tc.file)
				}
			}
		})
	}
}

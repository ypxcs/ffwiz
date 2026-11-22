package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultProbeTimeout bounds a single ffprobe run.
const DefaultProbeTimeout = 30 * time.Second

// StreamType is one of the codec_type values ffprobe reports.
type StreamType string

const (
	StreamVideo      StreamType = "video"
	StreamAudio      StreamType = "audio"
	StreamSubtitle   StreamType = "subtitle"
	StreamData       StreamType = "data"
	StreamAttachment StreamType = "attachment"
	StreamUnknown    StreamType = "unknown"
)

// FormatInfo is the container-level information from ffprobe's format object.
type FormatInfo struct {
	Name     string // format_name, e.g. "mov,mp4,m4a,3gp,3g2,mj2"
	LongName string
	Duration time.Duration
	Size     int64
	BitRate  int64
	Tags     map[string]string
}

// StreamInfo is one elementary stream.
type StreamInfo struct {
	Index         int
	Type          StreamType
	CodecName     string
	CodecLongName string
	Profile       string

	// Video-only.
	Width         int
	Height        int
	PixelFormat   string
	FrameRate     float64 // frames per second; 0 when the stream does not report one
	DisplayAspect string
	SampleAspect  string

	// Audio-only.
	Channels      int
	ChannelLayout string
	SampleRate    int
	BitsPerSample int

	Duration   time.Duration
	BitRate    int64
	FrameCount int64

	Language string
	Title    string
	Tags     map[string]string
}

// MediaInfo is everything ffwiz knows about an input file.
type MediaInfo struct {
	Path    string
	Format  FormatInfo
	Streams []StreamInfo
}

// Duration returns the best available duration for the file: the container
// duration if ffprobe reported one, otherwise the longest stream duration.
//
// Some formats (raw streams, still images) report no duration at all; callers
// must treat 0 as "unknown" rather than as an error (plan.md §7).
func (m *MediaInfo) Duration() time.Duration {
	if m.Format.Duration > 0 {
		return m.Format.Duration
	}
	var longest time.Duration
	for _, s := range m.Streams {
		if s.Duration > longest {
			longest = s.Duration
		}
	}
	return longest
}

// LastFrameTime is the latest point on the timeline from which a frame can still
// be read: the duration, less one frame when the input reports a frame rate.
//
// Seeking to the very end of a clip selects no frame - measured on a 2s, 25fps
// file: a frame comes out at 1.96s, and 1.999s already writes nothing and exits
// 0 - so the end of the clip is not a place a timestamp may point at
// (DECISIONS D36). Zero means "unknown", which callers must not treat as a
// position.
func (m *MediaInfo) LastFrameTime() time.Duration {
	if m == nil {
		return 0
	}
	d := m.Duration()
	if d <= 0 {
		return 0
	}
	frame := time.Duration(0)
	if v, ok := m.FirstStream(StreamVideo); ok && v.FrameRate > 0 {
		frame = time.Duration(float64(time.Second) / v.FrameRate)
	}
	// With no frame rate to go by, back off the smallest amount that still
	// leaves the end of the clip unreachable: the end itself is never a frame.
	if frame <= 0 || frame >= d {
		frame = time.Millisecond
	}
	if frame >= d {
		return 0
	}
	return d - frame
}

// Has reports whether the file contains at least one stream of the given type.
func (m *MediaInfo) Has(t StreamType) bool {
	for _, s := range m.Streams {
		if s.Type == t {
			return true
		}
	}
	return false
}

// StreamsOf returns every stream of the given type, in file order.
func (m *MediaInfo) StreamsOf(t StreamType) []StreamInfo {
	var out []StreamInfo
	for _, s := range m.Streams {
		if s.Type == t {
			out = append(out, s)
		}
	}
	return out
}

// FirstStream returns the first stream of the given type.
func (m *MediaInfo) FirstStream(t StreamType) (StreamInfo, bool) {
	for _, s := range m.Streams {
		if s.Type == t {
			return s, true
		}
	}
	return StreamInfo{}, false
}

// Probe runs ffprobe against path and parses the result.
func Probe(ctx context.Context, ffprobePath, path string) (*MediaInfo, error) {
	return ProbeWithTimeout(ctx, ffprobePath, path, DefaultProbeTimeout)
}

// ProbeWithTimeout is Probe with an explicit deadline.
func ProbeWithTimeout(ctx context.Context, ffprobePath, path string, timeout time.Duration) (*MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe path is empty")
	}

	// -v error keeps stderr limited to real problems; the JSON goes to stdout.
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		GuardPath(path),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("ffprobe %s: %w: %s", path, err, firstLine(msg))
		}
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}

	info, err := ParseProbeJSON(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	info.Path = path
	return info, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ParseProbeJSON decodes `ffprobe -print_format json -show_format -show_streams`
// output. It is exported so tests can run against captured fixtures without
// spawning a process.
func ParseProbeJSON(data []byte) (*MediaInfo, error) {
	var raw rawResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing ffprobe json: %w", err)
	}

	info := &MediaInfo{
		Format: FormatInfo{
			Name:     raw.Format.FormatName,
			LongName: raw.Format.LongName,
			Duration: raw.Format.Duration.Duration(),
			Size:     raw.Format.Size.Int64(),
			BitRate:  raw.Format.BitRate.Int64(),
			Tags:     raw.Format.Tags.strings(),
		},
	}

	for _, rs := range raw.Streams {
		s := StreamInfo{
			Index:         int(rs.Index.Int64()),
			Type:          normalizeStreamType(rs.CodecType),
			CodecName:     rs.CodecName,
			CodecLongName: rs.CodecLongName,
			Profile:       rs.Profile,
			Width:         int(rs.Width.Int64()),
			Height:        int(rs.Height.Int64()),
			PixelFormat:   rs.PixelFormat,
			FrameRate:     parseRatio(rs.RFrameRate.String()),
			DisplayAspect: rs.DisplayAspectRatio,
			SampleAspect:  rs.SampleAspectRatio,
			Channels:      int(rs.Channels.Int64()),
			ChannelLayout: rs.ChannelLayout,
			SampleRate:    int(rs.SampleRate.Int64()),
			BitsPerSample: int(rs.BitsPerSample.Int64()),
			Duration:      rs.Duration.Duration(),
			BitRate:       rs.BitRate.Int64(),
			FrameCount:    rs.NbFrames.Int64(),
			Tags:          rs.Tags.strings(),
		}
		// avg_frame_rate is a better average for variable-frame-rate video, but
		// r_frame_rate is what ffmpeg itself uses for the input frame rate and
		// is always present, so only fall back to avg when r is missing.
		if s.FrameRate == 0 {
			s.FrameRate = parseRatio(rs.AvgFrameRate.String())
		}
		// Stream durations are often absent while the container has one; fill
		// it in so per-stream ETA maths has something to work with.
		if s.Duration == 0 && info.Format.Duration > 0 {
			s.Duration = info.Format.Duration
		}
		s.Language = s.Tags["language"]
		s.Title = s.Tags["title"]
		info.Streams = append(info.Streams, s)
	}

	return info, nil
}

func normalizeStreamType(t string) StreamType {
	switch StreamType(strings.ToLower(t)) {
	case StreamVideo:
		return StreamVideo
	case StreamAudio:
		return StreamAudio
	case StreamSubtitle:
		return StreamSubtitle
	case StreamData:
		return StreamData
	case StreamAttachment:
		return StreamAttachment
	default:
		return StreamUnknown
	}
}

// flex is a JSON scalar (string, number, bool or null) decoded into text.
//
// ffprobe is inconsistent about types: "duration" and "bit_rate" are strings,
// "width" and "channels" are numbers, and any of them may be absent. Decoding
// everything through this type keeps the structs free of pointer checks.
type flex string

func (f *flex) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	switch {
	case s == "" || s == "null":
		*f = ""
	case s[0] == '"':
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*f = flex(v)
	default:
		*f = flex(s)
	}
	return nil
}

func (f flex) String() string { return string(f) }

// Int64 returns the value as an integer, or 0 when absent/unparseable.
func (f flex) Int64() int64 {
	s := strings.TrimSpace(string(f))
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	// Some fields arrive as floats ("29.97") even when we want an int.
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(v)
	}
	return 0
}

// Duration parses ffprobe's seconds-as-string ("2.021000"). "N/A" and absent
// values yield 0, which callers treat as unknown.
func (f flex) Duration() time.Duration {
	s := strings.TrimSpace(string(f))
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0
	}
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// parseRatio parses ffprobe's fractional rationals such as "30000/1001".
// Returns 0 for empty, "0/0" and unparseable input.
func parseRatio(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// flexTags is a tag map whose values may be any scalar type.
type flexTags map[string]flex

func (t flexTags) strings() map[string]string {
	if len(t) == 0 {
		return nil
	}
	out := make(map[string]string, len(t))
	for k, v := range t {
		out[k] = v.String()
	}
	return out
}

type rawResult struct {
	Format  rawFormat   `json:"format"`
	Streams []rawStream `json:"streams"`
}

type rawFormat struct {
	Filename   string   `json:"filename"`
	FormatName string   `json:"format_name"`
	LongName   string   `json:"format_long_name"`
	Duration   flex     `json:"duration"`
	Size       flex     `json:"size"`
	BitRate    flex     `json:"bit_rate"`
	Tags       flexTags `json:"tags"`
}

type rawStream struct {
	Index              flex     `json:"index"`
	CodecName          string   `json:"codec_name"`
	CodecLongName      string   `json:"codec_long_name"`
	CodecType          string   `json:"codec_type"`
	Profile            string   `json:"profile"`
	Width              flex     `json:"width"`
	Height             flex     `json:"height"`
	PixelFormat        string   `json:"pix_fmt"`
	SampleAspectRatio  string   `json:"sample_aspect_ratio"`
	DisplayAspectRatio string   `json:"display_aspect_ratio"`
	RFrameRate         flex     `json:"r_frame_rate"`
	AvgFrameRate       flex     `json:"avg_frame_rate"`
	Duration           flex     `json:"duration"`
	BitRate            flex     `json:"bit_rate"`
	Channels           flex     `json:"channels"`
	ChannelLayout      string   `json:"channel_layout"`
	SampleRate         flex     `json:"sample_rate"`
	BitsPerSample      flex     `json:"bits_per_sample"`
	NbFrames           flex     `json:"nb_frames"`
	Tags               flexTags `json:"tags"`
}

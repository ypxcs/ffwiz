package wizard

import (
	"fmt"
	"strings"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
)

// mediaSummary describes a probed file in plain lines: duration, size, container
// and one line per stream (plan.md §6.2).
//
// It never returns a value that cannot be rendered: a nil or partial probe
// produces a short note instead, because View runs before the probe command has
// returned (DECISIONS D18).
func mediaSummary(info *ffmpeg.MediaInfo) (string, error) {
	if info == nil {
		return "", fmt.Errorf("no file has been read yet")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  container  %s\n", orUnknown(info.Format.Name))
	fmt.Fprintf(&b, "  duration   %s\n", format.Duration(info.Duration()))
	fmt.Fprintf(&b, "  size       %s\n", format.Bytes(info.Format.Size))
	if info.Format.BitRate > 0 {
		fmt.Fprintf(&b, "  bitrate    %s\n", format.BitRate(info.Format.BitRate))
	}
	b.WriteString("\n")
	for _, s := range info.Streams {
		switch s.Type {
		case ffmpeg.StreamVideo:
			fmt.Fprintf(&b, "  video #%d   %s %s %s %s\n",
				s.Index, orUnknown(s.CodecName), resolutionLabel(s.Width, s.Height),
				orUnknown(s.PixelFormat), frameRateLabel(s.FrameRate))
		case ffmpeg.StreamAudio:
			channels := s.ChannelLayout
			if channels == "" && s.Channels > 0 {
				channels = fmt.Sprintf("%dch", s.Channels)
			}
			fmt.Fprintf(&b, "  audio #%d   %s %s%s\n",
				s.Index, orUnknown(s.CodecName), orUnknown(channels), sampleRateLabel(s.SampleRate))
			if lang := languageLabel(s.Language); lang != "" {
				fmt.Fprintf(&b, "               language %s\n", lang)
			}
		case ffmpeg.StreamSubtitle:
			fmt.Fprintf(&b, "  subs  #%d   %s%s\n", s.Index, orUnknown(s.CodecName), languageSuffix(s.Language))
		default:
			fmt.Fprintf(&b, "  other #%d   %s\n", s.Index, orUnknown(s.CodecName))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func frameRateLabel(fps float64) string {
	if fps <= 0 {
		return "unknown fps"
	}
	return fmt.Sprintf("%.3g fps", fps)
}

// resolutionLabel renders WxH, or a placeholder when the probe could not
// determine it. A literal "0x0" reads like a real resolution and is worse than
// saying nothing.
func resolutionLabel(w, h int) string {
	if w <= 0 || h <= 0 {
		return "(unknown size)"
	}
	return fmt.Sprintf("%dx%d", w, h)
}

func sampleRateLabel(rate int) string {
	if rate <= 0 {
		return ""
	}
	return fmt.Sprintf(" %d Hz", rate)
}

func languageSuffix(lang string) string {
	lang = languageLabel(lang)
	if lang == "" {
		return ""
	}
	return " " + lang
}

// languageLabel returns a language code worth showing, or "" for one carrying no
// information.
//
// ffprobe reports "und" (undetermined) whenever it cannot tell, which is common
// for streams whose container never recorded one. Printing it would put a
// meaningless token where a real code such as "eng" belongs, and would read as
// if the file had been tagged deliberately.
func languageLabel(lang string) string {
	if lang == "" || strings.EqualFold(lang, "und") {
		return ""
	}
	return lang
}

// orUnknown substitutes a readable placeholder for a missing value, so a summary
// never renders an empty column that looks like a bug.
func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

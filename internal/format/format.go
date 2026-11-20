// Package format renders sizes, durations and bit rates for people to read.
//
// It exists so that the wizard screens and the headless --dump output describe a
// file the same way: two implementations of "truncate to the millisecond" would
// eventually disagree, and the dump output is what the tests assert on.
//
// It holds no state and imports nothing but the standard library, so it is safe
// to use from anywhere, including internal/tasks if a future task needs it.
package format

import (
	"fmt"
	"time"
)

// Duration formats a media duration, truncating below the millisecond because
// anything finer is noise for media, and truncating is honest: a 1.9999s clip
// must not read as 2s.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	rounded := d.Truncate(time.Millisecond)
	if rounded == 0 {
		return "<1ms"
	}
	return rounded.String()
}

// Clock formats an elapsed or remaining time as M:SS, or H:MM:SS past an hour.
//
// It differs from Duration on purpose: a clock reading is a position on a
// timeline, where 0:00 is a real value rather than a missing one, and where
// sub-second precision is noise that makes the display jitter.
func Clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// Bytes formats a byte count for display. Sizes use binary units and are
// labelled as such (KiB, not KB), because that is how file managers report them
// and a mismatch between the two is confusing.
func Bytes(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMG"[exp])
}

// BitRate formats a bit rate. Rates are decimal (kbit/s = 1000 bit/s) because
// that is how every codec and container document quotes them.
func BitRate(bps int64) string {
	switch {
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mbit/s", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.0f kbit/s", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d bit/s", bps)
	}
}

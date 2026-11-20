package format

import (
	"testing"
	"time"
)

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
		if got := Bytes(tc.in); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.in, got, tc.want)
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
		if got := BitRate(tc.in); got != tc.want {
			t.Errorf("BitRate(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
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
		if got := Duration(tc.in); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClock(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{-5 * time.Second, "0:00"},
		{3 * time.Second, "0:03"},
		{65 * time.Second, "1:05"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour, "1:00:00"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
		// Rounded to the nearest second: a clock that flickers between two
		// values for one frame is worse than one that is half a second out.
		{1499 * time.Millisecond, "0:01"},
		{1600 * time.Millisecond, "0:02"},
		{1400 * time.Millisecond, "0:01"},
		{10*time.Hour + 5*time.Minute + 9*time.Second, "10:05:09"},
	}
	for _, tc := range tests {
		if got := Clock(tc.in); got != tc.want {
			t.Errorf("Clock(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

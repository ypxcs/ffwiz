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

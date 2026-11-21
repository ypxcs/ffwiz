package ffmpeg

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlanBitrateMatchesTheDocumentedFormula(t *testing.T) {
	// DECISIONS D8: video_kbps = floor(target_MiB * 8192 / duration_s * 0.97)
	//                            - audio_kbps
	// 10 MiB in 60 s with 128 kbit/s of audio:
	//   10 * 8192 / 60 = 1365.33 kbit/s total
	//   1365.33 * 0.97 = 1324.37
	//   floor -> 1324, minus 128 = 1196
	plan, err := PlanBitrate(10, 60*time.Second, 128, DefaultReserve, DefaultMinKbps)
	if err != nil {
		t.Fatal(err)
	}
	if plan.VideoKbps != 1196 {
		t.Errorf("VideoKbps = %d, want 1196", plan.VideoKbps)
	}
	if plan.MaxKbps != plan.VideoKbps {
		t.Errorf("MaxKbps = %d, want the average rate", plan.MaxKbps)
	}
	if plan.BufsizeKbps != 2*plan.VideoKbps {
		t.Errorf("BufsizeKbps = %d, want twice the maxrate", plan.BufsizeKbps)
	}
	if plan.TargetMiB != 10 || plan.Duration != 60*time.Second || plan.AudioKbps != 128 {
		t.Errorf("plan does not carry its inputs: %+v", plan)
	}
}

func TestPlanBitrateIsMonotonicInTargetSize(t *testing.T) {
	small, err := PlanBitrate(20, 10*time.Minute, 128, DefaultReserve, DefaultMinKbps)
	if err != nil {
		t.Fatal(err)
	}
	large, err := PlanBitrate(40, 10*time.Minute, 128, DefaultReserve, DefaultMinKbps)
	if err != nil {
		t.Fatal(err)
	}
	if large.VideoKbps <= small.VideoKbps {
		t.Errorf("doubling the target did not raise the bitrate: %d then %d", small.VideoKbps, large.VideoKbps)
	}
}

func TestPlanBitrateRefusesAnImpossibleTarget(t *testing.T) {
	// 1 MiB over an hour is a few kbit/s: not a video.
	_, err := PlanBitrate(1, time.Hour, 128, DefaultReserve, DefaultMinKbps)
	if !errors.Is(err, ErrTargetUnreachable) {
		t.Fatalf("err = %v, want ErrTargetUnreachable", err)
	}
	if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("the refusal should suggest a size, got %q", err)
	}
}

func TestPlanBitrateNeedsADuration(t *testing.T) {
	if _, err := PlanBitrate(10, 0, 128, DefaultReserve, DefaultMinKbps); err == nil {
		t.Fatal("a plan without a duration should fail")
	}
	if _, err := PlanBitrate(0, time.Minute, 0, DefaultReserve, DefaultMinKbps); err == nil {
		t.Fatal("a plan without a size should fail")
	}
}

func TestSuggestSizeIsBigEnough(t *testing.T) {
	duration := time.Hour
	suggestion := SuggestSizeMiB(duration, 128, DefaultReserve, DefaultMinKbps)
	// The suggested size, parsed back, must actually be reachable.
	number := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(suggestion), "MiB"))
	mib, err := strconv.ParseFloat(number, 64)
	if err != nil {
		t.Fatalf("parsing %q: %v", suggestion, err)
	}
	if _, err := PlanBitrate(mib, duration, 128, DefaultReserve, DefaultMinKbps); err != nil {
		t.Errorf("the suggested size %s is still unreachable: %v", suggestion, err)
	}
}

func TestParseBitrateKbps(t *testing.T) {
	tests := map[string]int{
		"192k":       192,
		"192K":       192,
		"1.5M":       1500,
		"128000":     128, // a bare number is bit/s
		"192 kbit/s": 192,
		"96k":        96,
	}
	for in, want := range tests {
		got, ok := ParseBitrateKbps(in)
		if !ok {
			t.Errorf("ParseBitrateKbps(%q) failed", in)
			continue
		}
		if got != want {
			t.Errorf("ParseBitrateKbps(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "k"} {
		if _, ok := ParseBitrateKbps(bad); ok {
			t.Errorf("ParseBitrateKbps(%q) should fail", bad)
		}
	}
}

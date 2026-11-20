package tasks

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// stepsTask exercises the four step types that M5 adds, plus the platform
// condition and the time_range placeholders.
const stepsTask = `
- id: steps
  label: Step types
  description: Used by the M5 step tests.
  requires: { video: true }
  steps:
    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, default: true }
        - { label: MKV, value: mkv, output_ext: mkv }

    - id: extras
      prompt: Extras
      type: multi
      default: faststart
      choices:
        - { label: Fast start, value: faststart, args: ["-movflags", "+faststart"] }
        - { label: Thumbnail, value: thumb, args: ["-vf", "thumbnail"], skips: [quality] }
        - { label: No audio, value: noaudio, args: ["-an"] }

    - id: quality
      prompt: Quality
      type: choice
      choices:
        - { label: Balanced, value: balanced, args: ["-crf", "23"], default: true }

    - id: height
      prompt: Height
      type: resolution
      template: ["-vf", "scale=-2:{value}"]
      custom: true
      validate: { min: 16, max: 4320 }
      choices:
        - { label: 1080p, value: "1080", default: true }
        - { label: 720p, value: "720" }

    - id: range
      prompt: Range
      type: time_range
      input_template: ["-ss", "{start}"]
      template: ["-t", "{duration}"]

    - id: faststart
      prompt: Fast start
      type: confirm
      args: ["-movflags", "+faststart"]

    - id: hardware
      prompt: Hardware
      type: choice
      when: { platform: darwin }
      choices:
        - { label: VideoToolbox, value: vt, args: ["-c:v", "h264_videotoolbox"], requires_encoder: h264_videotoolbox }
`

func loadStepsTask(t *testing.T) Task {
	t.Helper()
	var list []Task
	if err := yaml.Unmarshal([]byte(stepsTask), &list); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	task := list[0]
	task.Source = "steps.yaml"
	if err := (&Set{Tasks: []Task{task}}).Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return task
}

func videoInfo() *ffmpeg.MediaInfo {
	return &ffmpeg.MediaInfo{Format: ffmpeg.FormatInfo{Duration: 120 * time.Second}}
}

// ── multi ───────────────────────────────────────────────────────────────────

func TestResolveMultiTakesTheTickInOfferOrder(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}

	// Given in reverse order: the answer must come back in the order the
	// choices are offered, so the command does not depend on tick order.
	res := eng.Resolve(map[string]string{"extras": "noaudio,faststart"})
	if got, want := res.Answers["extras"], "faststart,noaudio"; got != want {
		t.Errorf("extras = %q, want %q", got, want)
	}

	selected := res.Selected("extras")
	if len(selected) != 2 || selected[0].Value != "faststart" || selected[1].Value != "noaudio" {
		t.Errorf("Selected = %v, want faststart then noaudio", selected)
	}
}

func TestResolveMultiDropsValuesThatAreNotOffered(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(map[string]string{"extras": "faststart,ghost"})
	if got, want := res.Answers["extras"], "faststart"; got != want {
		t.Errorf("extras = %q, want %q (the unknown value is dropped)", got, want)
	}
}

func TestResolveMultiFallsBackToItsDefault(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(nil)
	if got, want := res.Answers["extras"], "faststart"; got != want {
		t.Errorf("extras = %q, want the default %q", got, want)
	}
}

func TestResolveMultiChoiceSkipsALaterStep(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(map[string]string{"extras": "thumb"})
	if _, skipped := res.Skipped["quality"]; !skipped {
		t.Errorf("ticking thumb should skip quality; skipped = %v", res.Skipped)
	}
	// Unticking it puts the step back, which is why the skip is recomputed
	// rather than remembered.
	res = eng.Resolve(map[string]string{"extras": "faststart"})
	if _, skipped := res.Skipped["quality"]; skipped {
		t.Errorf("quality should be back; skipped = %v", res.Skipped)
	}
}

func TestBuildAppliesEveryTickedMultiChoice(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	cmd, _, err := eng.Build(map[string]string{"extras": "faststart,noaudio"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	display := cmd.DisplayArgs()
	if !slices.Contains(display, "-movflags") || !slices.Contains(display, "+faststart") {
		t.Errorf("args = %v, want the faststart option", display)
	}
	if !slices.Contains(display, "-an") {
		t.Errorf("args = %v, want -an from the second ticked option", display)
	}
}

// ── resolution ──────────────────────────────────────────────────────────────

func TestResolveResolutionPresetUsesTheTemplate(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(nil)
	if got, want := res.Answers["height"], "1080"; got != want {
		t.Fatalf("height = %q, want the default %q", got, want)
	}
	if _, isChoice := res.Choices["height"]; isChoice {
		// A resolution contributes through its template, never as a choice
		// carrying its own args.
		t.Errorf("a resolution step should not register a choice")
	}
}

func TestResolveResolutionAcceptsACustomValue(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(map[string]string{"height": "900"})
	if got := res.Answers["height"]; got != "900" {
		t.Errorf("height = %q, want the typed value", got)
	}
}

func TestResolveResolutionRefusesACustomValueOutsideItsBounds(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	// 4 is below the declared minimum, so the step falls back to its default
	// rather than emitting scale=-2:4.
	res := eng.Resolve(map[string]string{"height": "4"})
	if got := res.Answers["height"]; got != "1080" {
		t.Errorf("height = %q, want the default after an out-of-range value", got)
	}
}

func TestBuildResolutionEmitsTheScaledTemplate(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	cmd, _, err := eng.Build(map[string]string{"height": "720"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	display := cmd.DisplayArgs()
	if !slices.Contains(display, "-vf") || !slices.Contains(display, "scale=-2:720") {
		t.Errorf("args = %v, want a -vf scale=-2:720", display)
	}
}

// ── confirm ─────────────────────────────────────────────────────────────────

func TestResolveConfirmDefaultsToNo(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(nil)
	if got, want := res.Answers["faststart"], confirmNo; got != want {
		t.Errorf("faststart = %q, want %q (an unanswered toggle adds nothing)", got, want)
	}
	if res.Pending["faststart"] {
		t.Errorf("a confirm step is never pending: it is a toggle")
	}
}

func TestResolveConfirmYesAddsItsArgs(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	cmd, res, err := eng.Build(map[string]string{"faststart": "yes"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Yes("faststart") {
		t.Fatalf("faststart = %q, want yes", res.Answers["faststart"])
	}
	if !slices.Contains(cmd.DisplayArgs(), "-movflags") {
		t.Errorf("args = %v, want -movflags from the confirm", cmd.DisplayArgs())
	}
}

func TestResolveConfirmAcceptsTheSpellingsAYamlAuthorMightUse(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	for _, in := range []string{"yes", "true", "on", "1"} {
		res := eng.Resolve(map[string]string{"faststart": in})
		if !res.Yes("faststart") {
			t.Errorf("%q should mean yes, got %q", in, res.Answers["faststart"])
		}
	}
	for _, in := range []string{"no", "false", "off", "0", "maybe"} {
		res := eng.Resolve(map[string]string{"faststart": in})
		if res.Yes("faststart") {
			t.Errorf("%q should mean no, got %q", in, res.Answers["faststart"])
		}
	}
}

// ── time_range ──────────────────────────────────────────────────────────────

func TestParseTimeReadsSecondsAndClocks(t *testing.T) {
	tests := map[string]time.Duration{
		"30":           30 * time.Second,
		"12.5":         12500 * time.Millisecond,
		"0:30":         30 * time.Second,
		"1:02:03":      time.Hour + 2*time.Minute + 3*time.Second,
		"00:00:30.250": 30250 * time.Millisecond,
	}
	for in, want := range tests {
		got, err := ParseTime(in)
		if err != nil {
			t.Errorf("ParseTime(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTime(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5", "1:2:3:4", "1:x"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) should fail", bad)
		}
	}
}

func TestParseRangeNeedsAnEndAfterTheStart(t *testing.T) {
	r, err := ParseRange("10,30")
	if err != nil {
		t.Fatal(err)
	}
	if r.Start != 10*time.Second || r.End != 30*time.Second || r.Duration() != 20*time.Second {
		t.Errorf("range = %+v", r)
	}
	if _, err := ParseRange("30,10"); err == nil {
		t.Errorf("an end before the start should fail")
	}
	if _, err := ParseRange("10"); err == nil {
		t.Errorf("a single time is not a range")
	}
}

func TestCheckRangeAnswerRefusesAnEndPastTheInput(t *testing.T) {
	step := loadStepsTask(t).Steps[4]
	if err := step.CheckRangeAnswer("10,30", 120*time.Second); err != nil {
		t.Errorf("a range inside the input should pass: %v", err)
	}
	if err := step.CheckRangeAnswer("10,300", 120*time.Second); err == nil {
		t.Errorf("an end past the input should fail")
	}
	// An unknown input length (zero) only checks that the two ends are ordered.
	if err := step.CheckRangeAnswer("10,300", 0); err != nil {
		t.Errorf("with an unknown length only ordering is checked: %v", err)
	}
}

func TestBuildTimeRangePutsTheSeekBeforeTheInput(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	cmd, _, err := eng.Build(map[string]string{"range": "10,40"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	display := cmd.DisplayArgs()
	// The seek must precede -i; the duration must follow it (DECISIONS D6).
	seek, input, duration := slices.Index(display, "-ss"), slices.Index(display, "-i"), -1
	for i, a := range display {
		if a == "-t" {
			duration = i
		}
	}
	if seek < 0 || input < 0 || duration < 0 {
		t.Fatalf("args = %v, want -ss, -i and -t", display)
	}
	if seek > input {
		t.Errorf("args = %v: -ss must come before -i", display)
	}
	if duration < input {
		t.Errorf("args = %v: -t must come after -i", display)
	}
	if !slices.Contains(display, "00:00:10.000") || !slices.Contains(display, "00:00:30.000") {
		t.Errorf("args = %v, want the formatted start and duration", display)
	}
}

func TestExpectedDurationUsesTheTrimmedLength(t *testing.T) {
	eng := &Engine{Task: loadStepsTask(t), Info: videoInfo()}
	res := eng.Resolve(map[string]string{"range": "10,40"})
	got, _, err := eng.Build(map[string]string{"range": "10,40"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answers["range"] != "10,40" {
		t.Fatalf("range = %q", res.Answers["range"])
	}
	if d := got.ExpectedDuration(); d != 30*time.Second {
		t.Errorf("expected duration = %v, want 30s", d)
	}
}

// ── platform condition ──────────────────────────────────────────────────────

func TestPlatformConditionGatesAStep(t *testing.T) {
	task := loadStepsTask(t)
	caps := &ffmpeg.Capabilities{Encoders: map[string]string{"h264_videotoolbox": ""}}

	onMac := &Engine{Task: task, Info: videoInfo(), Caps: caps, Platform: "darwin"}
	if _, skipped := onMac.Resolve(nil).Skipped["hardware"]; skipped {
		t.Errorf("the hardware step should apply on darwin")
	}
	onLinux := &Engine{Task: task, Info: videoInfo(), Caps: caps, Platform: "linux"}
	if _, skipped := onLinux.Resolve(nil).Skipped["hardware"]; !skipped {
		t.Errorf("the hardware step should be skipped on linux")
	}
}

func TestConditionExplainNamesThePlatform(t *testing.T) {
	c := &Condition{Platform: "darwin"}
	if got := c.Explain(); !strings.Contains(got, "macOS") {
		t.Errorf("Explain = %q, want it to name macOS", got)
	}
}

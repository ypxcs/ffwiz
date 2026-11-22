package wizard

import (
	"strings"
	"testing"
	"testing/fstest"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// stepsYAML is a small task that uses every M5 step type, so the step renderer
// can be driven without depending on the shape of any shipped task.
const stepsYAML = `
- id: steps
  label: Step types
  description: Exercises every step type.
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
        - { label: No audio, value: noaudio, args: ["-an"] }

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
      args: ["-map_metadata", "-1"]
`

func stepsSet(t *testing.T) *tasks.Set {
	t.Helper()
	set, err := tasks.Load(tasks.LoadOptions{
		UserDir:   t.TempDir(),
		BuiltinFS: fstest.MapFS{"builtin/steps.yaml": &fstest.MapFile{Data: []byte(stepsYAML)}},
	})
	if err != nil {
		t.Fatalf("loading the fixture task: %v", err)
	}
	return set
}

// stepSession opens the wizard on the fixture task's first question.
func stepSession(t *testing.T) *session {
	t.Helper()
	caps := machineCaps()
	s := newSession(t, Deps{Set: stepsSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: "/tmp/clip.mp4", info: avInfo()})
	s.press("enter", "enter") // input summary, then start the only task
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the first step", s.model.screen)
	}
	return s
}

// ── multi ───────────────────────────────────────────────────────────────────

func TestMultiTogglesOptionsOnAndOff(t *testing.T) {
	s := stepSession(t)
	s.press("enter") // container: accept MP4, move to extras

	step, ok := s.model.currentStep()
	if !ok || step.ID != "extras" {
		t.Fatalf("current step = %v, want extras", step.ID)
	}
	// The default is ticked, so the first option starts checked.
	if view := s.view(); !strings.Contains(view, "[x]") || !strings.Contains(view, "[ ]") {
		t.Errorf("the multi step does not show checkboxes:\n%s", view)
	}

	// Tick the second option as well, then advance.
	s.press("down", "space", "enter")
	if got := s.model.answers["extras"]; got != "faststart,noaudio" {
		t.Errorf("extras = %q, want both ticked", got)
	}

	// Going back restores the ticks, and unticking one changes the answer.
	s.press("esc")
	step, _ = s.model.currentStep()
	if step.ID != "extras" {
		t.Fatalf("after back: step = %q, want extras", step.ID)
	}
	s.press("down", "space") // untick No audio
	s.press("enter")
	if got := s.model.answers["extras"]; got != "faststart" {
		t.Errorf("extras = %q, want only faststart after unticking", got)
	}
}

func TestMultiWithNothingTickedIsAnEmptyAnswer(t *testing.T) {
	s := stepSession(t)
	s.press("enter", "space", "enter") // container, untick the only ticked option, advance
	if got := s.model.answers["extras"]; got != "" {
		t.Errorf("extras = %q, want empty", got)
	}
	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	if strings.Contains(cmd.String(), "+faststart") {
		t.Errorf("command = %s, want no faststart option", cmd.String())
	}
}

// ── confirm ─────────────────────────────────────────────────────────────────

func TestConfirmTogglesAndAddsItsArgs(t *testing.T) {
	s := stepSession(t)
	// Walk to the confirm step: container, extras, height, then a valid range.
	s.press("enter", "enter", "enter")
	s.press("0", "enter", "1", "enter")

	step, ok := s.model.currentStep()
	if !ok {
		t.Fatal("no current step")
	}
	if step.ID != "faststart" {
		t.Fatalf("current step = %q, want faststart; steps were %v", step.ID, stepIDs(s.model.activeSteps()))
	}

	// An unanswered confirm is "no" and contributes nothing.
	if s.model.confirmYes {
		t.Errorf("the confirm should start at No")
	}
	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	if strings.Contains(cmd.String(), "-map_metadata") {
		t.Errorf("command = %s, want no -map_metadata while the toggle is off", cmd.String())
	}

	// Flipping it on and advancing puts the args in the command.
	s.press("space", "enter")
	if !s.model.resolution().Yes("faststart") {
		t.Errorf("faststart = %q, want yes", s.model.answers["faststart"])
	}
	cmd, _, err = s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	if !strings.Contains(cmd.String(), "-map_metadata") {
		t.Errorf("command = %s, want -map_metadata", cmd.String())
	}
}

// ── resolution ──────────────────────────────────────────────────────────────

func TestResolutionPresetAndCustomValue(t *testing.T) {
	s := stepSession(t)
	s.press("enter") // container
	s.press("enter") // extras
	step, ok := s.model.currentStep()
	if !ok || step.ID != "height" {
		t.Fatalf("current step = %v, want height", step.ID)
	}

	// The list shows the presets plus a Custom row.
	view := s.view()
	if !strings.Contains(view, "1080p") || !strings.Contains(view, "Custom") {
		t.Errorf("the resolution step does not show presets and a custom row:\n%s", view)
	}

	// Choosing the second preset records its value and scales accordingly.
	s.press("down", "enter")
	if got := s.model.answers["height"]; got != "720" {
		t.Fatalf("height = %q, want 720", got)
	}
	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	if !strings.Contains(cmd.String(), "scale=-2:720") {
		t.Errorf("command = %s, want scale=-2:720", cmd.String())
	}

	// Going back, moving to the Custom row and typing a height uses it.
	s.press("esc")
	s.press("down") // from the 720p preset onto the Custom row
	if s.model.choiceIdx != 2 {
		t.Fatalf("cursor = %d, want the Custom row", s.model.choiceIdx)
	}
	s.press("9", "0", "0", "enter")
	if got := s.model.answers["height"]; got != "900" {
		t.Fatalf("height = %q, want the typed 900", got)
	}
	cmd, _, err = s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	if !strings.Contains(cmd.String(), "scale=-2:900") {
		t.Errorf("command = %s, want scale=-2:900", cmd.String())
	}
}

func TestResolutionRefusesANonsenseCustomValue(t *testing.T) {
	s := stepSession(t)
	s.press("enter", "enter") // container, extras
	s.press("down", "down")   // onto the Custom row
	s.press("4", "enter")     // below the declared minimum of 16
	if s.model.notice == "" {
		t.Fatalf("a below-minimum value should be refused with a notice")
	}
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want to stay on the step", s.model.screen)
	}
}

// ── time_range ──────────────────────────────────────────────────────────────

func TestTimeRangeWalksTwoFields(t *testing.T) {
	s := stepSession(t)
	s.press("enter", "enter", "enter") // container, extras, height
	step, ok := s.model.currentStep()
	if !ok || step.ID != "range" {
		t.Fatalf("current step = %v, want range", step.ID)
	}
	if s.model.rangeField != 0 {
		t.Fatalf("the range step should start on the start field")
	}

	// The first enter validates the start and moves to the end field.
	s.press("0", "enter")
	if s.model.rangeField != 1 {
		t.Fatalf("rangeField = %d, want the end field", s.model.rangeField)
	}
	// The second enter records the whole range, canonically spelled.
	s.press("1", "enter")
	if got := s.model.answers["range"]; got != "00:00:00.000,00:00:01.000" {
		t.Fatalf("range = %q, want the canonical pair", got)
	}

	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	display := cmd.DisplayArgs()
	seek := indexOf(display, "-ss")
	input := indexOf(display, "-i")
	if seek < 0 || input < 0 || seek > input {
		t.Fatalf("args = %v, want -ss before -i (DECISIONS D6)", display)
	}
	if !containsString(display, "00:00:00.000") || !containsString(display, "00:00:01.000") {
		t.Errorf("args = %v, want the seek and the 1s duration", display)
	}
}

func TestTimeRangeRefusesAnEndPastTheInput(t *testing.T) {
	s := stepSession(t)
	s.press("enter", "enter", "enter") // to the range step
	s.press("1", "enter")              // start = 1
	s.press("5", "enter")              // end = 5, past the 2s input
	if s.model.notice == "" {
		t.Fatalf("an end past the input should be refused with a notice")
	}
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want to stay on the range step", s.model.screen)
	}
}

func TestTimeRangeBadStartStaysOnTheFirstField(t *testing.T) {
	s := stepSession(t)
	s.press("enter", "enter", "enter")
	s.press("enter") // empty start is refused
	if s.model.rangeField != 0 {
		t.Fatalf("an empty start must not move to the end field")
	}
	if s.model.notice == "" {
		t.Errorf("an empty start should explain itself")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func containsString(list []string, want string) bool { return indexOf(list, want) >= 0 }

// ── input steps that feed a filter ──────────────────────────────────────────

// subtitleSession opens the shipped burn_subtitles task, with the subtitles
// filter pretended into the capability set: the stock Homebrew build has no
// libass, and the task is not offered at all without it.
func subtitleSession(t *testing.T) *session {
	t.Helper()
	caps := machineCaps()
	caps.Filters["subtitles"] = struct{}{}
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: "/tmp/clip.mp4", info: avInfo()})
	s.startTask("burn_subtitles")
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the first step", s.model.screen)
	}
	return s
}

// typeText sends one keystroke per rune, because that is how a terminal
// delivers them.
func typeText(s *session, text string) {
	s.t.Helper()
	for _, r := range text {
		if r == ' ' {
			s.press("space")
			continue
		}
		s.press(string(r))
	}
}

// TestSubtitleStepRefusesAFileThatIsNotASubtitle: the filter picks its parser
// from the extension, so anything else is a typo, and a typo that reaches ffmpeg
// is a failed run rather than a corrected answer.
func TestSubtitleStepRefusesAFileThatIsNotASubtitle(t *testing.T) {
	s := subtitleSession(t)
	typeText(s, "notes.txt")
	s.press("enter")

	if step, _ := s.model.currentStep(); step.ID != "subs" {
		t.Errorf("the wizard moved on to %q, want it to stay on the subtitle question", step.ID)
	}
	if s.model.notice == "" {
		t.Fatal("a non-subtitle file was accepted without a word")
	}
	if !strings.Contains(s.model.notice, "srt") {
		t.Errorf("notice = %q, want it to name what is expected", s.model.notice)
	}
	if s.model.answers["subs"] != "" {
		t.Errorf("subs = %q, want the answer to be refused", s.model.answers["subs"])
	}
}

// TestSubtitleStepEscapesThePathIntoTheFilter is the end-to-end version of
// EscapeFilterValue's own test: the typed path has to arrive inside the filter
// argument escaped, or the preview would show a command that burns the wrong
// file - or fails with a message about a filter option that does not exist.
func TestSubtitleStepEscapesThePathIntoTheFilter(t *testing.T) {
	s := subtitleSession(t)
	typeText(s, "my subs.srt")
	s.press("enter")

	if s.model.notice != "" {
		t.Fatalf("the answer was refused: %s", s.model.notice)
	}
	if got := s.model.answers["subs"]; got != "my subs.srt" {
		t.Fatalf("subs = %q, want the typed path accepted", got)
	}
	if step, _ := s.model.currentStep(); step.ID == "subs" {
		t.Error("the wizard is still on the subtitle question after an answer")
	}

	cmd, _, err := s.model.liveCommand()
	if err != nil || cmd == nil {
		t.Fatalf("liveCommand: %v", err)
	}
	const want = "subtitles=filename='my subs.srt'"
	args := cmd.Args()
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) {
			if got := args[i+1]; got != want {
				t.Errorf("-vf = %q, want %q", got, want)
			}
			return
		}
	}
	t.Errorf("-vf is missing from %v", args)
}

// TestBurnSubtitlesIsRefusedWithoutLibass: a task whose only purpose is a filter
// this build does not have must not be startable, and the reason has to say so
// (DECISIONS D17, D39).
func TestBurnSubtitlesIsRefusedWithoutLibass(t *testing.T) {
	caps := machineCaps() // no subtitles filter, like the local Homebrew build
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: "/tmp/clip.mp4", info: avInfo()})
	s.press("enter") // the input summary

	var entry taskEntry
	for _, e := range s.model.taskEntries() {
		if e.task.ID == "burn_subtitles" {
			entry = e
		}
	}
	if entry.task.ID == "" {
		t.Fatal("burn_subtitles is missing from the task list")
	}
	if entry.applies {
		t.Error("burn_subtitles is offered by a build without the subtitles filter")
	}
	if !strings.Contains(entry.reason, "subtitles") {
		t.Errorf("reason = %q, want it to name the missing filter", entry.reason)
	}

	s.startTask("burn_subtitles")
	if s.model.screen == ScreenStep {
		t.Error("the wizard started a task that cannot run")
	}
	if !strings.Contains(s.model.notice, "subtitles") {
		t.Errorf("notice = %q, want it to explain why", s.model.notice)
	}
}

// ── layout ──────────────────────────────────────────────────────────────────

// TestStepScreenFitsEightyColumns renders every step type at the 80x24 minimum
// plan.md §6 requires, so a wide checkbox or custom field cannot break the
// layout.
func TestStepScreenFitsEightyColumns(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"container", nil},
		{"extras", []string{"enter"}},
		{"height", []string{"enter", "enter"}},
		{"range", []string{"enter", "enter", "enter"}},
		{"confirm", []string{"enter", "enter", "enter", "0", "enter", "1", "enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := stepSession(t)
			s.send(tea.WindowSizeMsg{Width: 80, Height: 24})
			s.press(tc.keys...)
			for i, line := range strings.Split(s.view(), "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Errorf("line %d is %d columns wide:\n%s", i, w, line)
				}
			}
		})
	}
}

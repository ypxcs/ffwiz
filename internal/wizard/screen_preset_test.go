package wizard

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// This file covers the preset screen (plan.md §5, DECISIONS D45): the named
// answer sets a task offers, and the row that asks its own questions instead.
//
// A preset is only a set of answers, so what these tests check is not a new kind
// of command but the two things a preset adds: the screen that lists them, and
// where choosing one lands.

// openPresets walks to the preset screen of a task without choosing a row.
func (s *session) openPresets(id string) {
	s.t.Helper()
	if s.model.screen == ScreenInput {
		s.press("enter")
	}
	s.selectTask(id)
	s.press("enter")
	if s.model.screen != ScreenPreset {
		s.t.Fatalf("openPresets(%q): screen = %v, want the preset list", id, s.model.screen)
	}
}

func TestPresetScreenListsTheTaskPresets(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.openPresets("convert_video")

	view := s.view()
	for _, want := range []string{
		"Start from a preset",
		"Convert video to another format",
		"Web-optimised MP4",
		"WhatsApp-friendly MP4",
		"YouTube upload (H.264/AAC)",
		noPresetLabel,
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the preset screen does not show %q:\n%s", want, view)
		}
	}

	// The cursor starts on the first preset, and up wraps round to the row that
	// asks the questions, so no row is unreachable by holding one key.
	if s.model.presetIndex != 0 {
		t.Errorf("the cursor starts on row %d, want the first preset", s.model.presetIndex)
	}
	s.press("up")
	if got, want := s.model.presetIndex, s.model.presetRowCount()-1; got != want {
		t.Errorf("up from the first row landed on %d, want %d", got, want)
	}
	if _, ok := s.model.highlightedPreset(); ok {
		t.Error("the last row is a preset, want the row that asks the questions")
	}
}

// TestPresetScreenPreviewsTheHighlightedPreset: the command pane shows the
// consequence of the row under the cursor, before anything has been chosen.
//
// Without that the one screen whose whole subject is "what would this produce"
// would be the one screen not saying.
func TestPresetScreenPreviewsTheHighlightedPreset(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.openPresets("convert_video")

	if got := s.commandText(); !strings.Contains(got, "-movflags +faststart") {
		t.Errorf("the first preset is not the one previewed:\n  %s", got)
	}

	s.press("down") // WhatsApp: small, and no streaming optimisation
	if got := s.commandText(); strings.Contains(got, "+faststart") || !strings.Contains(got, "-crf 28") {
		t.Errorf("the preview did not follow the cursor:\n  %s", got)
	}

	s.press("down") // YouTube
	if got := s.commandText(); !strings.Contains(got, "-crf 18") {
		t.Errorf("the third preset is not the one previewed:\n  %s", got)
	}

	s.press("down") // the task's own answers
	got := s.commandText()
	if strings.Contains(got, "+faststart") || !strings.Contains(got, "-crf 23") || !strings.Contains(got, "-c:a copy") {
		t.Errorf("the row that asks the questions does not preview the task's defaults:\n  %s", got)
	}
	if hint := s.model.hintPreset(); !strings.Contains(hint, "recommended answers") {
		t.Errorf("the no-preset hint = %q, want it to describe the defaults", hint)
	}
}

// TestChoosingAPresetLandsOnTheOutputScreen: a preset that answers every question
// the task asks leaves nothing to ask, so the wizard goes straight to the
// destination - the same place the last question leads to anyway.
func TestChoosingAPresetLandsOnTheOutputScreen(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.openPresets("convert_video")

	s.press("enter") // "Web-optimised MP4"
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}
	if !strings.Contains(s.model.notice, "Web-optimised MP4") {
		t.Errorf("notice = %q, want it to name the preset that was applied", s.model.notice)
	}

	want := "ffmpeg -n -i " + input + " -c:v libx264 -crf 23 -c:a aac -b:a 192k -movflags +faststart " +
		filepath.Join(dir, "clip_convert_video.mp4")
	if got := s.commandText(); got != want {
		t.Errorf("command =\n  %s\nwant\n  %s", got, want)
	}

	// The questions the preset answered are still reachable, with the preset's
	// answers shown as the selected ones (plan.md §6.4).
	s.press("esc")
	step, ok := s.model.currentStep()
	if !ok || step.ID != "streaming" {
		t.Fatalf("esc from the output screen landed on %q, want the last question", step.ID)
	}
	if !s.model.confirmYes {
		t.Error("the preset's answer is not shown as selected on the step it set")
	}
}

// TestAPresetLandsOnTheFirstQuestionItDoesNotAnswer: a preset that covers part of
// a task asks the rest rather than skipping them. The shipped presets cover
// everything they touch, so this uses a task of its own - which is also the
// proof that a preset needs nothing but YAML (plan.md §11).
func TestAPresetLandsOnTheFirstQuestionItDoesNotAnswer(t *testing.T) {
	set := loadTestSet(t, `
- id: two_steps
  label: Two questions
  description: A task whose preset answers only the first question.
  steps:
    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, default: true }
        - { label: MKV, value: mkv, output_ext: mkv }
    - id: quality
      prompt: Quality
      type: choice
      choices:
        - { label: High, value: high, args: ["-crf", "18"], default: true }
        - { label: Small, value: small, args: ["-crf", "28"] }
  presets:
    - id: mkv_only
      label: Matroska
      description: An MKV, whatever the quality.
      answers: { container: mkv }
`)
	dir := t.TempDir()
	caps := machineCaps()
	s := newSession(t, Deps{Set: set, Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: filepath.Join(dir, "clip.mp4"), info: avInfo()})

	s.openPresets("two_steps")
	s.press("enter") // the only preset

	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the question the preset left unanswered", s.model.screen)
	}
	step, ok := s.model.currentStep()
	if !ok || step.ID != "quality" {
		t.Fatalf("step = %q, want quality, the question the preset does not answer", step.ID)
	}
	if got := s.model.resolution().Answers["container"]; got != "mkv" {
		t.Errorf("container = %q, want the preset's answer to be in place", got)
	}

	// Answering what is left finishes the task, and the preset's answer is in the
	// command.
	s.press("enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen after the last question", s.model.screen)
	}
	if got := s.commandText(); !strings.Contains(got, "-crf 18") || !strings.HasSuffix(got, ".mkv") {
		t.Errorf("the command does not carry both answers:\n  %s", got)
	}
}

// TestEscFromThePresetScreenStartsNothing: the preset screen is a question like
// any other, and esc is the answer that commits to nothing.
func TestEscFromThePresetScreenStartsNothing(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.openPresets("convert_video")

	s.press("esc")
	if s.model.screen != ScreenTask {
		t.Fatalf("screen = %v, want the task list", s.model.screen)
	}
	if len(s.model.answers) != 0 {
		t.Errorf("answers = %v, want none after leaving the preset screen", s.model.answers)
	}
	if s.model.notice != "" {
		t.Errorf("notice = %q, want it cleared on the way back", s.model.notice)
	}
}

// TestNoPresetRowAsksTheTaskOwnQuestions: the last row is the task as it was
// before presets existed.
func TestNoPresetRowAsksTheTaskOwnQuestions(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.openPresets("convert_video")

	for s.model.presetIndex < s.model.presetRowCount()-1 {
		s.press("down")
	}
	s.press("enter")

	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the first question", s.model.screen)
	}
	step, _ := s.model.currentStep()
	if step.ID != "container" {
		t.Fatalf("step = %q, want the task's first question", step.ID)
	}
	if len(s.model.answers) != 0 {
		t.Errorf("answers = %v, want the task to start unanswered", s.model.answers)
	}
	// The defaults are what the pane shows, not a preset's answers.
	if got := s.commandText(); !strings.Contains(got, "-crf 23") || strings.Contains(got, "+faststart") {
		t.Errorf("the preview is not the task's own defaults:\n  %s", got)
	}
}

// TestPresetScreenFits: the preset list must not widen the frame at the smallest
// terminal plan.md §6 requires (80x24).
func TestPresetScreenFits(t *testing.T) {
	dir := t.TempDir()
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: filepath.Join(dir, "a-very-long-input-file-name-for-a-clip.mp4"), info: avInfo()})
	next, _ := s.model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s.model = next.(Model)
	s.openPresets("convert_video")

	content := s.view()
	lines := strings.Split(content, "\n")
	if len(lines) > 24 {
		t.Errorf("the view is %d lines tall, which does not fit 24 rows:\n%s", len(lines), content)
	}
	for _, line := range lines {
		if w := visibleLen(line); w > 80 {
			t.Errorf("a line is %d columns wide at 80x24: %q", w, line)
		}
	}
}

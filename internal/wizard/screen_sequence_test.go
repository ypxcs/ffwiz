package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// This file covers the numbered-sequence output of "extract frames": a run that
// writes many files rather than one. ffmpeg's image2 muxer replaces existing
// frames even under -n (measured; DECISIONS D34), so the wizard has to notice
// them itself.

// sequenceAt walks the extract-frames task in "one frame every few seconds" mode
// to the output screen, which is where the derived sequence name appears.
func sequenceAt(t *testing.T, dir string) (*session, *fakeRunner) {
	t.Helper()
	caps := machineCaps()
	runner := &fakeRunner{}
	s := newSession(t, Deps{
		Set:       builtinSet(t),
		Caps:      &caps,
		Clipboard: &fakeClipboard{},
		Tools:     ffmpeg.Tools{FFmpeg: "/fake/ffmpeg", FFprobe: "/fake/ffprobe"},
		Run:       runner.start,
		Reveal:    func(string) error { return nil },
	})
	s.send(probeMsg{path: filepath.Join(dir, "clip.mp4"), info: avInfo()})
	s.startTask("extract_frames")

	// mode: down onto "one frame every few seconds", then enter. The "at" step
	// drops out, leaving the interval and image steps.
	s.press("down", "enter", "enter", "enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}
	if !ffmpeg.IsSequence(s.model.outputPath) {
		t.Fatalf("output path = %q, want a numbered sequence", s.model.outputPath)
	}
	return s, runner
}

func TestWizardDerivesASequenceOutputName(t *testing.T) {
	dir := t.TempDir()
	s, _ := sequenceAt(t, dir)

	want := filepath.Join(dir, "clip_extract_frames.%04d.png")
	if s.model.outputPath != want {
		t.Errorf("output path = %q, want %q", s.model.outputPath, want)
	}
	if !strings.Contains(s.commandText(), "clip_extract_frames.%04d.png") {
		t.Errorf("the previewed command does not carry the sequence name:\n%s", s.commandText())
	}
}

// TestWizardAsksBeforeReplacingASequence: the frames are already there, so the
// same question as for a single file has to be asked. Choosing another name must
// leave -n in place.
func TestWizardAsksBeforeReplacingASequence(t *testing.T) {
	dir := t.TempDir()
	s, _ := sequenceAt(t, dir)

	for _, frame := range []string{"clip_extract_frames.0001.png", "clip_extract_frames.0002.png"} {
		if err := os.WriteFile(filepath.Join(dir, frame), []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s.press("enter")
	if !s.model.conflict {
		t.Fatal("the overwrite question was not asked for a sequence whose frames exist")
	}
	view := s.view()
	if !strings.Contains(view, "matches 2 existing files") {
		t.Errorf("the question does not say how many frames it would replace:\n%s", view)
	}
	if s.model.overwrite {
		t.Error("asking the question already agreed to overwrite")
	}

	// Choose another name: the frames stay and the command keeps -n.
	s.press("enter")
	if s.model.conflict {
		t.Error("choosing another name did not close the question")
	}
	if !strings.Contains(s.commandText(), "-n") {
		t.Errorf("the command no longer refuses to overwrite: %s", s.commandText())
	}
	if _, err := os.Stat(filepath.Join(dir, "clip_extract_frames.0001.png")); err != nil {
		t.Errorf("the existing frame was removed: %v", err)
	}
}

func TestWizardWillNotStartASequenceRunOverExistingFrames(t *testing.T) {
	dir := t.TempDir()
	s, runner := sequenceAt(t, dir)
	s.press("enter") // accept the name; nothing is there yet
	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}

	// Frames appear between the review screen and the run.
	if err := os.WriteFile(filepath.Join(dir, "clip_extract_frames.0007.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.press("r")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to be sent back to the output screen", s.model.screen)
	}
	if !s.model.conflict {
		t.Error("the overwrite question was not opened for the frames that appeared")
	}
	if !strings.Contains(s.model.notice, "now matches") {
		t.Errorf("notice = %q, want it to say the name is taken", s.model.notice)
	}
	if runner.started != 0 {
		t.Error("a run was started over frames that were already there")
	}
}

// TestWizardExtractFramesFitsEightyColumns renders the extract-frames questions
// and the sequence overwrite question at the 80x24 minimum, since both carry
// text the other screens never show (a file pattern, a count of frames).
func TestWizardExtractFramesFitsEightyColumns(t *testing.T) {
	fits := func(t *testing.T, s *session) {
		t.Helper()
		next, _ := s.model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		s.model = next.(Model)
		view := s.view()
		for i, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("line %d is %d columns wide:\n%s", i, w, view)
			}
		}
	}

	tests := []struct {
		name string
		keys []string
		// frames puts the sequence's files on disk first, which is what opens
		// the overwrite question.
		frames bool
		want   Screen
		// conflict is whether the overwrite question is on screen.
		conflict bool
	}{
		{name: "the mode question", want: ScreenStep},
		{name: "the thumbnail time", keys: []string{"enter"}, want: ScreenStep},
		{name: "the interval question", keys: []string{"down", "enter"}, want: ScreenStep},
		{name: "the image format", keys: []string{"down", "enter", "enter"}, want: ScreenStep},
		{name: "the output screen", keys: []string{"down", "enter", "enter", "enter"}, want: ScreenOutput},
		{
			name: "the overwrite question", frames: true,
			keys: []string{"down", "enter", "enter", "enter", "enter"},
			want: ScreenOutput, conflict: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.frames {
				for _, f := range []string{"clip_extract_frames.0001.png", "clip_extract_frames.0002.png"} {
					if err := os.WriteFile(filepath.Join(dir, f), []byte("png"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
			s.startTask("extract_frames")
			s.press(tc.keys...)
			if s.model.screen != tc.want {
				t.Fatalf("screen = %v, want %v", s.model.screen, tc.want)
			}
			if s.model.conflict != tc.conflict {
				t.Fatalf("conflict = %v, want %v", s.model.conflict, tc.conflict)
			}
			fits(t, s)
		})
	}
}

// TestWizardRefusesAThumbnailTimePastTheEnd: a seek to the end of the clip
// selects no frame, and ffmpeg then writes nothing while exiting 0 (measured,
// DECISIONS D36), so the wizard refuses the time rather than letting the run
// fail with no explanation.
func TestWizardRefusesAThumbnailTimePastTheEnd(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.startTask("extract_frames")
	s.press("enter") // "a single frame", which asks for the time

	step, ok := s.model.currentStep()
	if !ok || step.ID != "at" {
		t.Fatalf("current step = %v, want the thumbnail time", step.ID)
	}

	// The clip is two seconds at 25 fps, so the last usable point is 1.96s.
	s.model.stepInput.SetValue("5")
	s.press("enter")
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want to stay on the question", s.model.screen)
	}
	if !strings.Contains(s.model.notice, "past the end of the clip") {
		t.Errorf("notice = %q, want it to explain the time is past the end", s.model.notice)
	}
	if _, answered := s.model.answers["at"]; answered {
		t.Error("the refused time was recorded as an answer")
	}

	s.model.stepInput.SetValue("1.5")
	s.press("enter")
	if _, answered := s.model.answers["at"]; !answered {
		t.Errorf("a time inside the clip was not accepted (notice: %q)", s.model.notice)
	}
}

// TestWizardDeletesEveryFrameOfAStoppedSequence: stopping a sequence leaves
// however many frames it managed, and all of them are the partial output.
func TestWizardDeletesEveryFrameOfAStoppedSequence(t *testing.T) {
	dir := t.TempDir()
	s, _ := sequenceAt(t, dir)
	s.press("enter", "r")

	frames := []string{"clip_extract_frames.0001.png", "clip_extract_frames.0002.png", "clip_extract_frames.0003.png"}
	for _, f := range frames {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s.press("q", "q")
	if s.model.outcome != ffmpeg.OutcomeCancelled {
		t.Fatalf("outcome = %v, want cancelled", s.model.outcome)
	}
	if !strings.Contains(s.view(), "press d to delete it") {
		t.Errorf("the done screen does not offer to delete the partial frames:\n%s", s.view())
	}

	s.press("d")
	for _, f := range frames {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", f, err)
		}
	}
	if !s.model.removed {
		t.Error("the model did not record the deletion")
	}
}

// TestWizardMeasuresASequence: the size line has to describe the whole
// sequence rather than fail to stat a path that is a pattern.
func TestWizardMeasuresASequence(t *testing.T) {
	dir := t.TempDir()
	s, _ := sequenceAt(t, dir)
	s.press("enter", "r")
	for _, f := range []string{"clip_extract_frames.0001.png", "clip_extract_frames.0002.png"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("1234567890"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.send(runEventMsg{ev: ffmpeg.Event{Done: true, Result: ffmpeg.Result{ExitCode: 0}}})

	if s.model.outcome != ffmpeg.OutcomeSucceeded {
		t.Fatalf("outcome = %v, want succeeded", s.model.outcome)
	}
	view := s.view()
	if !strings.Contains(view, "in 2 files") {
		t.Errorf("the done screen does not measure the sequence:\n%s", view)
	}
	if strings.Contains(view, "could not be measured") {
		t.Errorf("the sequence could not be measured:\n%s", view)
	}
}

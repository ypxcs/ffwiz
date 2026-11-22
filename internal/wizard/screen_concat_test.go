package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ypxcs/ffwiz/internal/tasks"
)

// concatSession opens the wizard on the shipped concat task with a directory of
// clips to choose from. The clips are real (empty) files, because the browser
// reads the directory; their contents are never read, since probing is driven by
// the test through concatProbeMsg.
func concatSession(t *testing.T, names ...string) (*session, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not really media"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	caps := machineCaps()
	session := newSession(t, Deps{
		Set:       builtinSet(t),
		Caps:      &caps,
		Clipboard: &fakeClipboard{},
		// The clip browser opens beside the input file, so pointing the input
		// at the fixture directory is what puts the browser there.
		Input: filepath.Join(dir, "clip.mp4"),
	})
	session.send(probeMsg{path: filepath.Join(dir, "clip.mp4"), info: avInfo()})
	session.startTask("concat")
	if step, _ := session.model.currentStep(); step.ID != "clips" {
		t.Fatalf("concat's first step is %q, want clips", step.ID)
	}
	return session, dir
}

// clipOf is the path the answer holds for a clip name.
func clipOf(dir, name string) string { return filepath.Join(dir, name) }

func TestConcatPicksClipsInTheOrderTheyAreTicked(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")

	// The browser opens on "..", so the first file is one row down.
	s.press("down", "space")
	s.press("down", "space")
	s.press("enter")

	want := tasks.FileList([]string{clipOf(dir, "a.mp4"), clipOf(dir, "b.mp4")})
	if got := s.model.answers["clips"]; got != want {
		t.Errorf("clips =\n%q\nwant\n%q", got, want)
	}
	step, _ := s.model.currentStep()
	if step.ID != "container" {
		t.Errorf("after the clips: step = %q, want container", step.ID)
	}
}

// TestConcatOrderIsTheOrderTicked: the list is not sorted, so choosing the
// second file first is a different answer (plan.md §5.9 - the clips play in the
// order they are listed).
func TestConcatOrderIsTheOrderTicked(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")

	s.press("down", "down", "space") // b.mp4 first
	s.press("up", "space")           // then a.mp4
	s.press("enter")

	want := tasks.FileList([]string{clipOf(dir, "b.mp4"), clipOf(dir, "a.mp4")})
	if got := s.model.answers["clips"]; got != want {
		t.Errorf("clips =\n%q\nwant\n%q", got, want)
	}
}

func TestConcatUntickingTakesAClipBackOut(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")

	s.press("down", "space") // add a.mp4
	s.press("down", "space") // add b.mp4
	s.press("up", "space")   // take a.mp4 out again
	s.press("space")         // and add it once more, which puts it last
	s.press("enter")

	want := tasks.FileList([]string{clipOf(dir, "b.mp4"), clipOf(dir, "a.mp4")})
	if got := s.model.answers["clips"]; got != want {
		t.Errorf("clips =\n%q\nwant\n%q", got, want)
	}
}

func TestConcatRefusesToAdvanceWithOneClip(t *testing.T) {
	s, _ := concatSession(t, "a.mp4", "b.mp4")

	s.press("down", "space", "enter")
	if step, _ := s.model.currentStep(); step.ID != "clips" {
		t.Fatalf("one clip advanced the step to %q; want to stay on clips", step.ID)
	}
	if !s.model.noticeIsError || !strings.Contains(s.model.notice, "at least 2") {
		t.Errorf("notice = %q (error=%v), want a complaint about the clip count",
			s.model.notice, s.model.noticeIsError)
	}
}

func TestConcatGoingBackKeepsTheClips(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")

	s.press("down", "space", "down", "space", "enter") // two clips, then on to container
	s.press("esc")                                     // back to the clips

	if step, _ := s.model.currentStep(); step.ID != "clips" {
		t.Fatalf("after back: step = %q, want clips", step.ID)
	}
	// The answer is restored into the browser, so the ticks are back.
	want := tasks.FileList([]string{clipOf(dir, "a.mp4"), clipOf(dir, "b.mp4")})
	if got := s.model.answers["clips"]; got != want {
		t.Errorf("the answer was lost going back: %q", got)
	}
	if !strings.Contains(s.view(), "[x]") {
		t.Errorf("the restored clips are not ticked:\n%s", s.view())
	}
}

// TestConcatWarnsWhenTheStreamsDiffer covers the warning the plan asks for: the
// concat demuxer copies the streams, so a mismatch has to be said before the run
// rather than discovered as a failure (plan.md §5.9).
func TestConcatWarnsWhenTheStreamsDiffer(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")

	s.press("down", "space")
	s.press("down", "space")

	// Two clips that agree re-encode nothing, so there is nothing to warn about.
	s.send(
		concatProbeMsg{path: clipOf(dir, "a.mp4"), info: avInfo()},
		concatProbeMsg{path: clipOf(dir, "b.mp4"), info: avInfo()},
	)
	if got := s.model.concatWarning(); got != "" {
		t.Errorf("matching clips produced a warning: %q", got)
	}

	// Now a clip with a different picture codec.
	other := avInfo()
	other.Streams[0].CodecName = "hevc"
	s.send(concatProbeMsg{path: clipOf(dir, "b.mp4"), info: other})

	warning := s.model.concatWarning()
	if warning == "" {
		t.Fatal("differing picture codecs produced no warning")
	}
	if !strings.Contains(warning, "h264") || !strings.Contains(warning, "hevc") {
		t.Errorf("the warning does not name the codecs: %q", warning)
	}
	if !strings.Contains(s.view(), "do not all match") {
		t.Errorf("the warning is not on screen:\n%s", s.view())
	}
}

// TestConcatWarnsWhenOneClipHasNoSound is the case a codec comparison alone
// would miss: the streams are the same codec until one clip has none at all.
func TestConcatWarnsWhenOneClipHasNoSound(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	s.press("down", "space", "down", "space")

	silent := avInfo()
	silent.Streams = silent.Streams[:1] // drop the audio stream
	s.send(
		concatProbeMsg{path: clipOf(dir, "a.mp4"), info: avInfo()},
		concatProbeMsg{path: clipOf(dir, "b.mp4"), info: silent},
	)

	if got := s.model.concatWarning(); !strings.Contains(got, "sound stream") {
		t.Errorf("warning = %q, want it to mention the missing sound stream", got)
	}
}

func TestConcatOpensAFolderAndComesBack(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	sub := filepath.Join(dir, "more")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("making the subdirectory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "c.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the clip: %v", err)
	}
	s.model.setConcatDir(dir)

	// The folders come before the files, so "more" is one row past "..".
	s.press("down", "l")
	if got := s.model.concatDir; got != sub {
		t.Fatalf("after l: dir = %q, want %q", got, sub)
	}
	if !modelHasEntry(s.model, "c.mp4") {
		t.Errorf("the subdirectory's clip is not listed:\n%s", s.view())
	}

	s.press("h")
	if got := s.model.concatDir; got != dir {
		t.Errorf("after h: dir = %q, want %q", got, dir)
	}
}

// TestConcatWillNotAddAFolder: a folder cannot be a clip, and saying so beats a
// list that silently ignores the key.
func TestConcatWillNotAddAFolder(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	if err := os.Mkdir(filepath.Join(dir, "more"), 0o700); err != nil {
		t.Fatalf("making the subdirectory: %v", err)
	}
	s.model.setConcatDir(dir)

	s.press("down", "space") // "more" is the first entry after ".."
	if len(s.model.concatPicked) != 0 {
		t.Errorf("a folder was added to the clips: %v", s.model.concatPicked)
	}
	if !strings.Contains(s.model.notice, "folder") {
		t.Errorf("notice = %q, want it to say a folder cannot be a clip", s.model.notice)
	}
}

// TestConcatFilterNarrowsTheListing covers `/` (plan.md §6, Keyboard).
func TestConcatFilterNarrowsTheListing(t *testing.T) {
	s, _ := concatSession(t, "alpha.mp4", "beta.mp4")

	s.press("/")
	if !s.model.concatFiltering {
		t.Fatal("`/` did not open the filter")
	}
	s.press("b")
	if !strings.Contains(s.view(), "beta.mp4") {
		t.Errorf("the filter hid the matching file:\n%s", s.view())
	}
	if strings.Contains(s.view(), "alpha.mp4") {
		t.Errorf("the filter did not hide the other file:\n%s", s.view())
	}
	s.press("enter") // keep the filter, back to the list
	if s.model.concatFiltering {
		t.Error("enter did not leave the filter")
	}
}

func TestConcatFilterEscapeDropsIt(t *testing.T) {
	s, _ := concatSession(t, "alpha.mp4", "beta.mp4")
	s.press("/", "b", "esc")
	if s.model.concatFiltering {
		t.Error("esc did not leave the filter")
	}
	if got := s.model.concatFilter.Value(); got != "" {
		t.Errorf("the filter survived esc: %q", got)
	}
	if !strings.Contains(s.view(), "alpha.mp4") {
		t.Errorf("both files are not back after esc:\n%s", s.view())
	}
}

// TestConcatFilterOwnsTheLetters: with the filter focused, `q` is a letter
// rather than "quit", and `e` is a letter rather than "back" (DECISIONS D14).
func TestConcatFilterOwnsTheLetters(t *testing.T) {
	s, _ := concatSession(t, "alpha.mp4", "beta.mp4")
	s.press("/", "q", "e")
	if got := s.model.concatFilter.Value(); got != "qe" {
		t.Errorf("filter = %q, want \"qe\"", got)
	}
	if !s.model.textEntryFocused() {
		t.Error("textEntryFocused is false while the filter has focus")
	}
}

// TestConcatAnswerBecomesTheCommand: the clips replace the input, and the list
// ffmpeg reads is named rather than inlined (plan.md §5.9).
func TestConcatAnswerBecomesTheCommand(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	s.press("down", "space", "down", "space", "enter")
	// container defaults to MP4, so enter takes it.
	s.press("enter")

	text := s.commandText()
	if !strings.Contains(text, "-f concat") || !strings.Contains(text, "-safe 0") {
		t.Errorf("the command does not read the clip list:\n%s", text)
	}
	if !strings.Contains(text, "concat-") || !strings.Contains(text, ".txt") {
		t.Errorf("the command does not name a list file:\n%s", text)
	}
	for _, name := range []string{"a.mp4", "b.mp4"} {
		if strings.Contains(text, filepath.Join(dir, name)) {
			t.Errorf("the command names %s directly; the list should hold it:\n%s", name, text)
		}
	}

	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	arts := cmd.Artifacts()
	if len(arts) != 1 {
		t.Fatalf("artifacts = %v, want one list file", arts)
	}
	// The list holds both clips, in the order they were ticked.
	for _, name := range []string{"a.mp4", "b.mp4"} {
		if !strings.Contains(arts[0].Content, filepath.Join(dir, name)) {
			t.Errorf("the list does not name %s:\n%s", name, arts[0].Content)
		}
	}
}

// TestConcatWillNotJoinAFileWithItself is a guard on the whole point of the
// step: one file is a copy, not a join, and the browser cannot express that.
func TestConcatWillNotJoinAFileWithItself(t *testing.T) {
	s, _ := concatSession(t, "a.mp4")
	s.press("down", "space", "enter")
	if !strings.Contains(s.model.notice, "at least 2") {
		t.Errorf("notice = %q, want a complaint about the clip count", s.model.notice)
	}
}

// modelHasEntry reports whether the browser lists a name.
func modelHasEntry(m Model, name string) bool {
	for _, e := range m.concatEntries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// TestConcatProbeFailureIsSaidOutLoud: a clip that cannot be read means the
// warning cannot be given, and saying so is better than a silent pass.
func TestConcatProbeFailureIsSaidOutLoud(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	s.press("down", "space")

	// The test session has no ffprobe, so the command the browser runs fails.
	s.send(concatProbeMsg{path: clipOf(dir, "a.mp4"), err: os.ErrNotExist})
	if !s.model.noticeIsError {
		t.Errorf("a clip that could not be read produced no error, notice = %q", s.model.notice)
	}
	if _, ok := s.model.concatInfo[clipOf(dir, "a.mp4")]; ok {
		t.Error("a failed read recorded media information")
	}
}

// TestConcatWarningWaitsForEveryClip: a mismatch cannot be claimed before both
// clips have been read.
func TestConcatWarningWaitsForEveryClip(t *testing.T) {
	s, dir := concatSession(t, "a.mp4", "b.mp4")
	s.press("down", "space", "down", "space")

	s.send(concatProbeMsg{path: clipOf(dir, "a.mp4"), info: avInfo()})
	if got := s.model.concatWarning(); got != "" {
		t.Errorf("warning = %q before the second clip was read, want none", got)
	}
}

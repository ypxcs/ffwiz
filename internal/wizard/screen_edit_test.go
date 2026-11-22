package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// This file covers the review screen's edit mode and Save as script
// (plan.md §6.6, DECISIONS D44).

// typeText sends one key message per rune, the way a terminal would.
func (s *session) typeText(text string) {
	s.t.Helper()
	for _, r := range text {
		s.send(keyPress(string(r)))
	}
}

// editing opens the editor from the review screen and replaces the text,
// returning the session and the text that was put there.
func editing(t *testing.T, s *session, text string) {
	t.Helper()
	s.press("x")
	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want the edit screen", s.model.screen)
	}
	s.model.editArea.SetValue(text)
}

func TestWizardEditsTheCommand(t *testing.T) {
	s, _ := runnable(t)
	generated := s.commandText()
	if !strings.Contains(generated, "-c:a copy") {
		t.Fatalf("the generated command is not the expected default: %s", generated)
	}

	editing(t, s, strings.Replace(generated, "-c:a copy", "-an", 1))
	s.press("ctrl+s")

	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	if got := s.commandText(); !strings.Contains(got, "-an") || strings.Contains(got, "-c:a copy") {
		t.Errorf("the edited command did not take over: %s", got)
	}
	// The screen says the command is no longer the wizard's, rather than
	// showing a summary of answers that no longer describe it (D44).
	if view := s.view(); !strings.Contains(view, "edited by hand") {
		t.Errorf("the review screen does not say the command was edited:\n%s", view)
	}
}

// TestWizardEditorOwnsItsLetters: a text area is a text area. `q` must not quit
// and `e` must not go back, or the command could not contain either letter
// (DECISIONS D14).
func TestWizardEditorOwnsItsLetters(t *testing.T) {
	s, _ := runnable(t)
	s.press("x")
	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want the edit screen", s.model.screen)
	}
	before := s.model.editArea.Value()

	s.typeText("qex")
	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want to stay in the editor", s.model.screen)
	}
	want := before + "qex"
	if got := s.model.editArea.Value(); got != want {
		t.Errorf("the text area holds %q, want %q", got, want)
	}
}

func TestWizardRefusesAnEditThatChangesTheDestination(t *testing.T) {
	s, _ := runnable(t)
	generated := s.commandText()
	elsewhere := filepath.Join(filepath.Dir(s.model.outputPath), "somewhere-else.mp4")

	editing(t, s, strings.Replace(generated, s.model.outputPath, elsewhere, 1))
	s.press("ctrl+s")

	// The output screen owns the name, the overwrite question, the writability
	// check and the free space; an edit that retargets the file would leave all
	// of them describing something else (D44).
	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want to stay in the editor", s.model.screen)
	}
	if s.model.editErr == "" {
		t.Error("nothing explained why the edit was refused")
	}
	if !strings.Contains(s.view(), "still write to") {
		t.Errorf("the editor does not explain the refusal:\n%s", s.view())
	}
	if s.model.edited != nil {
		t.Error("the refused command was adopted anyway")
	}
}

func TestWizardRefusesAnEditWithABrokenQuote(t *testing.T) {
	s, _ := runnable(t)

	editing(t, s, "ffmpeg -i '"+s.model.input+" -c copy "+s.model.outputPath)
	s.press("ctrl+s")

	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want to stay in the editor", s.model.screen)
	}
	if !strings.Contains(s.view(), "unterminated quote") {
		t.Errorf("the editor does not name the problem:\n%s", s.view())
	}
}

func TestWizardRefusesAnEmptyEdit(t *testing.T) {
	s, _ := runnable(t)

	editing(t, s, "   ")
	s.press("ctrl+s")

	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want to stay in the editor", s.model.screen)
	}
	if !strings.Contains(s.view(), "nothing to run") {
		t.Errorf("the editor does not explain the empty command:\n%s", s.view())
	}
}

func TestWizardEscapingTheEditorKeepsTheBuiltCommand(t *testing.T) {
	s, _ := runnable(t)
	generated := s.commandText()

	editing(t, s, "ffmpeg -i whatever")
	s.press("esc")

	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	if s.model.edited != nil {
		t.Error("cancelling the editor adopted the text anyway")
	}
	if got := s.commandText(); got != generated {
		t.Errorf("command = %q, want the generated %q", got, generated)
	}
	if view := s.view(); strings.Contains(view, "edited by hand") {
		t.Error("the review screen still calls the command edited")
	}
}

func TestWizardUndoesTheEdit(t *testing.T) {
	s, _ := runnable(t)
	generated := s.commandText()

	editing(t, s, strings.Replace(generated, "-c:a copy", "-an", 1))
	s.press("ctrl+s")
	if s.model.edited == nil {
		t.Fatal("the edit was not adopted")
	}

	s.press("u")
	if s.model.edited != nil {
		t.Fatal("the edit was not dropped")
	}
	if got := s.commandText(); got != generated {
		t.Errorf("command = %q, want the generated %q", got, generated)
	}
	if !strings.Contains(s.view(), "back to the command") {
		t.Errorf("the screen does not confirm the undo:\n%s", s.view())
	}
}

// TestWizardRunsTheEditedCommand is the point of the whole screen: what runs is
// what the user typed, not what the wizard would have built.
func TestWizardRunsTheEditedCommand(t *testing.T) {
	s, runner := runnable(t)
	generated := s.commandText()

	editing(t, s, strings.Replace(generated, "-c:a copy", "-an", 1))
	s.press("ctrl+s")
	s.press("r")

	if runner.started != 1 {
		t.Fatalf("ffmpeg was started %d times, want once", runner.started)
	}
	if runner.command == nil {
		t.Fatal("no command was handed to ffmpeg")
	}
	if !runner.command.Edited() {
		t.Error("the command handed to ffmpeg was not the edited one")
	}
	args := strings.Join(runner.command.DisplayArgs(), " ")
	if !strings.Contains(args, "-an") {
		t.Errorf("the edited argument is missing: %s", args)
	}
	// The execution-only flags are still added, or there would be no progress.
	if !strings.Contains(strings.Join(runner.command.Args(), " "), "-progress pipe:1") {
		t.Errorf("progress reporting was lost: %v", runner.command.Args())
	}
}

// TestWizardOverwriteFollowsTheEditedText: -y in the text is the user's own
// answer to the overwrite question, in both directions (D28, D44).
func TestWizardOverwriteFollowsTheEditedText(t *testing.T) {
	s, _ := runnable(t)

	editing(t, s, strings.Replace(s.commandText(), "-n", "-y", 1))
	s.press("ctrl+s")
	if !s.model.overwrite {
		t.Fatal("the wizard did not pick -y up out of the edited command")
	}

	s.press("x")
	s.model.editArea.SetValue(strings.Replace(s.model.editArea.Value(), "-y", "-n", 1))
	s.press("ctrl+s")
	if s.model.overwrite {
		t.Error("the wizard kept -y after it was removed from the command")
	}
}

// TestWizardDropsTheEditWhenAnAnswerChanges: the edit was of a command built
// from the answers, so a changed answer takes the command back. Keeping the edit
// would mean a change the user just made is silently ignored (D44).
func TestWizardDropsTheEditWhenAnAnswerChanges(t *testing.T) {
	s, _ := runnable(t)

	editing(t, s, strings.Replace(s.commandText(), "-c:a copy", "-an", 1))
	s.press("ctrl+s")
	if s.model.edited == nil {
		t.Fatal("the edit was not adopted")
	}

	s.press("esc") // review -> output
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}
	s.press("esc") // output -> the last step
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the step screen", s.model.screen)
	}
	s.press("enter") // record the answer again
	if s.model.edited != nil {
		t.Error("the edited command survived an answer being recorded")
	}
	if got := s.commandText(); strings.Contains(got, "-an") {
		t.Errorf("the dropped edit is still in the command: %s", got)
	}
}

func TestWizardSavesAScript(t *testing.T) {
	s, _ := runnable(t)
	generated := s.commandText()

	s.press("s")

	path := scriptPath(s.model.outputPath)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the script: %v", err)
	}
	text := string(body)
	for _, want := range []string{"#!/bin/sh", "set -e", generated} {
		if !strings.Contains(text, want) {
			t.Errorf("the script does not contain %q:\n%s", want, text)
		}
	}
	// A script with a shebang is meant to be run.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the script is not executable: %v", info.Mode())
	}
	if !strings.Contains(s.view(), "wrote") {
		t.Errorf("the screen does not say the script was written:\n%s", s.view())
	}
}

// TestWizardSavesTheEditedScript: the script follows the command, edited or not.
func TestWizardSavesTheEditedScript(t *testing.T) {
	s, _ := runnable(t)

	editing(t, s, strings.Replace(s.commandText(), "-c:a copy", "-an", 1))
	s.press("ctrl+s")
	s.press("s")

	body, err := os.ReadFile(scriptPath(s.model.outputPath))
	if err != nil {
		t.Fatalf("reading the script: %v", err)
	}
	if !strings.Contains(string(body), "-an") {
		t.Errorf("the script does not carry the edit:\n%s", body)
	}
}

func TestWizardWillNotReplaceAnExistingScript(t *testing.T) {
	s, _ := runnable(t)
	path := scriptPath(s.model.outputPath)
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.press("s")

	if !strings.Contains(s.view(), "already exists") {
		t.Errorf("the screen does not explain why nothing was written:\n%s", s.view())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "mine\n" {
		t.Errorf("the existing script was replaced: %q", body)
	}
}

// TestWizardEditScreenFits: the editor must not widen the frame at the smallest
// terminal the plan requires (plan.md §6, 80x24).
func TestWizardEditScreenFits(t *testing.T) {
	dir := t.TempDir()
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: filepath.Join(dir, "a-very-long-input-file-name-for-a-clip.mp4"), info: avInfo()})
	next, _ := s.model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s.model = next.(Model)
	s.startTask("convert_video")
	s.acceptDefaults()
	s.press("enter") // the output screen's name
	s.press("x")
	if s.model.screen != ScreenEdit {
		t.Fatalf("screen = %v, want the edit screen", s.model.screen)
	}

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

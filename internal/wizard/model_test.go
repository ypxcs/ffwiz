package wizard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
)

// ── harness ─────────────────────────────────────────────────────────────────

// fakeClipboard records what was copied.
type fakeClipboard struct {
	text string
	err  error
}

func (f *fakeClipboard) WriteAll(text string) error {
	if f.err != nil {
		return f.err
	}
	f.text = text
	return nil
}

// session drives a model the way Bubble Tea would, without a terminal.
//
// Key messages are built by hand rather than through teatest, but they are the
// same KeyPressMsg values Bubble Tea's input parser produces: "enter", "esc" and
// "up" rather than " " or "\r", which is the part that actually goes wrong.
type session struct {
	t      *testing.T
	model  Model
	copied *fakeClipboard
}

func newSession(t *testing.T, deps Deps) *session {
	t.Helper()
	if deps.StartDir == "" {
		deps.StartDir = t.TempDir()
	}
	s := &session{t: t, model: New(deps)}
	if c, ok := deps.Clipboard.(*fakeClipboard); ok {
		s.copied = c
	}
	s.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return s
}

// send feeds messages to the model and runs any commands they produce, so a
// command that returns a message is followed through to the end.
func (s *session) send(msgs ...tea.Msg) {
	s.t.Helper()
	for _, msg := range msgs {
		next, cmd := s.model.Update(msg)
		m, ok := next.(Model)
		if !ok {
			s.t.Fatalf("Update returned %T, want wizard.Model", next)
		}
		s.model = m
		s.run(cmd, 0)
	}
}

// commandBudget bounds how long a command may take. The only command the wizard
// produces that waits is the text cursor's blink timer, which is not part of any
// flow: running it for real would make every test that touches a text field take
// half a second per keystroke.
const commandBudget = 25 * time.Millisecond

func (s *session) run(cmd tea.Cmd, depth int) {
	s.t.Helper()
	if cmd == nil || depth > 8 {
		return
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(commandBudget):
		return
	}
	switch m := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range m {
			s.run(c, depth+1)
		}
		return
	case tea.QuitMsg:
		return
	}
	next, cmd := s.model.Update(msg)
	if model, ok := next.(Model); ok {
		s.model = model
	}
	s.run(cmd, depth+1)
}

func (s *session) press(keys ...string) {
	s.t.Helper()
	for _, k := range keys {
		s.send(keyPress(k))
	}
}

// view is the rendered screen.
func (s *session) view() string { return s.model.View().Content }

// commandText is the command the wizard would run, as it would be typed.
func (s *session) commandText() string {
	s.t.Helper()
	cmd, _, err := s.model.liveCommand()
	if err != nil || cmd == nil {
		return ""
	}
	return cmd.String()
}

// keyPress builds the message Bubble Tea produces for a keystroke.
func keyPress(keystroke string) tea.KeyPressMsg {
	switch keystroke {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		runes := []rune(keystroke)
		if len(runes) != 1 {
			panic("keyPress: unhandled keystroke " + keystroke)
		}
		return tea.KeyPressMsg{Code: runes[0], Text: keystroke}
	}
}

// machineCaps is a typical Homebrew ffmpeg: libx264, libx265 and libvpx-vp9, but
// no libaom-av1 and no libass.
func machineCaps() ffmpeg.Capabilities {
	return ffmpeg.Capabilities{
		Encoders: map[string]string{
			"libx264": "x264", "libx265": "x265", "libvpx-vp9": "vp9",
			"aac": "AAC", "libopus": "Opus",
		},
		Muxers:       map[string]string{"mp4": "MP4", "matroska": "MKV", "webm": "WebM"},
		PixelFormats: map[string]struct{}{"yuv420p": {}},
		Filters:      map[string]struct{}{"scale": {}},
	}
}

// avInfo mimics the av.mp4 sample: two seconds of H.264 video and AAC audio.
func avInfo() *ffmpeg.MediaInfo {
	return &ffmpeg.MediaInfo{
		Path:   "av.mp4",
		Format: ffmpeg.FormatInfo{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 2 * time.Second, Size: 50685, BitRate: 203000},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamVideo, CodecName: "h264", Width: 320, Height: 240, PixelFormat: "yuv420p", FrameRate: 25},
			{Index: 1, Type: ffmpeg.StreamAudio, CodecName: "aac", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000},
		},
	}
}

// builtinSet loads the shipped catalogue.
func builtinSet(t *testing.T) *tasks.Set {
	t.Helper()
	set, err := tasks.Load(tasks.LoadOptions{UserDir: t.TempDir()})
	if err != nil {
		t.Fatalf("loading the built-in tasks: %v", err)
	}
	return set
}

// wizardAt opens a session with the shipped catalogue and a file already probed,
// which is where the wizard is after the input screen.
func wizardAt(t *testing.T, input string) *session {
	t.Helper()
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: &fakeClipboard{}})
	s.send(probeMsg{path: input, info: avInfo()})
	if s.model.screen != ScreenInput || !s.model.chosen {
		t.Fatalf("after probing: screen = %v, chosen = %v; want the file summary", s.model.screen, s.model.chosen)
	}
	return s
}

// ── the flow ────────────────────────────────────────────────────────────────

func TestWizardHappyPath(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)

	// The probe summary is shown before anything else, and the file's details
	// are on screen (plan.md §6.2).
	if view := s.view(); !strings.Contains(view, "container") || !strings.Contains(view, "h264 320x240") {
		t.Errorf("the input screen does not summarise the file:\n%s", view)
	}

	s.press("enter") // to the task list
	if s.model.screen != ScreenTask {
		t.Fatalf("screen = %v, want the task list", s.model.screen)
	}
	if view := s.view(); !strings.Contains(view, "Convert video to another format") {
		t.Errorf("the task list does not show the catalogue:\n%s", view)
	}

	s.press("enter") // start convert_video
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the first step", s.model.screen)
	}

	// Every question is answered with its default, so four enters walk the task.
	for i := 0; i < 4; i++ {
		if s.model.screen != ScreenStep {
			t.Fatalf("step %d: screen = %v, want a step", i, s.model.screen)
		}
		s.press("enter")
	}

	if s.model.screen != ScreenOutput {
		t.Fatalf("after the last step: screen = %v, want the output screen", s.model.screen)
	}
	want := filepath.Join(dir, "clip_convert_video.mp4")
	if s.model.outputPath != want {
		t.Errorf("default output = %q, want %q", s.model.outputPath, want)
	}

	s.press("enter") // accept the name
	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}

	// The review screen must show the answer in plain language, not just values.
	view := s.view()
	// The output is checked by file name: the full path of a temporary directory
	// is wider than the pane and is abbreviated to fit, while the exact path was
	// already asserted above.
	for _, wantSub := range []string{"Output format", "MP4", "Video codec", "H.264", filepath.Base(want)} {
		if !strings.Contains(view, wantSub) {
			t.Errorf("the review screen does not mention %q:\n%s", wantSub, view)
		}
	}

	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	got := cmd.String()
	wantCmd := "ffmpeg -n -i " + input + " -c:v libx264 -crf 23 -c:a copy " + want
	if got != wantCmd {
		t.Errorf("command =\n  %s\nwant\n  %s", got, wantCmd)
	}

	s.press("c")
	if s.copied.text == "" {
		t.Fatal("pressing c did not copy anything")
	}
	if s.copied.text != wantCmd {
		t.Errorf("copied %q, want the display command %q", s.copied.text, wantCmd)
	}
	if strings.Contains(s.copied.text, "-nostdin") || strings.Contains(s.copied.text, "-progress") {
		t.Errorf("the copied command carries the execution-only flags: %q", s.copied.text)
	}
}

func TestWizardWebMSwapsCodecsAndSteps(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter") // input summary, task list

	// Choose WebM: H.264 cannot be muxed into it, so the codec list must shrink
	// and the quality question must follow VP9.
	s.press("down", "down", "enter")

	step, ok := s.model.currentStep()
	if !ok {
		t.Fatal("no current step")
	}
	if step.ID != "vcodec" {
		t.Fatalf("current step = %q, want vcodec", step.ID)
	}
	shown := s.model.resolution().Shown["vcodec"]
	if len(shown) != 1 || shown[0].Value != "vp9" {
		t.Fatalf("offered codecs for WebM = %+v, want only VP9", shown)
	}
	// The hidden ones must be explained, not silently gone.
	if view := s.view(); !strings.Contains(view, "H.264") {
		t.Errorf("the step screen does not say which codecs were withheld:\n%s", view)
	}

	s.press("enter", "enter", "enter") // VP9, quality, audio
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen; steps were %v",
			s.model.screen, stepIDs(s.model.activeSteps()))
	}

	wantSteps := "container,vcodec,quality_vp9,audio"
	if got := strings.Join(stepIDs(s.model.activeSteps()), ","); got != wantSteps {
		t.Errorf("active steps = %s, want %s", got, wantSteps)
	}

	s.press("enter") // accept the name
	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	want := "ffmpeg -n -i " + input + " -c:v libvpx-vp9 -b:v 0 -crf 32 -c:a libopus -b:a 128k " +
		filepath.Join(dir, "clip_convert_video.webm")
	if got := cmd.String(); got != want {
		t.Errorf("command =\n  %s\nwant\n  %s", got, want)
	}
}

func TestWizardGoingBackKeepsAnswersAndRecomputes(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter") // summary, task list

	// container, codec=H.265, quality=Small file, audio.
	//
	// The quality cursor starts on the default that would be used (Balanced), so
	// a single step down reaches Small file.
	s.press("enter")         // container: MP4
	s.press("down", "enter") // vcodec: H.265
	s.press("down", "enter") // quality_h265: Small file
	s.press("enter")         // audio: keep as is

	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	// Go all the way back to the codec question and return, twice. The answers
	// given on the way through must still be there.
	s.press("esc") // back to the audio step
	s.press("esc") // back to quality
	s.press("esc") // back to vcodec
	step, _ := s.model.currentStep()
	if step.ID != "vcodec" {
		t.Fatalf("after going back: step = %q, want vcodec", step.ID)
	}
	if got := s.model.resolution().Answers["vcodec"]; got != "h265" {
		t.Errorf("vcodec = %q after going back, want the answer to survive", got)
	}

	s.press("enter", "enter", "enter") // forward again without changing anything
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v after returning, want the output screen", s.model.screen)
	}

	s.press("enter")
	cmd, _, err := s.model.liveCommand()
	if err != nil {
		t.Fatalf("liveCommand: %v", err)
	}
	want := "ffmpeg -n -i " + input + " -c:v libx265 -crf 32 -c:a copy " +
		filepath.Join(dir, "clip_convert_video.mp4")
	if got := cmd.String(); got != want {
		t.Errorf("command =\n  %s\nwant\n  %s", got, want)
	}
}

func TestWizardChangingAnAnswerDropsTheStepsItRuledOut(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.press("enter", "enter")

	// Answer everything for MP4/H.264, then go back and switch the container to
	// WebM. The H.264-specific steps must disappear rather than linger with
	// stale answers.
	s.press("enter", "enter", "enter", "enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	s.press("esc", "esc", "esc", "esc") // audio -> quality -> vcodec -> container
	step, _ := s.model.currentStep()
	if step.ID != "container" {
		t.Fatalf("step = %q, want container", step.ID)
	}
	s.press("down", "down", "enter") // WebM

	got := strings.Join(stepIDs(s.model.activeSteps()), ",")
	if got != "container,vcodec,quality_vp9,audio" {
		t.Errorf("active steps = %s, want the WebM chain", got)
	}
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the next step after the change", s.model.screen)
	}
}

func TestWizardAcceptsDataFromTheCommandLine(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	caps := machineCaps()

	m := New(Deps{Set: builtinSet(t), Caps: &caps, Input: input, StartDir: dir})
	// Init must ask for the background colour and start probing, and nothing
	// else: the picker is not used when a file was given.
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init returned no command")
	}
	if !m.probing {
		t.Error("the model is not in its loading state before the probe returns")
	}
	if view := m.View().Content; !strings.Contains(view, "Probing") {
		t.Errorf("the loading state does not say what it is doing:\n%s", view)
	}

	s := &session{t: t, model: m}
	s.send(probeMsg{path: input, info: avInfo()})
	if s.model.screen != ScreenInput || !s.model.chosen {
		t.Fatalf("screen = %v after probing an argument, want the summary", s.model.screen)
	}
}

// ── the rules the wizard must not break ─────────────────────────────────────

// atOutput runs the happy path up to the output screen and makes the default
// name exist, which is the state every overwrite test starts from.
func atOutput(t *testing.T) (*session, string) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter", "enter", "enter", "enter", "enter")

	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	existing := filepath.Join(dir, "clip_convert_video.mp4")
	mustWriteFile(t, existing)
	return s, existing
}

// TestWizardAsksBeforeOverwriting: accepting a name that exists must ask, and
// the answer must be what puts -y in the command (plan.md §6.5, §8).
func TestWizardAsksBeforeOverwriting(t *testing.T) {
	s, _ := atOutput(t)

	s.press("enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen with the question", s.model.screen)
	}
	if !s.model.conflict {
		t.Fatal("the overwrite question was not opened")
	}
	for _, want := range []string{"already exists", "Choose another name", "Overwrite it", "Cancel"} {
		if !strings.Contains(s.view(), want) {
			t.Errorf("the question does not offer %q:\n%s", want, s.view())
		}
	}
	// The cursor starts on the answer that cannot lose anything.
	if s.model.conflictIdx != 0 {
		t.Errorf("the cursor starts on option %d, want the safe one", s.model.conflictIdx)
	}
	if s.model.overwrite {
		t.Error("opening the question already agreed to overwrite")
	}
	if strings.Contains(s.commandText(), "-y") {
		t.Errorf("the command already overwrites: %s", s.commandText())
	}

	// Overwrite it, which is the second option.
	s.press("down", "enter")
	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	if !s.model.overwrite {
		t.Error("choosing Overwrite did not set the overwrite flag")
	}
	if !strings.Contains(s.commandText(), "-y") {
		t.Errorf("the command does not overwrite: %s", s.commandText())
	}
	if strings.Contains(s.commandText(), "-n") {
		t.Errorf("the command still says -n: %s", s.commandText())
	}
	if !strings.Contains(s.view(), "will be replaced") {
		t.Errorf("the review screen does not warn about replacing the file:\n%s", s.view())
	}
}

// TestWizardChoosingAnotherNameLeavesOverwriteOff: the safe answer returns to
// the name, and the command stays -n.
func TestWizardChoosingAnotherNameLeavesOverwriteOff(t *testing.T) {
	s, _ := atOutput(t)

	s.press("enter") // open the question
	s.press("enter") // Choose another name
	if s.model.conflict {
		t.Error("choosing another name did not close the question")
	}
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen", s.model.screen)
	}

	s.model.outputInput.SetValue(filepath.Join(filepath.Dir(s.model.outputPath), "other.mp4"))
	s.press("enter")
	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen after renaming", s.model.screen)
	}
	if s.model.overwrite || strings.Contains(s.commandText(), "-y") {
		t.Errorf("a new name still overwrites: %s", s.commandText())
	}
}

// TestWizardCancellingTheOverwriteQuestion goes back to the last question
// without touching the file.
func TestWizardCancellingTheOverwriteQuestion(t *testing.T) {
	s, _ := atOutput(t)

	s.press("enter")        // open the question
	s.press("down", "down") // Cancel
	s.press("enter")
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the last question", s.model.screen)
	}
	if s.model.overwrite {
		t.Error("cancelling set the overwrite flag")
	}
}

// TestWizardEscDoesNotOverwrite: the escape key closes the question and returns
// to the name field. That means the destructive answer can only ever be reached
// by pressing enter on it deliberately (DECISIONS D28).
func TestWizardEscDoesNotOverwrite(t *testing.T) {
	s, _ := atOutput(t)

	s.press("enter") // open the question
	s.press("down")  // move onto Overwrite it
	s.press("esc")   // ... and back out without choosing it

	if s.model.conflict {
		t.Error("esc did not close the question")
	}
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}
	if s.model.overwrite {
		t.Error("esc on the Overwrite option agreed to overwrite")
	}
}

// TestWizardAcceptsOnlyAPlaceItCanWrite: a folder is not a name that can be
// replaced, and a folder that does not exist is refused before a process is
// started that would fail at the last moment (plan.md §8).
func TestWizardAcceptsOnlyAPlaceItCanWrite(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter", "enter", "enter", "enter", "enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	folder := filepath.Join(dir, "a folder.mp4")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	s.model.outputInput.SetValue(folder)
	s.press("enter")
	if s.model.conflict {
		t.Error("the overwrite question was offered for a folder")
	}
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen", s.model.screen)
	}
	if !strings.Contains(s.view(), "is a folder") {
		t.Errorf("the screen does not explain that it is a folder:\n%s", s.view())
	}

	s.model.outputInput.SetValue(filepath.Join(dir, "no", "such", "place.mp4"))
	s.press("enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen", s.model.screen)
	}
	if !strings.Contains(s.view(), "does not exist") {
		t.Errorf("the screen does not explain the missing folder:\n%s", s.view())
	}
}

// TestWizardRefusesAFolderItCannotWriteTo: plan.md §8 asks for the folder to be
// writable, not merely present, and the check happens before ffmpeg is started.
func TestWizardRefusesAFolderItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only folder, so the check cannot fail")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter", "enter", "enter", "enter", "enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	s.model.outputInput.SetValue(filepath.Join(locked, "out.mp4"))
	s.press("enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen", s.model.screen)
	}
	if !strings.Contains(s.view(), "cannot be written to") {
		t.Errorf("the screen does not explain the unwritable folder:\n%s", s.view())
	}
	if names, err := os.ReadDir(locked); err != nil || len(names) != 0 {
		t.Errorf("the check left something behind in the folder: %v %v", names, err)
	}
}

// TestWizardWillNotStartWhenTheOutputAppeared: a file created after the review// screen must not be overwritten by a run, and -n would silently do nothing
// (DECISIONS D9), so the wizard asks again instead.
func TestWizardWillNotStartWhenTheOutputAppeared(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter")

	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	// Something else creates the file between the review screen and the run.
	mustWriteFile(t, s.model.outputPath)

	s.press("r")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to be sent back to the output screen", s.model.screen)
	}
	if !s.model.conflict {
		t.Error("the overwrite question was not opened for the file that appeared")
	}
	if s.model.handle != nil {
		t.Error("a run was started anyway")
	}
}

func TestWizardRefusesTheInputAsOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	s := wizardAt(t, input)
	s.press("enter", "enter", "enter", "enter", "enter", "enter")

	s.model.outputInput.SetValue(input)
	s.press("enter")
	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want to stay on the output screen", s.model.screen)
	}
	if !strings.Contains(s.view(), "input file") {
		t.Errorf("the screen does not say why the name is refused:\n%s", s.view())
	}
}

func TestWizardRefusesATaskTheInputCannotSupport(t *testing.T) {
	audioOnly := &ffmpeg.MediaInfo{
		Path:    "song.mp3",
		Format:  ffmpeg.FormatInfo{Duration: 3 * time.Second},
		Streams: []ffmpeg.StreamInfo{{Index: 0, Type: ffmpeg.StreamAudio, CodecName: "mp3"}},
	}
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps})
	s.send(probeMsg{path: "song.mp3", info: audioOnly})
	s.press("enter") // summary -> task list

	if s.model.screen != ScreenTask {
		t.Fatalf("screen = %v, want the task list", s.model.screen)
	}
	if view := s.view(); !strings.Contains(view, "video stream") {
		t.Errorf("the task list does not explain why convert_video is unavailable:\n%s", view)
	}

	s.press("enter")
	if s.model.screen != ScreenTask {
		t.Fatalf("screen = %v, want to stay on the task list", s.model.screen)
	}
	if s.model.notice == "" {
		t.Error("selecting an impossible task produced no explanation")
	}
}

func TestWizardLettersDoNotTriggerActionsInTextFields(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.press("enter", "enter", "enter", "enter", "enter", "enter")

	if s.model.screen != ScreenOutput {
		t.Fatalf("screen = %v, want the output screen", s.model.screen)
	}

	// "e" is the back key everywhere else, but inside the path field it is a
	// letter, and "q" must not quit (DECISIONS D14).
	before := s.model.outputInput.Value()
	s.press("e", "q", "x")
	after := s.model.outputInput.Value()
	if !strings.HasSuffix(after, "eqx") {
		t.Errorf("typing into the output field produced %q, want it to end with eqx (started at %q)", after, before)
	}
	if s.model.screen != ScreenOutput {
		t.Errorf("typing changed the screen to %v", s.model.screen)
	}
}

func TestWizardCopyWithoutAClipboardExplainsItself(t *testing.T) {
	dir := t.TempDir()
	caps := machineCaps()
	// No Clipboard in Deps, which is what a caller that cannot offer one passes.
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps})
	s.send(probeMsg{path: filepath.Join(dir, "clip.mp4"), info: avInfo()})
	s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter", "c")

	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	if !strings.Contains(s.view(), "no clipboard") {
		t.Errorf("a failed copy is not reported:\n%s", s.view())
	}
	if s.model.copied {
		t.Error("a failed copy was recorded as successful")
	}
}

func TestWizardCopyFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	clip := &fakeClipboard{err: errors.New("clipboard is full")}
	caps := machineCaps()
	s := newSession(t, Deps{Set: builtinSet(t), Caps: &caps, Clipboard: clip})
	s.send(probeMsg{path: filepath.Join(dir, "clip.mp4"), info: avInfo()})
	s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter", "c")

	if !strings.Contains(s.view(), "clipboard is full") {
		t.Errorf("the clipboard error was swallowed:\n%s", s.view())
	}
}

func TestWizardQuitKeys(t *testing.T) {
	dir := t.TempDir()
	for _, keystroke := range []string{"q", "ctrl+c"} {
		t.Run(keystroke, func(t *testing.T) {
			s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
			_, cmd := s.model.Update(keyPress(keystroke))
			if cmd == nil {
				t.Fatalf("%q did not quit", keystroke)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("%q produced %T, want a quit", keystroke, cmd())
			}
		})
	}
}

// TestReviewAlignsTheAnswerColumn checks the review list's label column.
//
// The screen is a two-column list of labels and answers, and a row whose answer
// starts one column out from the rest is hard to scan. This is asserted on the
// rendered text rather than eyeballed because the padding is computed from the
// *visible* width of an already-styled string, which is the step that goes wrong.
func TestReviewAlignsTheAnswerColumn(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "clip.mp4"))
	s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter")

	// The label column is 18 wide, plus the space before the answer, so every
	// answer begins in the same place no matter how long its label is.
	want := []string{
		"  Output format:     MP4",
		"  Video codec:       H.264 (most compatible)",
		"  Quality:           Balanced (recommended)",
		"  Audio:             Keep as is (no re-encode)",
	}

	got := stripANSI(s.view())
	for _, row := range want {
		if !strings.Contains(got, row) {
			t.Errorf("the review screen does not contain %q:\n%s", row, got)
		}
	}
}

// ── View must never panic ───────────────────────────────────────────────────

// TestViewIsTotal walks every screen, including the states that exist for only
// an instant, and renders it. A nil dereference in View is invisible in unit
// tests and fatal in a terminal (DECISIONS D18).
func TestViewIsTotal(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "clip.mp4")
	caps := machineCaps()
	set := builtinSet(t)

	// A task with an input step, so the text-entry branch is covered too: the
	// shipped catalogue has no input steps until the trim task arrives.
	inputSet := loadTestSet(t, `
- id: trim
  label: Trim
  description: Cut a piece out.
  requires: { video: true }
  steps:
    - id: start
      prompt: Start time
      hint: Where to begin.
      type: input
      placeholder: "0:00"
      input_template: ["-ss", "{value}"]
      validate: { regex: "^[0-9:.]+$" }
    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, default: true }
        - { label: MKV, value: mkv, output_ext: mkv }
`)

	tests := []struct {
		name string
		want []string
		// set overrides the catalogue; set is nil for the shipped one.
		set *tasks.Set
		// unprobed leaves the wizard on the file picker, before any file is read.
		unprobed bool
		run      func(s *session)
	}{
		{
			name:     "picker",
			want:     []string{"ffwiz", "Choose a file", "Input", "command"},
			unprobed: true,
			run:      func(s *session) {},
		},
		{
			name: "probing",
			want: []string{"ffwiz", "Probing"},
			run:  func(s *session) { s.model.probing = true },
		},
		{
			name: "probe failed",
			want: []string{"could not read"},
			run: func(s *session) {
				s.send(probeMsg{path: "/nope.mp4", err: errors.New("no such file")})
			},
		},
		{
			name: "file summary",
			want: []string{"This file", "container", "video #0"},
			run:  func(s *session) {},
		},
		{
			name: "task list",
			want: []string{"What do you want to do?"},
			run:  func(s *session) { s.press("enter") },
		},
		{
			name: "choice step",
			want: []string{"Output format", "MP4", "command"},
			run:  func(s *session) { s.press("enter", "enter") },
		},
		{
			name: "input step",
			want: []string{"Start time", "1/2", "Where to begin."},
			set:  inputSet,
			run:  func(s *session) { s.press("enter", "enter") },
		},
		{
			name: "output",
			want: []string{"Where should the result go?"},
			run:  func(s *session) { s.press("enter", "enter", "enter", "enter", "enter", "enter") },
		},
		{
			name: "review",
			want: []string{"Ready to go", "Output format", "press r to run it"},
			run:  func(s *session) { s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter") },
		},
		{
			name: "review with an unanswered step",
			want: []string{"Ready to go", "still unanswered: start", "not answered", "<start>"},
			set:  inputSet,
			run: func(s *session) {
				// A step with no default leaves the command incomplete, and the
				// preview must say so rather than show a command that cannot run.
				s.press("enter", "enter") // task list, then the trim task
				s.model.answers = map[string]string{"container": "mp4"}
				s.model.outputPath = filepath.Join(dir, "out.mp4")
				s.model.screen = ScreenReview
			},
		},
		{
			// The run screen has to render before the process exists, while it
			// runs, and when its length is unknown (DECISIONS D18).
			name: "run starting",
			want: []string{"Encoding", "starting ffmpeg"},
			run: func(s *session) {
				s.model.screen = ScreenRun
				s.model.starting = true
			},
		},
		{
			name: "run with progress",
			want: []string{"Encoding", "50%", "elapsed 0:03", "about 0:01 left", "184 kbit/s"},
			run: func(s *session) {
				// Walk to the review screen first: the progress bar needs the
				// command, and the expected duration comes from it.
				s.press("enter", "enter", "enter", "enter", "enter", "enter", "enter")
				s.model.screen = ScreenRun
				s.model.elapsed = 3 * time.Second
				s.model.progress = ffmpeg.Progress{OutTime: time.Second, Speed: 2, TotalSize: 2048, Bitrate: 183600}
			},
		},
		{
			name: "run with an unknown length",
			want: []string{"length unknown", "elapsed"},
			run: func(s *session) {
				// No task was ever chosen, so there is no command and no length
				// to measure progress against.
				s.model.screen = ScreenRun
			},
		},
		{
			name: "run with the log open",
			want: []string{"frame= 1", "fps=0.0"},
			run: func(s *session) {
				s.model.screen = ScreenRun
				s.model.logs = []string{"frame= 1 fps=0.0"}
				s.model.showLog = true
			},
		},
		{
			name: "run asking to stop",
			want: []string{"Stop the encode?"},
			run: func(s *session) {
				s.model.screen = ScreenRun
				s.model.askingToStop = true
			},
		},
		{
			name: "done succeeded",
			want: []string{"Done", "Size:", "open the folder"},
			run: func(s *session) {
				s.model.screen = ScreenDone
				s.model.outcome = ffmpeg.OutcomeSucceeded
				s.model.outputPath = filepath.Join(dir, "out.mp4")
				mustWriteFile(t, s.model.outputPath)
			},
		},
		{
			name: "done stopped",
			want: []string{"Stopped", "shorter than intended", "press d to delete it"},
			run: func(s *session) {
				s.model.screen = ScreenDone
				s.model.outcome = ffmpeg.OutcomeCancelled
				s.model.outputPath = filepath.Join(dir, "out.mp4")
				mustWriteFile(t, s.model.outputPath)
			},
		},
		{
			name: "done failed",
			want: []string{"It did not work", "the end of ffmpeg's log", "Unknown encoder"},
			run: func(s *session) {
				s.model.screen = ScreenDone
				s.model.outcome = ffmpeg.OutcomeFailed
				s.model.hintText = "this ffmpeg build does not have a codec the task asked for"
				s.model.outputPath = filepath.Join(dir, "out.mp4")
				s.model.result = ffmpeg.Result{ExitCode: 1, Stderr: []string{"Unknown encoder 'libnope'"}}
			},
		},
		{
			name: "full help",
			want: []string{"ffwiz"},
			run:  func(s *session) { s.press("?") },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useSet := set
			if tc.set != nil {
				useSet = tc.set
			}
			s := newSession(t, Deps{Set: useSet, Caps: &caps, StartDir: dir})
			if !tc.unprobed {
				s.send(probeMsg{path: input, info: avInfo()})
			}
			tc.run(s)

			var content string
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("View panicked on the %s screen: %v", tc.name, r)
					}
				}()
				content = s.view()
			}()

			for _, want := range tc.want {
				if !strings.Contains(content, want) {
					t.Errorf("the %s screen does not contain %q:\n%s", tc.name, want, content)
				}
			}
		})
	}
}

// TestViewAtEightyColumns is the smallest terminal plan.md §6 requires the
// wizard to survive.
func TestViewAtEightyColumns(t *testing.T) {
	dir := t.TempDir()
	s := wizardAt(t, filepath.Join(dir, "a-file-with-a-fairly-long-name.mp4"))
	s.press("enter", "enter")

	next, _ := s.model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s.model = next.(Model)

	content := s.view()
	lines := strings.Split(content, "\n")
	if len(lines) > 24 {
		t.Errorf("the view is %d lines tall, which does not fit 24 rows:\n%s", len(lines), content)
	}
	for _, line := range lines {
		if w := visibleLen(line); w > 80 {
			t.Errorf("a line is %d columns wide, which overflows 80:\n%s", w, line)
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func stepIDs(steps []tasks.Step) []string {
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadTestSet(t *testing.T, yaml string) *tasks.Set {
	t.Helper()
	fsys := fstest.MapFS{"builtin/tasks.yaml": &fstest.MapFile{Data: []byte(yaml)}}
	set, err := tasks.Load(tasks.LoadOptions{BuiltinFS: fsys, UserDir: t.TempDir()})
	if err != nil {
		t.Fatalf("loading the test tasks: %v", err)
	}
	return set
}

package wizard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// ── a fake ffmpeg ───────────────────────────────────────────────────────────

// fakeRunner stands in for ffmpeg.Start. Nothing is spawned: the test decides
// what the process reports, which is how progress, cancellation and failure are
// exercised deterministically (plan.md §3, rule 4 - the UI must be testable
// without a terminal or a process).
type fakeRunner struct {
	events chan ffmpeg.Event
	// path and command record what the wizard asked for.
	path    string
	command *ffmpeg.Command
	// startErr, when set, is what ffmpeg.Start would have failed with.
	startErr error
	// started is how many times a run was requested.
	started int
	// cancelled records that Cancel was called.
	cancelled bool
}

func (f *fakeRunner) start(_ context.Context, ffmpegPath string, cmd *ffmpeg.Command, _ ffmpeg.RunOptions) (RunHandle, error) {
	f.started++
	f.path = ffmpegPath
	f.command = cmd
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.events == nil {
		// Deep enough that no test blocks writing an event.
		f.events = make(chan ffmpeg.Event, 32)
	}
	return f, nil
}

func (f *fakeRunner) Events() <-chan ffmpeg.Event { return f.events }
func (f *fakeRunner) Cancel()                     { f.cancelled = true }

// push sends one event to the model, as the real event reader would. The Done
// event closes the fake stream, exactly as ffmpeg.Run closes its own after the
// final event.
func (s *session) push(runner *fakeRunner, ev ffmpeg.Event) {
	s.t.Helper()
	s.send(runEventMsg{ev: ev, ch: runner.events})
	if ev.Done {
		close(runner.events)
	}
}

// runnable is a wizard sitting on the review screen with a fake ffmpeg and a
// fake file manager behind it.
func runnable(t *testing.T) (*session, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
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
	s.press("enter") // to the task list
	s.startTask("convert_video")
	s.press("enter", "enter", "enter", "enter", "enter") // the four steps, then the output screen
	if s.model.screen != ScreenReview {
		t.Fatalf("screen = %v, want the review screen", s.model.screen)
	}
	return s, runner
}

// ── starting a run ──────────────────────────────────────────────────────────

func TestWizardStartsTheRun(t *testing.T) {
	s, runner := runnable(t)

	s.press("r")
	if s.model.screen != ScreenRun {
		t.Fatalf("screen = %v, want the run screen", s.model.screen)
	}
	if runner.started != 1 {
		t.Fatalf("ffmpeg was started %d times, want once", runner.started)
	}
	if runner.path != "/fake/ffmpeg" {
		t.Errorf("ffmpeg path = %q, want the located binary", runner.path)
	}
	if runner.command == nil {
		t.Fatal("no command was handed to ffmpeg")
	}
	if !strings.Contains(runner.command.String(), s.model.outputPath) {
		t.Errorf("the command does not write %s: %s", s.model.outputPath, runner.command.String())
	}
	if runner.cancelled {
		t.Error("the run was cancelled as soon as it started")
	}
	if !strings.Contains(s.view(), "Encoding") {
		t.Errorf("the run screen does not say it is encoding:\n%s", s.view())
	}
}

func TestWizardWillNotRunWithUnansweredSteps(t *testing.T) {
	caps := machineCaps()
	runner := &fakeRunner{}
	s := newSession(t, Deps{
		Set:       builtinSet(t),
		Caps:      &caps,
		Clipboard: &fakeClipboard{},
		Tools:     ffmpeg.Tools{FFmpeg: "/fake/ffmpeg"},
		Run:       runner.start,
	})
	s.send(probeMsg{path: filepath.Join(t.TempDir(), "clip.mp4"), info: avInfo()})
	// Leave the task unstarted: there is no engine, so there is no command.
	s.model.screen = ScreenReview
	s.press("r")

	if runner.started != 0 {
		t.Error("a run was started with no command behind it")
	}
	if s.model.screen == ScreenRun {
		t.Error("the wizard moved to the run screen with nothing to run")
	}
}

func TestWizardReportsWhenFFmpegCannotStart(t *testing.T) {
	s, runner := runnable(t)
	runner.startErr = context.DeadlineExceeded

	// The failure comes back as a message, so the run screen is left
	// immediately for the done screen rather than spinning for ever.
	s.press("r")
	if s.model.screen != ScreenDone {
		t.Fatalf("screen = %v, want the done screen", s.model.screen)
	}
	if s.model.outcome != ffmpeg.OutcomeFailed {
		t.Errorf("outcome = %v, want failed", s.model.outcome)
	}
	if !strings.Contains(s.view(), "starting ffmpeg") {
		t.Errorf("the failure does not mention starting ffmpeg:\n%s", s.view())
	}
}

// ── progress ────────────────────────────────────────────────────────────────

func TestWizardRunShowsProgress(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")

	s.push(runner, ffmpeg.Event{Progress: &ffmpeg.Progress{
		OutTime: time.Second, Speed: 2, TotalSize: 2048, Bitrate: 183600,
	}})

	// av.mp4 is two seconds long, so one second of output is half the job.
	view := s.view()
	if !strings.Contains(view, "50%") {
		t.Errorf("the run screen does not show the percentage:\n%s", view)
	}
	if !strings.Contains(view, "0:01") {
		t.Errorf("the run screen does not show the remaining time:\n%s", view)
	}
	if !strings.Contains(view, "184 kbit/s") {
		t.Errorf("the run screen does not show the bit rate:\n%s", view)
	}
	if !strings.Contains(view, "2.0 KiB written") {
		t.Errorf("the run screen does not show the bytes written:\n%s", view)
	}
	if s.model.progress.OutTime != time.Second {
		t.Errorf("the model kept %v, want the reported 1s", s.model.progress.OutTime)
	}
}

func TestWizardRunLogCanBeShown(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	s.push(runner, ffmpeg.Event{Log: "frame= 1 fps=0.0 q=28.0 size=0kB time=00:00:00.04"})

	// The log is hidden behind a key: the run screen is about the progress.
	if strings.Contains(s.view(), "fps=0.0") {
		t.Errorf("the log is on screen before it was asked for:\n%s", s.view())
	}
	s.press("l")
	if !strings.Contains(s.view(), "fps=0.0") {
		t.Errorf("pressing l did not reveal the log:\n%s", s.view())
	}
}

// ── stopping ────────────────────────────────────────────────────────────────

func TestWizardCancelAsksBeforeStopping(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")

	s.press("q")
	if !s.model.askingToStop {
		t.Fatal("q did not ask before stopping the encode")
	}
	if runner.cancelled {
		t.Error("the first q stopped the encode without asking")
	}
	if s.model.screen != ScreenRun {
		t.Fatalf("screen = %v, want to still be running", s.model.screen)
	}

	// Any other key carries on.
	s.press("x")
	if s.model.askingToStop {
		t.Error("an unrelated key did not dismiss the question")
	}
	if runner.cancelled {
		t.Error("dismissing the question stopped the encode")
	}

	// q twice in a row stops it.
	s.press("q", "q")
	if !runner.cancelled {
		t.Error("confirming did not stop the process")
	}
	if s.model.screen != ScreenDone {
		t.Fatalf("screen = %v, want the done screen", s.model.screen)
	}
	if s.model.outcome != ffmpeg.OutcomeCancelled {
		t.Errorf("outcome = %v, want cancelled", s.model.outcome)
	}
}

func TestWizardOffersToDeleteThePartialFile(t *testing.T) {
	s, _ := runnable(t)
	s.press("r")
	partial := s.model.outputPath
	if err := os.WriteFile(partial, []byte("half a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.press("q", "q")
	if !strings.Contains(s.view(), "press d to delete it") {
		t.Errorf("the done screen does not offer to delete the partial file:\n%s", s.view())
	}

	s.press("d")
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("the partial file is still there: %v", err)
	}
	if !s.model.removed {
		t.Error("the model did not record the deletion")
	}
	if strings.Contains(s.view(), "press d to delete it") {
		t.Errorf("the file is offered for deletion twice:\n%s", s.view())
	}
}

func TestWizardDeleteIsOfferedOnlyAfterAStop(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	if err := os.WriteFile(s.model.outputPath, []byte("a real output"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{ExitCode: 0}})

	if s.model.outcome != ffmpeg.OutcomeSucceeded {
		t.Fatalf("outcome = %v, want succeeded", s.model.outcome)
	}
	s.press("d")
	if _, err := os.Stat(s.model.outputPath); err != nil {
		t.Errorf("d deleted a finished file: %v", err)
	}
}

// ── finishing ───────────────────────────────────────────────────────────────

func TestWizardRunSuccessReportsTheSize(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	if err := os.WriteFile(s.model.outputPath, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{ExitCode: 0}})

	if s.model.screen != ScreenDone {
		t.Fatalf("screen = %v, want the done screen", s.model.screen)
	}
	if s.model.outcome != ffmpeg.OutcomeSucceeded {
		t.Fatalf("outcome = %v, want succeeded (%s)", s.model.outcome, s.model.hintText)
	}
	view := s.view()
	if !strings.Contains(view, "Done") {
		t.Errorf("the done screen does not say it is done:\n%s", view)
	}
	if !strings.Contains(view, "4.0 KiB") {
		t.Errorf("the done screen does not report the size:\n%s", view)
	}
	// The input is 50 MiB in the fixture, so 4 KiB is a lot smaller.
	if !strings.Contains(view, "smaller than the input") {
		t.Errorf("the done screen does not compare with the input:\n%s", view)
	}
}

func TestWizardRunFailureShowsTheHintAndLog(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	s.push(runner, ffmpeg.Event{Log: "Unknown encoder 'libnope'"})
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{
		Err:      errors.New("exit status 1"),
		ExitCode: 1,
		Stderr:   []string{"Stream mapping:", "Unknown encoder 'libnope'"},
	}})

	if s.model.screen != ScreenDone {
		t.Fatalf("screen = %v, want the done screen", s.model.screen)
	}
	if s.model.outcome != ffmpeg.OutcomeFailed {
		t.Fatalf("outcome = %v, want failed", s.model.outcome)
	}
	view := s.view()
	if !strings.Contains(view, "does not have a codec") {
		t.Errorf("the failure has no plain-language hint:\n%s", view)
	}
	if !strings.Contains(view, "Unknown encoder") {
		t.Errorf("the failure does not quote ffmpeg:\n%s", view)
	}
}

func TestWizardRefusedOverwriteIsReportedAsSuch(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	// -n prints this and exits 0, which is the trap in DECISIONS D9.
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{
		ExitCode: 0,
		Stderr:   []string{"File 'out.mp4' already exists. Exiting."},
	}})

	if s.model.outcome != ffmpeg.OutcomeRefused {
		t.Fatalf("outcome = %v, want refused", s.model.outcome)
	}
	if !strings.Contains(s.view(), "already exists") {
		t.Errorf("the refusal is not explained:\n%s", s.view())
	}
}

func TestWizardDoneCanStartAnotherTask(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{Cancelled: true, ExitCode: -1}})

	before := s.model.input
	s.press("esc")
	if s.model.screen != ScreenTask {
		t.Fatalf("screen = %v, want the task list", s.model.screen)
	}
	if s.model.input != before {
		t.Errorf("the input changed from %q to %q", before, s.model.input)
	}

	// Picking a task starts over: the previous answers belong to the task that
	// was just abandoned (plan.md §4.5).
	s.press("enter")
	if s.model.screen != ScreenStep {
		t.Fatalf("screen = %v, want the first question", s.model.screen)
	}
	if len(s.model.answers) > 0 {
		t.Errorf("the previous answers survived into the new task: %v", s.model.answers)
	}
}

func TestWizardDoneOpensTheFolder(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	if err := os.WriteFile(s.model.outputPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{ExitCode: 0}})

	s.press("o")
	if s.model.notice == "" {
		t.Errorf("opening the folder said nothing; the model's notice is empty")
	}
}

// TestWizardOffersNothingThatCannotWork: after a failure there is no file to show
// and nothing to delete, so neither key is offered and neither key acts.
func TestWizardOffersNothingThatCannotWork(t *testing.T) {
	s, runner := runnable(t)
	s.press("r")
	s.push(runner, ffmpeg.Event{Done: true, Result: ffmpeg.Result{
		Err:      errors.New("exit status 1"),
		ExitCode: 1,
		Stderr:   []string{"Unknown encoder 'libnope'"},
	}})

	view := s.view()
	for _, unwanted := range []string{"open the folder", "delete"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("a failed run still offers %q:\n%s", unwanted, view)
		}
	}
	s.press("o", "d")
	if s.model.notice != "" {
		t.Errorf("a key that leads nowhere still acted: %q", s.model.notice)
	}
}

// TestWizardStoppedBeforeAnythingWasWritten: a cancel that lands before ffmpeg
// has created the file leaves nothing to delete and nothing to open.
func TestWizardStoppedBeforeAnythingWasWritten(t *testing.T) {
	s, _ := runnable(t)
	s.press("r")
	s.press("q", "q")

	if s.model.outcome != ffmpeg.OutcomeCancelled {
		t.Fatalf("outcome = %v, want cancelled", s.model.outcome)
	}
	view := s.view()
	for _, unwanted := range []string{"press d to delete", "open the folder"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("a stop that wrote nothing still offers %q:\n%s", unwanted, view)
		}
	}

	s.press("d", "o")
	if s.model.removed {
		t.Error("the model claims to have deleted a file that does not exist")
	}
	if s.model.notice != "" {
		t.Errorf("a key that leads nowhere still acted: %q", s.model.notice)
	}
}

// TestWizardRunAndDoneFitEightyColumns: the run and done screens are the ones
// most likely to overflow, because ffmpeg's own output and the file's size end
// up in them. plan.md §6 requires the wizard to survive 80x24, so the frames are
// measured rather than eyeballed.
func TestWizardRunAndDoneFitEightyColumns(t *testing.T) {
	long := strings.Repeat("frame= 1 fps=0.0 q=28.0 size=   0kB time=00:00:00.04 ", 4)

	cases := []struct {
		name  string
		setup func(*session, *fakeRunner)
	}{
		{"run", func(s *session, r *fakeRunner) {
			s.press("r")
			s.push(r, ffmpeg.Event{Progress: &ffmpeg.Progress{
				OutTime: time.Second, Speed: 2, TotalSize: 2048, Bitrate: 183600,
			}})
		}},
		{"run with the log shown", func(s *session, r *fakeRunner) {
			s.press("r")
			s.push(r, ffmpeg.Event{Log: long})
			s.press("l")
		}},
		{"stopped", func(s *session, r *fakeRunner) {
			s.press("r")
			if err := os.WriteFile(s.model.outputPath, []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			s.press("q", "q")
		}},
		{"done", func(s *session, r *fakeRunner) {
			s.press("r")
			if err := os.WriteFile(s.model.outputPath, []byte("done"), 0o600); err != nil {
				t.Fatal(err)
			}
			s.push(r, ffmpeg.Event{Done: true, Result: ffmpeg.Result{ExitCode: 0}})
		}},
		{"failed", func(s *session, r *fakeRunner) {
			s.press("r")
			s.push(r, ffmpeg.Event{Done: true, Result: ffmpeg.Result{
				Err:      errors.New("exit status 1"),
				ExitCode: 1,
				Stderr:   []string{"a complaint from ffmpeg that is far too long to fit on one line of eighty columns"},
			}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, runner := runnable(t)
			tc.setup(s, runner)

			next, _ := s.model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			s.model = next.(Model)

			content := s.view()
			lines := strings.Split(content, "\n")
			if len(lines) > 24 {
				t.Errorf("the view is %d lines tall, which does not fit 24 rows:\n%s", len(lines), content)
			}
			for _, line := range lines {
				if w := visibleLen(line); w > 80 {
					t.Errorf("a line is %d columns wide, which overflows 80:\n%s", w, content)
				}
			}
		})
	}
}

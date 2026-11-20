package ffmpeg

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── the -progress stream ────────────────────────────────────────────────────

// TestProgressDecoder feeds real ffmpeg output, block by block.
//
// The values and the exact spelling come from ffmpeg 8.1.1's `-progress pipe:1`
// stream, including the leading space in the bit rate field and the "N/A" that
// the first blocks carry (DECISIONS D4 on out_time_ms).
func TestProgressDecoder(t *testing.T) {
	block := []string{
		"frame=12",
		"fps=25.0",
		"stream_0_0_q=28.0",
		"bitrate= 183.6kbits/s",
		"total_size=2048",
		"out_time_us=500000",
		"out_time_ms=500000",
		"out_time=00:00:00.500000",
		"dup_frames=0",
		"drop_frames=0",
		"speed=1.10x",
		"progress=continue",
	}

	var d ProgressDecoder
	var got Progress
	snapshots := 0
	for _, line := range block {
		snapshot, ok := d.Line(line)
		if !ok {
			continue
		}
		snapshots++
		got = snapshot
	}

	if snapshots != 1 {
		t.Fatalf("the block produced %d snapshots, want exactly 1", snapshots)
	}
	// A microsecond count, not a millisecond one: 500000µs is half a second.
	if got.OutTime != 500*time.Millisecond {
		t.Errorf("OutTime = %v, want 500ms", got.OutTime)
	}
	if got.Bitrate != 183600 {
		t.Errorf("Bitrate = %v, want 183600 bits/s", got.Bitrate)
	}
	if got.TotalSize != 2048 {
		t.Errorf("TotalSize = %d, want 2048", got.TotalSize)
	}
	if got.Frame != 12 {
		t.Errorf("Frame = %d, want 12", got.Frame)
	}
	if math.Abs(got.Speed-1.10) > 1e-9 {
		t.Errorf("Speed = %v, want 1.10", got.Speed)
	}
	if got.Ended {
		t.Error("Ended = true for a continue block")
	}
}

// TestProgressDecoderPartialBlocks: a snapshot must never be reported mid-block,
// or the UI would show the new out_time with the previous block's speed.
func TestProgressDecoderPartialBlocks(t *testing.T) {
	var d ProgressDecoder
	for _, line := range []string{"out_time_us=1000000", "speed=2.0x"} {
		if _, ok := d.Line(line); ok {
			t.Fatalf("%q ended a block; it must not", line)
		}
	}
	snapshot, ok := d.Line("progress=continue")
	if !ok {
		t.Fatal("progress=continue did not end the block")
	}
	if snapshot.OutTime != time.Second || snapshot.Speed != 2 {
		t.Errorf("snapshot = %+v, want 1s at 2x", snapshot)
	}
}

func TestProgressDecoderUnreadableFields(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  Progress
	}{
		{
			name:  "everything is N/A",
			lines: []string{"bitrate=N/A", "speed=N/A", "total_size=N/A", "out_time_us=N/A"},
			want:  Progress{},
		},
		{
			// A later N/A clears the value rather than leaving a stale speed
			// behind, so the ETA stops claiming to know something it does not.
			name:  "N/A clears an earlier value",
			lines: []string{"speed=2.0x", "progress=continue", "speed=N/A", "progress=continue"},
			want:  Progress{Speed: 0},
		},
		{
			name:  "out_time is the fallback when the microsecond fields are absent",
			lines: []string{"out_time=01:02:03.500000", "progress=continue"},
			want:  Progress{OutTime: time.Hour + 2*time.Minute + 3500*time.Millisecond},
		},
		{
			name:  "a bare bitrate is bits per second",
			lines: []string{"bitrate=1000", "progress=end"},
			want:  Progress{Bitrate: 1000, Ended: true},
		},
		{
			name:  "megabits are scaled",
			lines: []string{"bitrate=1.5Mbits/s", "progress=continue"},
			want:  Progress{Bitrate: 1.5e6},
		},
		{
			name:  "noise is ignored",
			lines: []string{"", "garbage", "=5", "progress=continue"},
			want:  Progress{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d ProgressDecoder
			var got Progress
			ended := false
			for _, line := range tc.lines {
				if snapshot, ok := d.Line(line); ok {
					got = snapshot
					ended = snapshot.Ended
				}
			}
			got.Ended = ended
			if got != tc.want {
				t.Errorf("decoded %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ── percent and ETA ─────────────────────────────────────────────────────────

func TestProgressPercent(t *testing.T) {
	const total = 10 * time.Second
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{"unknown length", Progress{OutTime: 5 * time.Second}, 0},
		{"half way", Progress{OutTime: 5 * time.Second}, 50},
		{"finished", Progress{OutTime: 10 * time.Second}, 100},
		{"past the end is clamped", Progress{OutTime: 30 * time.Second}, 100},
		{"negative is clamped", Progress{OutTime: -time.Second}, 0},
		{"nothing yet", Progress{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.Percent(total)
			if tc.name == "unknown length" {
				got = tc.p.Percent(0)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Percent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProgressETA(t *testing.T) {
	const total = 10 * time.Second
	tests := []struct {
		name string
		p    Progress
		want time.Duration
	}{
		{"half done at 2x means 2.5s left", Progress{OutTime: 5 * time.Second, Speed: 2}, 2500 * time.Millisecond},
		{"realtime", Progress{OutTime: 4 * time.Second, Speed: 1}, 6 * time.Second},
		{"no speed means unknown", Progress{OutTime: 4 * time.Second}, 0},
		{"no length means unknown", Progress{OutTime: 4 * time.Second, Speed: 2}, 0},
		{"already past the end", Progress{OutTime: 11 * time.Second, Speed: 2}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total := total
			if tc.name == "no length means unknown" {
				total = 0
			}
			if got := tc.p.ETA(total); got != tc.want {
				t.Errorf("ETA = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── Diagnose ────────────────────────────────────────────────────────────────

func TestDiagnose(t *testing.T) {
	dir := t.TempDir()
	written := filepath.Join(dir, "written.mp4")
	if err := os.WriteFile(written, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.mp4")

	tests := []struct {
		name       string
		res        Result
		output     string
		want       Outcome
		wantHint   string
		noMoreHint bool
	}{
		{
			name:   "a written file is a success",
			res:    Result{ExitCode: 0},
			output: written,
			want:   OutcomeSucceeded,
		},
		{
			// The trap from DECISIONS D9: -n refuses, ffmpeg says so and exits
			// 0, and nothing is written. Exit status alone would call this a
			// success.
			name:     "refusing to overwrite is not a success",
			res:      Result{ExitCode: 0, Stderr: []string{"File 'out.mp4' already exists. Exiting.", "Error opening output file out.mp4."}},
			output:   written,
			want:     OutcomeRefused,
			wantHint: "choose another name",
		},
		{
			name:     "exit 0 with nothing written is a failure",
			res:      Result{ExitCode: 0},
			output:   missing,
			want:     OutcomeFailed,
			wantHint: "was not written",
		},
		{
			name:     "an empty output is not a success",
			res:      Result{ExitCode: 0},
			output:   empty,
			want:     OutcomeFailed,
			wantHint: "was not written",
		},
		{
			name:     "a stop is reported as a stop, not a failure",
			res:      Result{Err: errExit, ExitCode: 255, Cancelled: true},
			output:   missing,
			want:     OutcomeCancelled,
			wantHint: "",
		},
		{
			name:     "a missing encoder gets a codec hint",
			res:      Result{Err: errExit, ExitCode: 1, Stderr: []string{"Unknown encoder 'libnope'"}},
			output:   missing,
			want:     OutcomeFailed,
			wantHint: "does not have a codec",
		},
		{
			name:     "permission errors say so",
			res:      Result{Err: errExit, ExitCode: 1, Stderr: []string{"out.mp4: Permission denied"}},
			output:   missing,
			want:     OutcomeFailed,
			wantHint: "not allowed to write there",
		},
		{
			name:     "a full disk says so",
			res:      Result{Err: errExit, ExitCode: 1, Stderr: []string{"av_interleaved_write_frame(): No space left on device"}},
			output:   missing,
			want:     OutcomeFailed,
			wantHint: "disk is full",
		},
		{
			name:     "an unrecognised failure still quotes ffmpeg",
			res:      Result{Err: errExit, ExitCode: 1, Stderr: []string{"Something odd happened"}},
			output:   missing,
			want:     OutcomeFailed,
			wantHint: "Something odd happened",
		},
		{
			name:       "a failure with no log at all gets no invented hint",
			res:        Result{Err: errExit, ExitCode: 1},
			output:     missing,
			want:       OutcomeFailed,
			noMoreHint: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, hint := Diagnose(tc.res, tc.output)
			if got != tc.want {
				t.Errorf("Diagnose = %v, want %v", got, tc.want)
			}
			if tc.noMoreHint {
				if hint != "" {
					t.Errorf("hint = %q, want none", hint)
				}
				return
			}
			if tc.wantHint != "" && !strings.Contains(hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to mention %q", hint, tc.wantHint)
			}
			if tc.wantHint == "" && hint != "" {
				t.Errorf("hint = %q, want none", hint)
			}
		})
	}
}

func TestLastLines(t *testing.T) {
	res := Result{Stderr: []string{"a", "b", "c", "d"}}
	if got := res.LastLines(2); strings.Join(got, "") != "cd" {
		t.Errorf("LastLines(2) = %v, want the last two", got)
	}
	if got := res.LastLines(10); len(got) != 4 {
		t.Errorf("LastLines(10) = %v, want everything", got)
	}
	if got := res.LastLines(0); len(got) != 4 {
		t.Errorf("LastLines(0) = %v, want everything", got)
	}
}

// ── running a process ───────────────────────────────────────────────────────

var errExit = errStub("exit status 1")

type errStub string

func (e errStub) Error() string { return string(e) }

// fakeFFmpeg writes a script that stands in for ffmpeg. Letting the test drive
// the exact bytes on stdout and stderr is the only way to check the throttling
// and the block boundaries without waiting on a real encode.
func fakeFFmpeg(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testCommand(t *testing.T, output string) *Command {
	t.Helper()
	cmd, err := Build(Request{
		Inputs: []InputSpec{{Path: "in.mp4"}},
		Output: output,
	})
	if err != nil {
		t.Fatalf("building the test command: %v", err)
	}
	return cmd
}

// collected is everything a run reported.
type collected struct {
	progress []Progress
	logs     []string
	result   Result
}

// drain reads the event stream to its end.
func drain(t *testing.T, r *Run) collected {
	t.Helper()
	var got collected
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return got
			}
			switch {
			case ev.Done:
				got.result = ev.Result
			case ev.Progress != nil:
				got.progress = append(got.progress, *ev.Progress)
			default:
				got.logs = append(got.logs, ev.Log)
			}
		case <-timeout:
			t.Fatal("the run never finished")
		}
	}
}

func TestRunReportsProgressLogsAndResult(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out.mp4")
	script := strings.Join([]string{
		`echo "frame=12"`,
		`echo "total_size=2048"`,
		`echo "out_time_us=500000"`,
		`echo "out_time_ms=500000"`,
		`echo "speed=1.10x"`,
		`echo "progress=continue"`,
		// Four more blocks inside the throttle window: they must collapse into
		// nothing, so the UI is not flooded (plan.md §7).
		`echo "out_time_us=600000"`,
		`echo "progress=continue"`,
		`echo "out_time_us=700000"`,
		`echo "progress=continue"`,
		`echo "out_time_us=800000"`,
		`echo "progress=continue"`,
		`echo "out_time_us=1000000"`,
		`echo "progress=end"`,
		`echo "a warning on stderr" >&2`,
		`exit 0`,
	}, "\n") + "\n"

	r, err := Start(context.Background(), fakeFFmpeg(t, script), testCommand(t, output), RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := drain(t, r)

	// The first block and the final one, with the four in between throttled
	// away. The final one is delivered even inside the window because it is the
	// one that says the encode finished.
	if len(got.progress) != 2 {
		t.Fatalf("got %d progress updates, want 2: %+v", len(got.progress), got.progress)
	}
	if got.progress[0].OutTime != 500*time.Millisecond {
		t.Errorf("first update is %v, want 500ms", got.progress[0].OutTime)
	}
	last := got.progress[len(got.progress)-1]
	if !last.Ended || last.OutTime != time.Second {
		t.Errorf("last update = %+v, want the end at 1s", last)
	}
	if len(got.logs) != 1 || got.logs[0] != "a warning on stderr" {
		t.Errorf("logs = %q, want the one stderr line", got.logs)
	}
	if got.result.Cancelled {
		t.Error("the run reports itself cancelled, but nothing stopped it")
	}
	if got.result.Err != nil {
		t.Errorf("Err = %v, want nil", got.result.Err)
	}
	if got.result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.result.ExitCode)
	}
	if len(got.result.Stderr) != 1 {
		t.Errorf("Result.Stderr = %q, want the one line", got.result.Stderr)
	}

	// Nothing was written, so the run is a failure no matter what ffmpeg said
	// (DECISIONS D9).
	if outcome, hint := Diagnose(got.result, output); outcome != OutcomeFailed {
		t.Errorf("Diagnose = %v (%s), want failed", outcome, hint)
	}
}

// TestRunCancel checks the interrupt-then-kill path: the process must stop
// promptly, not after the kill grace period.
func TestRunCancel(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out.mp4")
	// exec replaces the shell with sleep, so the interrupt reaches the process
	// that is actually waiting rather than the shell that spawned it.
	script := "echo \"out_time_us=500000\"\necho \"progress=continue\"\nexec sleep 30\n"

	r, err := Start(context.Background(), fakeFFmpeg(t, script), testCommand(t, output),
		RunOptions{KillAfter: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	seenProgress := false
	var result *Result
	timeout := time.After(20 * time.Second)
	for result == nil {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				t.Fatal("the event stream closed without a Done event")
			}
			if ev.Progress != nil && !seenProgress {
				seenProgress = true
				r.Cancel()
			}
			if ev.Done {
				res := ev.Result
				result = &res
			}
		case <-timeout:
			t.Fatal("the run never finished after being cancelled")
		}
	}

	if !seenProgress {
		t.Error("no progress arrived before the cancel")
	}
	if !result.Cancelled {
		t.Errorf("Cancelled = false, want true for a stopped run")
	}
	if outcome, _ := Diagnose(*result, output); outcome != OutcomeCancelled {
		t.Errorf("Diagnose = %v, want cancelled", outcome)
	}
}

func TestRunCancelTwiceIsSafe(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out.mp4")
	r, err := Start(context.Background(), fakeFFmpeg(t, "exec sleep 30\n"), testCommand(t, output),
		RunOptions{KillAfter: 2 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Cancel()
	r.Cancel()
	got := drain(t, r)
	if !got.result.Cancelled {
		t.Error("Cancelled = false after Cancel")
	}
}

func TestStartRejectsBadInput(t *testing.T) {
	cmd := testCommand(t, filepath.Join(t.TempDir(), "out.mp4"))
	if _, err := Start(context.Background(), "", cmd, RunOptions{}); err == nil {
		t.Error("Start with no ffmpeg path succeeded")
	}
	if _, err := Start(context.Background(), "/bin/echo", nil, RunOptions{}); err != ErrNoCommand {
		t.Errorf("Start with no command = %v, want ErrNoCommand", err)
	}
	if _, err := Start(context.Background(), filepath.Join(t.TempDir(), "nope"), cmd, RunOptions{}); err == nil {
		t.Error("Start with a missing binary succeeded")
	}
}

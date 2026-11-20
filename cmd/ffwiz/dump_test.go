package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
)

// dumpTaskYAML is a small task with the features the dump output has to render:
// a default answer, an input-gated step and a capability-gated choice.
const dumpTaskYAML = `
- id: demo
  label: Demo task
  description: Used by the dump tests.
  requires: { video: true }
  steps:
    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, default: true }
        - { label: AV1 in MKV, value: av1, output_ext: mkv, args: ["-c:v", "libsvtav1"], requires_encoder: libsvtav1 }
    - id: audio
      prompt: Audio
      type: choice
      when: { input_has: audio }
      choices:
        - { label: Keep, value: keep, args: ["-c:a", "copy"], default: true }
        - { label: Drop, value: drop, args: ["-an"] }
`

func demoSet(t *testing.T) *tasks.Set {
	t.Helper()
	fsys := fstest.MapFS{"builtin/demo.yaml": &fstest.MapFile{Data: []byte(dumpTaskYAML)}}
	set, err := tasks.Load(tasks.LoadOptions{BuiltinFS: fsys, UserDir: t.TempDir()})
	if err != nil {
		t.Fatalf("loading the demo task: %v", err)
	}
	return set
}

func TestParseAnswers(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", in: nil, want: map[string]string{}},
		{name: "single", in: []string{"a=b"}, want: map[string]string{"a": "b"}},
		{name: "comma separated", in: []string{"a=b,c=d"}, want: map[string]string{"a": "b", "c": "d"}},
		{name: "repeated flags", in: []string{"a=b", "c=d"}, want: map[string]string{"a": "b", "c": "d"}},
		{name: "spaces are tolerated", in: []string{" a = b , c = d "}, want: map[string]string{"a": "b", "c": "d"}},
		{name: "value may contain equals", in: []string{"filter=a=b"}, want: map[string]string{"filter": "a=b"}},
		{name: "empty value is allowed", in: []string{"a="}, want: map[string]string{"a": ""}},
		{name: "ignores empty entries", in: []string{"a=b,,c=d"}, want: map[string]string{"a": "b", "c": "d"}},
		{name: "missing equals", in: []string{"a"}, wantErr: true},
		{name: "missing key", in: []string{"=b"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAnswers(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseAnswers(%v) succeeded, want an error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAnswers(%v): %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseAnswers(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("parseAnswers(%v)[%q] = %q, want %q", tc.in, k, got[k], v)
				}
			}
		})
	}
}

// noProbeTools has no ffprobe, so the dump does not probe. Combined with
// IgnoreCapabilities it makes runDump testable without ffmpeg installed.
var noProbeTools = ffmpeg.Tools{FFmpeg: "/nonexistent/ffmpeg"}

func dumpTo(t *testing.T, flags dumpFlags) (string, error) {
	t.Helper()
	flags.IgnoreCapabilities = true
	var buf bytes.Buffer
	err := runDump(&buf, noProbeTools, demoSet(t), flags)
	return buf.String(), err
}

func TestRunDumpText(t *testing.T) {
	out, err := dumpTo(t, dumpFlags{TaskID: "demo", Input: "clip.mp4"})
	if err != nil {
		t.Fatalf("runDump: %v", err)
	}

	for _, want := range []string{
		"task     demo",
		"input    clip.mp4",
		"output   clip_out.mp4",
		"ffmpeg -n -i clip.mp4 clip_out.mp4",
		"container",
		"default",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output does not contain %q:\n%s", want, out)
		}
	}

	// With no probe result the audio step, which needs an audio stream, must be
	// reported as skipped rather than silently answered.
	if !strings.Contains(out, "skipped") || !strings.Contains(out, "audio") {
		t.Errorf("dump output does not explain the skipped audio step:\n%s", out)
	}
}

func TestRunDumpAnswers(t *testing.T) {
	out, err := dumpTo(t, dumpFlags{
		TaskID:  "demo",
		Input:   "clip.mp4",
		Answers: map[string]string{"container": "av1", "audio": "drop"},
	})
	if err != nil {
		t.Fatalf("runDump: %v", err)
	}
	if !strings.Contains(out, "ffmpeg -n -i clip.mp4 -c:v libsvtav1 clip_out.mkv") {
		t.Errorf("dump output does not contain the expected command:\n%s", out)
	}
	if !strings.Contains(out, "answered") {
		t.Errorf("dump output does not distinguish given answers from defaults:\n%s", out)
	}
	// There is no probe result here, so the input is assumed to carry no audio
	// and the audio step is switched off. The `audio=drop` answer is therefore
	// ignored rather than turned into -an: answering a step that does not apply
	// must not leak into the command (plan.md §4.5).
	if strings.Contains(out, "-an") {
		t.Errorf("dump output applied an answer from a step that is switched off:\n%s", out)
	}
}

func TestRunDumpJSON(t *testing.T) {
	out, err := dumpTo(t, dumpFlags{TaskID: "demo", Input: "clip.mp4", JSON: true})
	if err != nil {
		t.Fatalf("runDump: %v", err)
	}

	var payload dumpJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("the dump is not valid JSON: %v\n%s", err, out)
	}
	if payload.TaskID != "demo" {
		t.Errorf("task_id = %q", payload.TaskID)
	}
	if got := strings.Join(payload.Active, ","); got != "container" {
		t.Errorf("active steps = %q, want container only (no audio in the input)", got)
	}
	if payload.Answers["container"] != "mp4" {
		t.Errorf("answers = %v, want the container default", payload.Answers)
	}
	// The command array is the one that is executed, so it carries the
	// execution-only flags; the display string does not.
	if !strings.Contains(strings.Join(payload.Command, " "), "-nostdin") {
		t.Errorf("command = %v, want the execution-only flags", payload.Command)
	}
	if strings.Contains(payload.Display, "-nostdin") {
		t.Errorf("display = %q, want the execution-only flags hidden", payload.Display)
	}
}

func TestRunDumpErrors(t *testing.T) {
	t.Run("unknown task", func(t *testing.T) {
		_, err := dumpTo(t, dumpFlags{TaskID: "nope", Input: "clip.mp4"})
		if err == nil {
			t.Fatal("runDump accepted an unknown task")
		}
		if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "demo") {
			t.Errorf("error = %q, want it to name the task and list the alternatives", err)
		}
	})

	t.Run("no input", func(t *testing.T) {
		_, err := dumpTo(t, dumpFlags{TaskID: "demo"})
		if err == nil {
			t.Fatal("runDump accepted a missing input path")
		}
		if !strings.Contains(err.Error(), "input") {
			t.Errorf("error = %q, want it to mention --input", err)
		}
	})
}

func TestRunDumpProbeFailureIsReported(t *testing.T) {
	// A real ffprobe path that does not exist must produce an error rather than
	// an empty summary.
	tools := ffmpeg.Tools{FFprobe: "/nonexistent/ffprobe"}
	var buf bytes.Buffer
	err := runDump(&buf, tools, demoSet(t), dumpFlags{
		TaskID:             "demo",
		Input:              "clip.mp4",
		IgnoreCapabilities: true,
	})
	if err == nil {
		t.Fatal("runDump ignored a failing ffprobe")
	}
	if !strings.Contains(err.Error(), "probing") {
		t.Errorf("error = %q, want it to say probing failed", err)
	}
}

func TestRunList(t *testing.T) {
	var buf bytes.Buffer
	runList(&buf, demoSet(t), nil)
	got := buf.String()
	if !strings.Contains(got, "demo") || !strings.Contains(got, "Demo task") {
		t.Errorf("runList output = %q", got)
	}
}

func TestRunListMarksTasksThatDoNotApply(t *testing.T) {
	// convert_video needs video; an audio-only input must be flagged rather
	// than offered (plan.md §11).
	audioOnly := &ffmpeg.MediaInfo{
		Path:   "song.mp3",
		Format: ffmpeg.FormatInfo{Duration: 0},
		Streams: []ffmpeg.StreamInfo{
			{Index: 0, Type: ffmpeg.StreamAudio, CodecName: "mp3"},
		},
	}
	var buf bytes.Buffer
	runList(&buf, demoSet(t), audioOnly)
	if !strings.Contains(buf.String(), "video stream") {
		t.Errorf("runList did not explain why the task does not apply: %q", buf.String())
	}
}

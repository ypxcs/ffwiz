package tasks

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// computeTask is the "compress to a target size" shape: a size in MiB and an
// audio bitrate, from which the engine works out the video bitrate.
const computeTask = `
- id: compress
  label: Compress video
  description: Used by the compute tests.
  requires: { video: true }
  steps:
    - id: audio
      prompt: Audio
      type: choice
      default: "192k"
      choices:
        - { label: "AAC 192k", value: "192k", args: ["-c:a", "aac", "-b:a", "192k"] }
        - { label: "Keep as is", value: copy, args: ["-c:a", "copy"] }

    - id: size
      prompt: Target size
      hint: Smaller files lose detail.
      type: choice
      compute:
        kind: video_bitrate
        audio_step: audio
        audio_kbps: 128
        template: ["-b:v", "{bitrate}", "-maxrate", "{maxrate}", "-bufsize", "{bufsize}"]
      choices:
        - { label: "10 MiB", value: "10", default: true }
        - { label: "25 MiB", value: "25" }
`

func loadComputeTask(t *testing.T) Task {
	t.Helper()
	var list []Task
	if err := yaml.Unmarshal([]byte(computeTask), &list); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	task := list[0]
	task.Source = "compute.yaml"
	if err := (&Set{Tasks: []Task{task}}).Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return task
}

func TestComputeVideoBitrateReachesTheTarget(t *testing.T) {
	info := &ffmpeg.MediaInfo{Format: ffmpeg.FormatInfo{Duration: 60 * time.Second}}
	eng := &Engine{Task: loadComputeTask(t), Info: info}

	cmd, res, err := eng.Build(nil, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	// 10 MiB in 60s with 192 kbit/s of audio: floor(1365.33*0.97)-192 = 1132.
	display := cmd.DisplayArgs()
	if !contains(display, "-b:v") || !contains(display, "1132k") {
		t.Errorf("args = %v, want -b:v 1132k", display)
	}
	if !contains(display, "-maxrate") || !contains(display, "1132k") {
		t.Errorf("args = %v, want a matching -maxrate", display)
	}
	if !contains(display, "-bufsize") || !contains(display, "2264k") {
		t.Errorf("args = %v, want -bufsize 2264k", display)
	}
	// The note is what the wizard shows next to the question.
	note := res.Notes["size"]
	if !strings.Contains(note, "MiB") || !strings.Contains(note, "1:00") {
		t.Errorf("note = %q, want the arithmetic spelled out", note)
	}
}

func TestComputeAudioStepIsReadFromItsAnswer(t *testing.T) {
	info := &ffmpeg.MediaInfo{Format: ffmpeg.FormatInfo{Duration: 60 * time.Second}}
	eng := &Engine{Task: loadComputeTask(t), Info: info}

	// A stream copy carries no bitrate, so the declared 128 kbit/s stands in.
	_, _, err := eng.Build(map[string]string{"audio": "copy"}, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	copied := eng.Resolve(map[string]string{"audio": "copy"})
	withAudio := eng.Resolve(map[string]string{"audio": "aac192"})
	if copied.Notes["size"] == withAudio.Notes["size"] {
		t.Errorf("the note should differ once the audio bitrate is known: %q", copied.Notes["size"])
	}
}

func TestComputeRefusesAnUnreachableTarget(t *testing.T) {
	// An hour of video in 10 MiB is a few kbit/s: the arithmetic cannot give a
	// usable video bitrate, and the build must say so rather than emit -b:v 1k.
	info := &ffmpeg.MediaInfo{Format: ffmpeg.FormatInfo{Duration: time.Hour}}
	eng := &Engine{Task: loadComputeTask(t), Info: info}
	_, _, err := eng.Build(nil, BuildOptions{Input: "in.mp4", Output: "out.mp4"})
	if err == nil {
		t.Fatal("an unreachable target should be an error")
	}
	if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("err = %v, want it to suggest a size", err)
	}
}

func TestComputeTemplatePlaceholderErrors(t *testing.T) {
	bad := `
- id: x
  label: X
  steps:
    - id: size
      prompt: Size
      type: choice
      compute:
        kind: video_bitrate
        template: ["-b:v", "{bitrate}", "-b:a", "{nope}"]
      choices: [{ label: L, value: "10" }]
`
	var list []Task
	if err := yaml.Unmarshal([]byte(bad), &list); err != nil {
		t.Fatal(err)
	}
	list[0].Source = "bad.yaml"
	err := (&Set{Tasks: list}).Validate()
	if err == nil || !strings.Contains(err.Error(), "{nope}") {
		t.Fatalf("err = %v, want an unknown-placeholder error", err)
	}
}

func TestComputeRejectsAnUnknownKind(t *testing.T) {
	bad := `
- id: x
  label: X
  steps:
    - id: size
      prompt: Size
      type: choice
      compute:
        kind: magic
        template: ["-b:v", "{bitrate}"]
      choices: [{ label: L, value: "10" }]
`
	var list []Task
	if err := yaml.Unmarshal([]byte(bad), &list); err != nil {
		t.Fatal(err)
	}
	list[0].Source = "bad.yaml"
	err := (&Set{Tasks: list}).Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("err = %v, want an unknown-kind error", err)
	}
}

func TestComputeRejectsATemplateAlongsideIt(t *testing.T) {
	bad := `
- id: x
  label: X
  steps:
    - id: size
      prompt: Size
      type: choice
      template: ["-b:v", "{value}"]
      compute:
        kind: video_bitrate
        template: ["-b:v", "{bitrate}"]
      choices: [{ label: L, value: "10" }]
`
	var list []Task
	if err := yaml.Unmarshal([]byte(bad), &list); err != nil {
		t.Fatal(err)
	}
	list[0].Source = "bad.yaml"
	err := (&Set{Tasks: list}).Validate()
	if err == nil || !strings.Contains(err.Error(), "cannot both be set") {
		t.Fatalf("err = %v, want a both-set error", err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

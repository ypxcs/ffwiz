package tasks

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// fixtureTask exercises every schema feature the engine has to worry about:
// capability gating, choice and step conditions, skips, both filter targets, a
// filtergraph with maps, and both kinds of template.
const fixtureTask = `
- id: fixture
  label: Fixture task
  description: Used by the engine tests.
  requires: { video: true }
  steps:
    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, requires_muxer: mp4, default: true }
        - { label: GIF, value: gif, output_ext: gif, requires_muxer: gif, requires_filter: palettegen }

    - id: codec
      prompt: Video codec
      type: choice
      choices:
        - { label: H.264, value: h264, args: ["-c:v", "libx264"], requires_encoder: libx264, default: true }
        - { label: H.265, value: h265, args: ["-c:v", "libx265"], requires_encoder: libx265 }
        - { label: Copy, value: copy, args: ["-c:v", "copy"], skips: [quality] }
        - label: GIF graph
          value: gifgraph
          filter_complex: ["[0:v] fps=10 [out]"]
          maps: ["[out]"]
          # A filtergraph owns the video stream, so the steps that would add a
          # simple -vf filter must be switched off; Validate enforces this.
          skips: [quality, scale, speed]
          when: { answer_is: { container: gif } }

    - id: quality
      prompt: Quality
      type: choice
      when: { answer_not: { codec: copy } }
      choices:
        - { label: Balanced, value: balanced, args: ["-crf", "23"], default: true }
        - { label: Small, value: small, args: ["-crf", "28"] }

    - id: scale
      prompt: Resolution
      type: choice
      choices:
        - { label: 720p, value: "720", filters: ["scale=-2:720"], default: true }
        - { label: 480p, value: "480", filters: ["scale=-2:480"] }

    - id: loudness
      prompt: Audio handling
      type: choice
      when: { input_has: audio }
      choices:
        - { label: Normalise, value: on, filters: ["loudnorm"], target: audio, default: true }
        - { label: Leave alone, value: off }

    - id: start
      prompt: Start time
      type: input
      input_template: ["-ss", "{value}"]
      template: ["-t", "5"]

    - id: speed
      prompt: Speed
      type: choice
      time_scale: true
      choices:
        - { label: Normal, value: "1", default: true }
        - { label: Double, value: "2", filters: ["setpts=0.5*PTS"] }
        - { label: Half, value: "0.5", filters: ["setpts=2*PTS"] }
`

// loadFixture parses the fixture and fails the test if it is invalid, so the
// engine tests cannot silently drift from the schema.
func loadFixture(t *testing.T) Task {
	t.Helper()
	var tasks []Task
	if err := yaml.Unmarshal([]byte(fixtureTask), &tasks); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("fixture has %d tasks, want 1", len(tasks))
	}
	task := tasks[0]
	task.Source = "fixture.yaml"
	set := &Set{Tasks: []Task{task}}
	if err := set.Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return task
}

func engineFor(task Task, info *ffmpeg.MediaInfo, caps *ffmpeg.Capabilities) *Engine {
	return &Engine{Task: task, Info: info, Caps: caps}
}

// fullCaps is a capability set that has everything the fixture asks for.
func fullCaps() *ffmpeg.Capabilities {
	return &ffmpeg.Capabilities{
		Encoders:     map[string]string{"libx264": "x264", "libx265": "x265"},
		Muxers:       map[string]string{"mp4": "MP4", "gif": "GIF"},
		PixelFormats: map[string]struct{}{"yuv420p": {}},
		Filters:      map[string]struct{}{"scale": {}, "palettegen": {}},
	}
}

func TestResolveAppliesDefaults(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	res := eng.Resolve(nil)

	if got := res.Answers["container"]; got != "mp4" {
		t.Errorf("container = %q, want the default mp4", got)
	}
	if got := res.Answers["quality"]; got != "balanced" {
		t.Errorf("quality = %q, want the default balanced", got)
	}
	if got := res.Choices["codec"].Value; got != "h264" {
		t.Errorf("codec = %q, want the default h264", got)
	}
	// The fixture has no input, so the audio-gated step is out.
	if _, active := res.Answers["loudness"]; active {
		t.Error("loudness was answered without an audio stream in the input")
	}
	if reason := res.Skipped["loudness"]; !strings.Contains(reason, "audio stream") {
		t.Errorf("loudness skip reason = %q, want it to mention the audio stream", reason)
	}
}

func TestResolveHonoursAnswersAndOrdering(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	res := eng.Resolve(map[string]string{"codec": "h265", "scale": "480"})

	if got := res.Choices["codec"].Value; got != "h265" {
		t.Errorf("codec = %q, want h265", got)
	}
	if got := res.Answers["scale"]; got != "480" {
		t.Errorf("scale = %q, want 480", got)
	}
	// Active is in step order, which is what determines argument order.
	var ids []string
	for _, s := range res.Active {
		ids = append(ids, s.ID)
	}
	want := []string{"container", "codec", "quality", "scale", "start", "speed"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("active steps = %v, want %v", ids, want)
	}
}

func TestResolveFallsBackWhenAnAnswerIsGone(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	// h265 is hidden when the local ffmpeg has no libx265 encoder, so an answer
	// naming it must not be used.
	caps := fullCaps()
	delete(caps.Encoders, "libx265")
	eng.Caps = caps

	res := eng.Resolve(map[string]string{"codec": "h265"})

	if got := res.Answers["codec"]; got != "h264" {
		t.Errorf("codec = %q, want the fallback h264", got)
	}
	var hidden []string
	for _, c := range res.HiddenChoices["codec"] {
		hidden = append(hidden, c.Value)
	}
	if !slices.Contains(hidden, "h265") {
		t.Errorf("hidden codec choices = %v, want them to include h265", hidden)
	}
	if reason := eng.Unavailable(Choice{Value: "h265", RequiresEncoder: "libx265"}); !strings.Contains(reason, "libx265") {
		t.Errorf("Unavailable() = %q, want it to name libx265", reason)
	}
}

func TestResolveSkipsStepWhenItsChoicesAreAllUnavailable(t *testing.T) {
	task := loadFixture(t)
	caps := fullCaps()
	// Neither container is available: no mp4 muxer, and no gif muxer/filter.
	delete(caps.Muxers, "gif")
	delete(caps.Muxers, "mp4")
	delete(caps.Filters, "palettegen")

	eng := engineFor(task, nil, caps)
	res := eng.Resolve(nil)

	if _, active := res.Answers["container"]; active {
		t.Error("container was answered even though no container is available")
	}
	if reason := res.Skipped["container"]; !strings.Contains(reason, "no option") {
		t.Errorf("container skip reason = %q, want it to say no option is available", reason)
	}
}

func TestResolveChoiceConditionHidesChoice(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	// gifgraph only applies when the container is gif.
	res := eng.Resolve(map[string]string{"container": "mp4", "codec": "gifgraph"})

	if got := res.Answers["codec"]; got != "h264" {
		t.Errorf("codec = %q, want h264: gifgraph does not apply to an MP4 output", got)
	}
}

func TestResolveSkipsClearAnswers(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	// A user who picks Copy and then goes back and picks H.264 must not end up
	// with a stale quality answer if quality was switched off in between. The
	// engine recomputes from scratch, so this is really a test that resolve is
	// not stateful.
	res := eng.Resolve(map[string]string{"codec": "copy", "quality": "small"})
	if _, answered := res.Answers["quality"]; answered {
		t.Error("quality was answered even though the codec is copy")
	}
	if reason := res.Skipped["quality"]; !strings.Contains(reason, "copy") {
		t.Errorf("quality skip reason = %q, want it to mention the copy answer", reason)
	}

	res = eng.Resolve(map[string]string{"codec": "h264"})
	if got := res.Answers["quality"]; got != "balanced" {
		t.Errorf("quality = %q, want it back at its default once the codec is h264", got)
	}
}

func TestBuildArgumentOrder(t *testing.T) {
	info := media(10*time.Second, ffmpeg.StreamVideo, ffmpeg.StreamAudio)
	eng := engineFor(loadFixture(t), info, fullCaps())

	cmd, res, err := eng.Build(map[string]string{"scale": "480", "start": "3"}, BuildOptions{Input: "in.mp4"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got := strings.Join(cmd.Args(), " ")
	display := cmd.String()
	wantDisplay := "ffmpeg -n -ss 3 -i in.mp4 -vf scale=-2:480 -af loudnorm -c:v libx264 -crf 23 -t 5 in_out.mp4"
	if display != wantDisplay {
		t.Errorf("String() =\n  %s\nwant\n  %s", display, wantDisplay)
	}
	// The executed form adds the execution-only flags, which must not appear in
	// the display form (plan.md §7).
	for _, hidden := range []string{"-hide_banner", "-nostdin", "-progress", "-nostats"} {
		if !strings.Contains(got, hidden) {
			t.Errorf("Args() is missing %s: %s", hidden, got)
		}
		if strings.Contains(display, hidden) {
			t.Errorf("String() should not show %s: %s", hidden, display)
		}
	}

	// Verify the raw argument list separately, position by position, so a
	// failure says which rule broke.
	args := cmd.Args()
	assertOrder(t, args,
		"-n", // overwrite policy first
		"-ss", "3",
		"-i", "in.mp4",
		"-vf", "scale=-2:480",
		"-af", "loudnorm",
		"-c:v", "libx264",
		"-crf", "23",
		"-t", "5",
		"in_out.mp4",
	)

	if res.OutputExt() != "mp4" {
		t.Errorf("OutputExt() = %q, want mp4", res.OutputExt())
	}
	if cmd.ExpectedDuration() != 10*time.Second {
		t.Errorf("ExpectedDuration() = %v, want the input duration", cmd.ExpectedDuration())
	}
}

// assertOrder checks that seq contains sub, in order, allowing extra arguments
// between them.
func assertOrder(t *testing.T, seq []string, sub ...string) {
	t.Helper()
	i := 0
	for _, s := range sub {
		found := false
		for ; i < len(seq); i++ {
			if seq[i] == s {
				found = true
				i++
				break
			}
		}
		if !found {
			t.Fatalf("argument %q not found after position %d in %v", s, i, seq)
		}
	}
}

func TestBuildFilterComplexRequiresMaps(t *testing.T) {
	task := loadFixture(t)
	// Remove the maps from the gifgraph choice to prove Build rejects it.
	for i := range task.Steps {
		if task.Steps[i].ID != "codec" {
			continue
		}
		for j := range task.Steps[i].Choices {
			if task.Steps[i].Choices[j].Value == "gifgraph" {
				task.Steps[i].Choices[j].Maps = nil
			}
		}
	}

	eng := engineFor(task, nil, fullCaps())
	_, _, err := eng.Build(map[string]string{"container": "gif", "codec": "gifgraph"}, BuildOptions{Input: "in.mp4"})
	if err == nil {
		t.Fatal("Build accepted a filtergraph without maps; ffmpeg would produce no stream")
	}
	if !strings.Contains(err.Error(), "map") {
		t.Errorf("error = %q, want it to explain that maps are missing", err)
	}
}

func TestBuildFilterComplexAndMaps(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	cmd, _, err := eng.Build(map[string]string{"container": "gif", "codec": "gifgraph"},
		BuildOptions{Input: "in.mp4", Output: "out.gif"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := "ffmpeg -n -i in.mp4 -filter_complex '[0:v] fps=10 [out]' -map '[out]' out.gif"
	if got := cmd.String(); got != want {
		t.Errorf("String() =\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildFailsWithoutInputOrOutput(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	if _, _, err := eng.Build(nil, BuildOptions{}); err == nil {
		t.Error("Build accepted an empty input path")
	}

	// A task whose answers select no container cannot name an output file.
	noExt := Task{ID: "noext", Label: "No ext", Steps: []Step{{
		ID: "x", Prompt: "x", Type: StepChoice,
		Choices: []Choice{{Label: "A", Value: "a", Default: true}},
	}}}
	eng = engineFor(noExt, nil, nil)
	_, _, err := eng.Build(nil, BuildOptions{Input: "in.mp4"})
	if err == nil {
		t.Fatal("Build invented an output path with no extension")
	}
	if !strings.Contains(err.Error(), "extension") {
		t.Errorf("error = %q, want it to mention the missing extension", err)
	}
}

func TestBuildRejectsOutputEqualToInput(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	_, _, err := eng.Build(nil, BuildOptions{Input: "in.mp4", Output: "in.mp4"})
	if err == nil {
		t.Fatal("Build allowed the output to clobber the input")
	}
	if !strings.Contains(err.Error(), "same") {
		t.Errorf("error = %q, want it to say the paths are the same", err)
	}
}

func TestBuildOverwriteFlag(t *testing.T) {
	eng := engineFor(loadFixture(t), nil, fullCaps())

	cmd, _, err := eng.Build(nil, BuildOptions{Input: "in.mp4", Output: "out.mp4", Overwrite: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !cmd.Overwrites() {
		t.Error("Overwrites() = false, want true")
	}
	if !strings.HasPrefix(cmd.String(), "ffmpeg -y ") {
		t.Errorf("String() = %q, want it to start with -y", cmd.String())
	}
}

func TestDefaultOutput(t *testing.T) {
	tests := []struct {
		input string
		ext   string
		want  string
	}{
		{"clip.mp4", "mp4", "clip_out.mp4"},
		{"media/clip.mkv", "webm", "media/clip_out.webm"},
		{"/abs/path/clip.avi", "mp4", "/abs/path/clip_out.mp4"},
		{"no-extension", "gif", "no-extension_out.gif"},
		{"archive.tar.gz", "mp3", "archive.tar_out.mp3"},
		// Awkward names must survive unchanged (plan.md §11).
		{"sp ace.mp4", "mp4", "sp ace_out.mp4"},
		{"quote's.mp4", "mp4", "quote's_out.mp4"},
		{"ünïcodé.mp4", "mp4", "ünïcodé_out.mp4"},
	}
	for _, tc := range tests {
		res := Resolution{Active: []Step{{ID: "container"}}, Choices: map[string]Choice{"container": {OutputExt: tc.ext}}}
		got, err := DefaultOutput(tc.input, res)
		if err != nil {
			t.Errorf("DefaultOutput(%q, %s): %v", tc.input, tc.ext, err)
			continue
		}
		if got != tc.want {
			t.Errorf("DefaultOutput(%q, %s) = %q, want %q", tc.input, tc.ext, got, tc.want)
		}
	}
}

func TestExpectedDurationScalesWithSpeed(t *testing.T) {
	info := media(10*time.Second, ffmpeg.StreamVideo, ffmpeg.StreamAudio)

	tests := []struct {
		speed string
		want  time.Duration
	}{
		{"", 10 * time.Second},
		{"1", 10 * time.Second},
		{"2", 5 * time.Second},
		{"0.5", 20 * time.Second},
	}
	for _, tc := range tests {
		eng := engineFor(loadFixture(t), info, fullCaps())
		cmd, _, err := eng.Build(map[string]string{"speed": tc.speed}, BuildOptions{Input: "in.mp4"})
		if err != nil {
			t.Fatalf("Build with speed=%q: %v", tc.speed, err)
		}
		if got := cmd.ExpectedDuration(); got != tc.want {
			t.Errorf("speed=%q: ExpectedDuration() = %v, want %v", tc.speed, got, tc.want)
		}
	}

	// With no probe result there is no duration to scale, and 0 means unknown.
	eng := engineFor(loadFixture(t), nil, fullCaps())
	cmd, _, err := eng.Build(nil, BuildOptions{Input: "in.mp4"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cmd.ExpectedDuration(); got != 0 {
		t.Errorf("ExpectedDuration() = %v with no probe data, want 0", got)
	}
}

func TestBuildWithoutCapabilitiesAssumesEverything(t *testing.T) {
	// Caps is nil in the golden tests and for `--ignore-capabilities`, so the
	// full catalogue must be reachable.
	eng := engineFor(loadFixture(t), nil, nil)
	res := eng.Resolve(map[string]string{"codec": "h265"})
	if got := res.Answers["codec"]; got != "h265" {
		t.Errorf("codec = %q, want h265 when capability checks are off", got)
	}
}

func TestTaskApplies(t *testing.T) {
	task := loadFixture(t)

	if ok, reason := task.Applies(nil); !ok {
		t.Errorf("Applies(nil) = false (%s), want true: nothing is known yet", reason)
	}
	if ok, _ := task.Applies(media(time.Second, ffmpeg.StreamVideo)); !ok {
		t.Error("Applies() = false for a video input, want true")
	}
	ok, reason := task.Applies(media(time.Second, ffmpeg.StreamAudio))
	if ok {
		t.Error("Applies() = true for an audio-only input, want false")
	}
	if !strings.Contains(reason, "video") {
		t.Errorf("reason = %q, want it to mention video", reason)
	}
}

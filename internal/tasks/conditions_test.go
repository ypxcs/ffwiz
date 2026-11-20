package tasks

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// media builds a MediaInfo with the given stream types, so the input_has cases
// read clearly.
func media(duration time.Duration, types ...ffmpeg.StreamType) *ffmpeg.MediaInfo {
	info := &ffmpeg.MediaInfo{Path: "in.mp4", Format: ffmpeg.FormatInfo{Duration: duration}}
	for i, t := range types {
		info.Streams = append(info.Streams, ffmpeg.StreamInfo{Index: i, Type: t, CodecName: string(t)})
	}
	return info
}

// parseCondition decodes a condition from YAML text, which is how the real ones
// arrive.
func parseCondition(t *testing.T, src string) *Condition {
	t.Helper()
	var c Condition
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("parsing condition %q: %v", src, err)
	}
	return &c
}

func TestConditionEval(t *testing.T) {
	av := media(10*time.Second, ffmpeg.StreamVideo, ffmpeg.StreamAudio)
	mp3 := media(10*time.Second, ffmpeg.StreamAudio)

	tests := []struct {
		name    string
		yaml    string
		info    *ffmpeg.MediaInfo
		answers map[string]string
		want    bool
	}{
		{name: "nil condition always holds", yaml: "{}\n", info: av, want: true},

		{name: "input_has video on a video file", yaml: "{ input_has: video }", info: av, want: true},
		{name: "input_has video on audio only", yaml: "{ input_has: video }", info: mp3, want: false},
		{name: "input_has audio on audio only", yaml: "{ input_has: audio }", info: mp3, want: true},
		{name: "input_has subtitles on a file without any", yaml: "{ input_has: subtitles }", info: av, want: false},
		{name: "input_has subtitle singular is accepted", yaml: "{ input_has: subtitle }", info: av, want: false},
		{name: "input_has unknown type is false", yaml: "{ input_has: chapters }", info: av, want: false},
		// No file chosen yet: every input_has is false rather than an error.
		{name: "input_has with no input", yaml: "{ input_has: video }", info: nil, want: false},

		{name: "min_duration met", yaml: "{ min_duration: 5s }", info: av, want: true},
		{name: "min_duration unmet", yaml: "{ min_duration: 30s }", info: av, want: false},
		{name: "min_duration bare seconds", yaml: "{ min_duration: 30 }", info: av, want: false},
		{name: "min_duration with no input", yaml: "{ min_duration: 1s }", info: nil, want: false},

		{
			name: "answer_is matches", yaml: "{ answer_is: { vcodec: copy } }",
			info: av, answers: map[string]string{"vcodec": "copy"}, want: true,
		},
		{
			name: "answer_is does not match", yaml: "{ answer_is: { vcodec: copy } }",
			info: av, answers: map[string]string{"vcodec": "libx264"}, want: false,
		},
		{
			name: "answer_is on an unanswered step", yaml: "{ answer_is: { vcodec: copy } }",
			info: av, answers: map[string]string{}, want: false,
		},
		{
			name: "answer_not matches", yaml: "{ answer_not: { vcodec: copy } }",
			info: av, answers: map[string]string{"vcodec": "libx264"}, want: true,
		},
		{
			name: "answer_not does not match", yaml: "{ answer_not: { vcodec: copy } }",
			info: av, answers: map[string]string{"vcodec": "copy"}, want: false,
		},

		{
			// Several fields in one condition are an AND.
			name:    "fields combine with AND, both hold",
			yaml:    "{ input_has: audio, answer_is: { vcodec: copy } }",
			info:    av,
			answers: map[string]string{"vcodec": "copy"},
			want:    true,
		},
		{
			name:    "fields combine with AND, one fails",
			yaml:    "{ input_has: video, answer_is: { vcodec: copy } }",
			info:    mp3,
			answers: map[string]string{"vcodec": "copy"},
			want:    false,
		},

		{
			name:    "any holds when one branch holds",
			yaml:    "{ any: [{ answer_is: { container: mp4 } }, { input_has: subtitles }] }",
			info:    av,
			answers: map[string]string{"container": "mp4"},
			want:    true,
		},
		{
			name:    "any fails when no branch holds",
			yaml:    "{ any: [{ answer_is: { container: webm } }, { input_has: subtitles }] }",
			info:    av,
			answers: map[string]string{"container": "mp4"},
			want:    false,
		},
		{
			name:    "all holds when every branch holds",
			yaml:    "{ all: [{ input_has: audio }, { answer_is: { container: mp4 } }] }",
			info:    av,
			answers: map[string]string{"container": "mp4"},
			want:    true,
		},
		{
			name:    "all fails when one branch fails",
			yaml:    "{ all: [{ input_has: audio }, { answer_is: { container: webm } }] }",
			info:    av,
			answers: map[string]string{"container": "mp4"},
			want:    false,
		},
		{
			name: "not inverts",
			yaml: "{ not: { answer_is: { container: webm } } }",
			info: av, answers: map[string]string{"container": "mp4"}, want: true,
		},
		{
			name: "not inverts the other way",
			yaml: "{ not: { answer_is: { container: webm } } }",
			info: av, answers: map[string]string{"container": "webm"}, want: false,
		},
		{
			name: "nested combinators",
			yaml: "{ all: [ { input_has: audio }, { not: { answer_is: { audio: none } } } ] }",
			info: av, answers: map[string]string{"audio": "copy"}, want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := parseCondition(t, tc.yaml)
			got := c.Eval(EvalContext{Info: tc.info, Answers: tc.answers})
			if got != tc.want {
				t.Errorf("Eval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConditionExplain(t *testing.T) {
	tests := []struct {
		yaml     string
		contains []string
	}{
		{"{ input_has: audio }", []string{"audio stream"}},
		{"{ min_duration: 10s }", []string{"10s"}},
		{"{ answer_is: { vcodec: copy } }", []string{"vcodec", "copy"}},
		{"{ answer_not: { container: webm } }", []string{"container", "webm"}},
		// Map iteration order must not leak into the output.
		{"{ answer_not: { b: y, a: x } }", []string{"a", "b"}},
	}
	for _, tc := range tests {
		got := parseCondition(t, tc.yaml).Explain()
		for _, want := range tc.contains {
			if !strings.Contains(got, want) {
				t.Errorf("Explain(%s) = %q, want it to contain %q", tc.yaml, got, want)
			}
		}
	}

	if got := (*Condition)(nil).Explain(); got != "" {
		t.Errorf("Explain() on a nil condition = %q, want an empty string", got)
	}
}

func TestConditionValidateRejectsUnknownStep(t *testing.T) {
	stepIDs := map[string]int{"container": 0, "vcodec": 1}

	t.Run("unknown step", func(t *testing.T) {
		c := parseCondition(t, "{ answer_is: { contaner: mp4 } }")
		errs := c.validate(stepIDs, 2)
		if len(errs) == 0 {
			t.Fatal("validate accepted a condition referring to an unknown step")
		}
		if !strings.Contains(errs[0].Error(), "contaner") {
			t.Errorf("error %q does not name the bad step", errs[0])
		}
	})

	t.Run("forward reference", func(t *testing.T) {
		// vcodec is index 1; a step at index 1 may not read its own answer.
		c := parseCondition(t, "{ answer_is: { vcodec: copy } }")
		if errs := c.validate(stepIDs, 1); len(errs) == 0 {
			t.Fatal("validate accepted a condition reading a step that is not earlier")
		}
	})

	t.Run("earlier step is fine", func(t *testing.T) {
		c := parseCondition(t, "{ answer_is: { container: mp4 } }")
		if errs := c.validate(stepIDs, 2); len(errs) != 0 {
			t.Fatalf("validate rejected a valid condition: %v", errs)
		}
	})

	t.Run("unknown input_has", func(t *testing.T) {
		c := parseCondition(t, "{ input_has: vidio }")
		if errs := c.validate(stepIDs, 0); len(errs) == 0 {
			t.Fatal("validate accepted an unknown input_has value")
		}
	})
}

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		yaml string
		want time.Duration
	}{
		{"10s", 10 * time.Second},
		{"1m30s", 90 * time.Second},
		{"250ms", 250 * time.Millisecond},
		{"10", 10 * time.Second},
		{"10.5", 10500 * time.Millisecond},
		{`"10"`, 10 * time.Second},
	}
	for _, tc := range tests {
		var d Duration
		if err := yaml.Unmarshal([]byte(tc.yaml), &d); err != nil {
			t.Errorf("unmarshalling %s: %v", tc.yaml, err)
			continue
		}
		if d.Std() != tc.want {
			t.Errorf("unmarshalling %s = %v, want %v", tc.yaml, d.Std(), tc.want)
		}
	}

	for _, bad := range []string{"soon", "10 minutes", "[]", "{a: b}"} {
		var d Duration
		if err := yaml.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("unmarshalling %q succeeded, want an error", bad)
		}
	}
}

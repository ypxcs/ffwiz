// Package tasks implements the data-driven wizard engine: tasks, steps and
// choices are defined in YAML and turned into an ffmpeg.Request.
//
// Like internal/ffmpeg, nothing here imports a TUI library (plan.md §3): the
// whole engine is testable without a terminal.
package tasks

import (
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// StepType selects the widget the wizard renders and the shape of the answer.
type StepType string

const (
	// StepChoice is a single selection from a list.
	StepChoice StepType = "choice"
	// StepMulti is a multi-selection from a list (plan.md §4.2).
	StepMulti StepType = "multi"
	// StepInput is free text or a number with optional validation.
	StepInput StepType = "input"
	// StepTimeRange is a validated start/end pair.
	StepTimeRange StepType = "time_range"
	// StepResolution is a resolution preset that preserves aspect ratio.
	StepResolution StepType = "resolution"
	// StepConfirm is a yes/no toggle.
	StepConfirm StepType = "confirm"
)

// Task is one user-facing job, e.g. "Convert video to another format".
type Task struct {
	ID          string   `yaml:"id"`
	Label       string   `yaml:"label"`
	Description string   `yaml:"description"`
	Requires    Requires `yaml:"requires"`
	// Disabled hides the task. A user override file can set this to remove a
	// built-in task (DECISIONS D12).
	Disabled bool   `yaml:"disabled"`
	Steps    []Step `yaml:"steps"`

	// Source is the file this task was loaded from, for error messages. It is
	// not part of the YAML.
	Source string `yaml:"-"`
}

// Requires gates a whole task on what the input file actually contains.
type Requires struct {
	Video     bool `yaml:"video"`
	Audio     bool `yaml:"audio"`
	Subtitles bool `yaml:"subtitles"`
}

// Step is one question in a task.
type Step struct {
	ID     string   `yaml:"id"`
	Prompt string   `yaml:"prompt"`
	Hint   string   `yaml:"hint"`
	Type   StepType `yaml:"type"`

	Choices []Choice `yaml:"choices"`

	// Default is the answer used when the step is active and unanswered. For a
	// choice step it is a choice value; for other types it is the literal value.
	Default string `yaml:"default"`

	// When gates the step. A step whose condition is false is skipped silently
	// and any answer it held is cleared (plan.md §4.5).
	When *Condition `yaml:"when"`

	// Validate constrains an input step's value.
	Validate *Validate `yaml:"validate"`

	// Template turns an input step's value into arguments, e.g.
	// [\"-ss\", \"{value}\"]. {value} is substituted. These are *output*
	// arguments, because that is what a rewritten value usually produces.
	Template []string `yaml:"template"`

	// InputTemplate is Template for arguments that must precede -i, which is
	// the case for a seek: "-ss 30 -i in.mp4" seeks instantly, while putting
	// -ss after -i decodes everything from the start and is much slower
	// (DECISIONS D6).
	InputTemplate []string `yaml:"input_template"`

	// Skips names later steps that become irrelevant once this step is
	// answered. Equivalent to listing them in a choice's skips, but applies to
	// every answer of this step.
	Skips []string `yaml:"skips"`

	// Args are output arguments contributed by a confirm step when the answer
	// is yes, e.g. ["-movflags", "+faststart"]. Ignored for other types, which
	// contribute through their templates or their choices.
	Args []string `yaml:"args"`

	// Filters and Target let a confirm step contribute to the merged -vf/-af
	// chain, e.g. a "burn in the timecode" toggle.
	Filters []string     `yaml:"filters"`
	Target  FilterTarget `yaml:"target"`

	// Custom offers a "Custom..." entry alongside a resolution step's presets,
	// so the user can type a height that is not in the list. The typed value
	// replaces {value} in the step's template and is checked by Validate.
	Custom bool `yaml:"custom"`

	// Placeholder, Unit and MinLabel/MaxLabel are display hints for input and
	// time_range steps.
	Placeholder string `yaml:"placeholder"`
	Unit        string `yaml:"unit"`
	MinLabel    string `yaml:"min_label"`
	MaxLabel    string `yaml:"max_label"`

	// TimeScale marks a step whose answer is a playback-speed multiplier, e.g.
	// "2" for twice as fast. The engine multiplies the active ones and divides
	// the expected output duration by the result, so the progress bar stays
	// honest (DECISIONS D3). It is a flag rather than a step id convention so a
	// typo in an id cannot silently break the progress bar.
	TimeScale bool `yaml:"time_scale"`
}

// Choice is one option within a choice or multi step.
//
// Argument placement is the single most dangerous thing in this file, so it is
// spelled out per field below. plan.md §4.4 describes `args` as "before input,
// global", which contradicts every example in §4.1 and is wrong: ffmpeg treats
// options before -i as belonging to the *output* group, so
//
//	ffmpeg -c:v libx264 -i in.mp4 out.mp4
//
// fails with "Output file does not contain any stream" and tries to write to
// the input path. Verified on ffmpeg 8.1.1; see DECISIONS D19.
type Choice struct {
	Label   string   `yaml:"label"`
	Value   string   `yaml:"value"`
	Hint    string   `yaml:"hint"`
	Args    []string `yaml:"args"`
	Default bool     `yaml:"default"`

	// When hides this choice when it does not hold, evaluated against the
	// answers known so far. It exists because capability gating alone cannot
	// express "H.264 is fine, unless the container is WebM": ffmpeg refuses to
	// mux H.264 into WebM and the command would fail at run time. Conditions on
	// choices may only read earlier steps, like step conditions (plan.md §4.5).
	When *Condition `yaml:"when"`

	// InputArgs are emitted before -i. Only for genuine input options such as a
	// fast seek (-ss) or -t on the input side.
	InputArgs []string `yaml:"input_args"`

	// OutputArgs are emitted after filters and maps, i.e. after everything this
	// choice's Args and Filters contributed.
	OutputArgs []string `yaml:"output_args"`

	// Filters contributes to the merged -vf or -af chain, depending on the
	// step's Target.
	Filters []string     `yaml:"filters"`
	Target  FilterTarget `yaml:"target"`

	// FilterComplex is a complete filtergraph. When set, Maps must be set too
	// or ffmpeg fails with "Output file does not contain any stream"
	// (DECISIONS D2). Validated at load time.
	FilterComplex []string `yaml:"filter_complex"`
	Maps          []string `yaml:"maps"`

	// OutputExt supplies the output file extension, e.g. "mp4". The first
	// active choice that provides one wins.
	OutputExt string `yaml:"output_ext"`

	// Capability gates. A choice whose requirement is not met is hidden with an
	// explanation rather than silently producing a broken command
	// (plan.md §7, DECISIONS D17).
	RequiresEncoder string `yaml:"requires_encoder"`
	RequiresMuxer   string `yaml:"requires_muxer"`
	RequiresFilter  string `yaml:"requires_filter"`

	// Skips names later steps this choice makes irrelevant, e.g. choosing
	// stream copy skips the quality question entirely.
	Skips []string `yaml:"skips"`
}

// FilterTarget says which merged chain a choice's filters belong to.
type FilterTarget string

const (
	// FilterVideo contributes to -vf (the default).
	FilterVideo FilterTarget = "video"
	// FilterAudio contributes to -af.
	FilterAudio FilterTarget = "audio"
)

// Validate constrains the value of an input step.
type Validate struct {
	Regex string   `yaml:"regex"`
	Min   *float64 `yaml:"min"`
	Max   *float64 `yaml:"max"`
}

// Condition gates a step or expresses a nested combinator (plan.md §4.5).
//
// All the fields present in a single condition must hold (logical AND). Use Any
// or All for explicit grouping, and Not to negate.
type Condition struct {
	// InputHas requires the input file to contain a stream of this type:
	// video, audio or subtitles.
	InputHas string `yaml:"input_has"`

	// MinDuration requires the input to be at least this long. Accepts a Go
	// duration string ("10s", "1m30s") or a bare number of seconds.
	MinDuration *Duration `yaml:"min_duration"`

	// Platform requires the wizard to be running on this OS, e.g. "darwin".
	// It exists because ffmpeg lists hardware encoders for platforms other than
	// the one it is running on: h264_vaapi appears in a macOS build's -encoders
	// listing but can never open a device there, so a requires_encoder gate is
	// not enough to hide it (plan.md §7, DECISIONS D33).
	Platform string `yaml:"platform"`

	// AnswerIs and AnswerNot compare earlier answers, keyed by step id.
	AnswerIs  map[string]string `yaml:"answer_is"`
	AnswerNot map[string]string `yaml:"answer_not"`

	// Any holds when at least one nested condition holds.
	Any []Condition `yaml:"any"`
	// All holds when every nested condition holds.
	All []Condition `yaml:"all"`
	// Not inverts a nested condition.
	Not *Condition `yaml:"not"`
}

// Duration is a time.Duration that unmarshals from "10s", "1m30s" or a bare
// number of seconds.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case int:
		*d = Duration(time.Duration(v) * time.Second)
		return nil
	case float64:
		*d = Duration(time.Duration(v * float64(time.Second)))
		return nil
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			// Bare numbers as strings are common in YAML authors' heads.
			if secs, ferr := strconv.ParseFloat(v, 64); ferr == nil {
				*d = Duration(time.Duration(secs * float64(time.Second)))
				return nil
			}
			return fmt.Errorf("invalid duration %q: %w", v, err)
		}
		*d = Duration(parsed)
		return nil
	default:
		return fmt.Errorf("invalid duration %v (%T)", raw, raw)
	}
}

// String renders the duration for error messages.
func (d Duration) String() string { return time.Duration(d).String() }

package tasks

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// Engine runs a single task against a probed input.
type Engine struct {
	Task Task
	// Info is the probed input. Nil means "no file chosen yet", which makes
	// every input_has condition false.
	Info *ffmpeg.MediaInfo
	// Caps, when non-nil, is used to hide choices the local build cannot
	// produce. Nil means "assume everything is available", which is what the
	// golden tests want.
	Caps *ffmpeg.Capabilities
}

// Resolution is the outcome of walking a task's steps with a set of raw answers:
// which steps are active, and what the effective answers are.
type Resolution struct {
	// Active holds the steps that should be shown, in order.
	Active []Step
	// Answers holds the effective answer per active step, after defaults were
	// applied. Answers belonging to inactive steps are absent, which is what
	// plan.md §4.5 requires: a skipped step's answer must not leak into the
	// generated command when the step is re-enabled later.
	Answers map[string]string
	// Choices maps a choice step id to the resolved choice.
	Choices map[string]Choice
	// Skipped records why a step was left out, keyed by step id. The UI uses
	// this to explain a step the user answered but which no longer applies.
	Skipped map[string]string
	// HiddenChoices maps a choice step id to the choices that were filtered out
	// by capability requirements, so the UI can offer "why is X missing?".
	HiddenChoices map[string][]Choice

	// TimeScale is the product of the answers to time_scale steps: 2 means the
	// output plays twice as fast and is therefore half as long. It is 1 when no
	// step changes the playback speed.
	TimeScale float64
}

// Resolve walks the task's steps in order and works out which are active.
//
// Order matters and is why this is a single forward pass: a step's condition may
// only read answers from earlier steps (enforced at load time), and a choice's
// `skips` list can only switch off later steps.
func (e *Engine) Resolve(raw map[string]string) Resolution {
	res := Resolution{
		Answers:       map[string]string{},
		Choices:       map[string]Choice{},
		Skipped:       map[string]string{},
		HiddenChoices: map[string][]Choice{},
		TimeScale:     1,
	}

	skippedByAnswer := map[string]string{}

	for _, step := range e.Task.Steps {
		// A choice taken earlier in the task can switch this step off.
		if reason, skipped := skippedByAnswer[step.ID]; skipped {
			res.Skipped[step.ID] = reason
			continue
		}

		if !step.When.Eval(EvalContext{Info: e.Info, Answers: res.Answers}) {
			res.Skipped[step.ID] = "does not apply: " + step.When.Explain()
			continue
		}

		// Capability- and answer-filtered choices, computed before defaults so
		// that a default pointing at a hidden choice cannot be selected.
		available, hidden := e.availableChoices(step, res.Answers)
		if len(hidden) > 0 {
			res.HiddenChoices[step.ID] = hidden
		}
		if step.Type == StepChoice && len(available) == 0 {
			// The choices were removed by a `when` condition, by a missing
			// encoder/muxer/filter, or both; the message must not claim to know
			// which, because HiddenChoices carries the detail.
			res.Skipped[step.ID] = "no option is available for this input or this ffmpeg build"
			continue
		}

		value, ok := raw[step.ID]
		if step.Type == StepChoice {
			value, ok = selectChoice(step, available, value)
		} else if !ok || value == "" {
			value, ok = step.Default, step.Default != ""
		}

		if ok && value != "" {
			res.Answers[step.ID] = value
			if step.TimeScale {
				if factor, err := strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64); err == nil && factor > 0 {
					res.TimeScale *= factor
				}
			}
			for _, skip := range step.Skips {
				markSkipped(skippedByAnswer, skip, fmt.Sprintf("not needed when %s is answered", step.ID))
			}
			if step.Type == StepChoice {
				chosen := findChoice(available, value)
				res.Choices[step.ID] = chosen
				for _, skip := range chosen.Skips {
					markSkipped(skippedByAnswer, skip, fmt.Sprintf("not needed when %s is %q", step.ID, value))
				}
			}
		}

		res.Active = append(res.Active, step)
	}

	return res
}

// markSkipped records the first reason a step is switched off; a later skip of
// the same step does not overwrite it, because the first is the direct cause.
func markSkipped(reasons map[string]string, stepID, reason string) {
	if _, exists := reasons[stepID]; !exists {
		reasons[stepID] = reason
	}
}

// availableChoices splits a step's choices into the ones that apply here and the
// ones that do not, for either of two reasons: a `when` condition that does not
// hold, or a capability this ffmpeg build lacks. When Caps is nil every
// capability is assumed present.
func (e *Engine) availableChoices(step Step, answers map[string]string) (available, hidden []Choice) {
	for _, c := range step.Choices {
		if c.When != nil && !c.When.Eval(EvalContext{Info: e.Info, Answers: answers}) {
			hidden = append(hidden, c)
			continue
		}
		if e.unavailable(c) != "" {
			hidden = append(hidden, c)
			continue
		}
		available = append(available, c)
	}
	return available, hidden
}

// unavailable returns a human-readable reason when a choice cannot be produced
// by the local build, or "" when it can.
//
// It is exported as Unavailable for the UI, which needs the same explanation to
// answer "why is this option missing?" (plan.md §11).
func (e *Engine) unavailable(c Choice) string {
	if e.Caps == nil {
		return ""
	}
	switch {
	case c.RequiresEncoder != "" && !e.Caps.HasEncoder(c.RequiresEncoder):
		return fmt.Sprintf("this ffmpeg does not have the %s encoder", c.RequiresEncoder)
	case c.RequiresMuxer != "" && !e.Caps.HasMuxer(c.RequiresMuxer):
		return fmt.Sprintf("this ffmpeg does not have the %s muxer", c.RequiresMuxer)
	case c.RequiresFilter != "" && !e.Caps.HasFilter(c.RequiresFilter):
		// The usual cause is a build without the optional library a filter
		// needs, e.g. libass for the subtitles filter (DECISIONS D17).
		return fmt.Sprintf("this ffmpeg does not have the %s filter", c.RequiresFilter)
	}
	return ""
}

// Unavailable reports why a choice cannot be used with this ffmpeg build, or ""
// when it can. A nil Caps means no checks are made.
func (e *Engine) Unavailable(c Choice) string { return e.unavailable(c) }

// Applies reports whether a task can run against an input at all, plus a reason
// when it cannot. A task that requires video is meaningless for an MP3, and the
// task list must say why rather than offering a broken flow (plan.md §11).
func (t Task) Applies(info *ffmpeg.MediaInfo) (bool, string) {
	if info == nil {
		return true, ""
	}
	if t.Requires.Video && !info.Has(ffmpeg.StreamVideo) {
		return false, "needs an input with a video stream"
	}
	if t.Requires.Audio && !info.Has(ffmpeg.StreamAudio) {
		return false, "needs an input with an audio stream"
	}
	if t.Requires.Subtitles && !info.Has(ffmpeg.StreamSubtitle) {
		return false, "needs an input with a subtitle stream"
	}
	return true, ""
}

// selectChoice picks the answer for a choice step: the user's answer if it is
// available, otherwise the default, otherwise the single default-marked choice,
// otherwise the first available one.
func selectChoice(step Step, available []Choice, want string) (string, bool) {
	if want != "" && findChoiceOK(available, want) {
		return want, true
	}
	if step.Default != "" && findChoiceOK(available, step.Default) {
		return step.Default, true
	}
	for _, c := range available {
		if c.Default {
			return c.Value, true
		}
	}
	if len(available) > 0 {
		return available[0].Value, true
	}
	return "", false
}

func findChoice(choices []Choice, value string) Choice {
	for _, c := range choices {
		if c.Value == value {
			return c
		}
	}
	return Choice{Value: value}
}

func findChoiceOK(choices []Choice, value string) bool {
	for _, c := range choices {
		if c.Value == value {
			return true
		}
	}
	return false
}

// BuildOptions supplies the parts of the request that are not answers.
type BuildOptions struct {
	// Input is the probed file path.
	Input string
	// Output is the destination path. When empty, DefaultOutput is used.
	Output string
	// Overwrite permits clobbering an existing file; false by default
	// (plan.md §8).
	Overwrite bool
}

// Build resolves the answers and produces a runnable ffmpeg command.
func (e *Engine) Build(raw map[string]string, opts BuildOptions) (*ffmpeg.Command, Resolution, error) {
	res := e.Resolve(raw)

	if opts.Input == "" {
		return nil, res, errors.New("no input file")
	}

	req := ffmpeg.Request{
		Inputs:           []ffmpeg.InputSpec{{Path: opts.Input}},
		Output:           opts.Output,
		Overwrite:        opts.Overwrite,
		ExpectedDuration: e.expectedDuration(res),
	}

	// Argument placement follows the field docs in schema.go and DECISIONS D19:
	// input options before -i, everything else in step order after the inputs.
	for _, step := range res.Active {
		choice, isChoice := res.Choices[step.ID]
		if !isChoice {
			// A non-choice step contributes through its template.
			outputArgs, inputArgs := templateArgs(step, res.Answers[step.ID])
			req.Inputs[0].Args = append(req.Inputs[0].Args, inputArgs...)
			req.OutputArgs = append(req.OutputArgs, outputArgs...)
			continue
		}

		req.Inputs[0].Args = append(req.Inputs[0].Args, choice.InputArgs...)
		req.OutputArgs = append(req.OutputArgs, choice.Args...)
		req.Maps = append(req.Maps, choice.Maps...)
		if choice.FilterComplex != nil {
			req.FilterComplex = append(req.FilterComplex, choice.FilterComplex...)
		}
		switch choice.Target {
		case FilterAudio:
			req.AudioFilters = append(req.AudioFilters, choice.Filters...)
		default:
			req.VideoFilters = append(req.VideoFilters, choice.Filters...)
		}
		// OutputArgs are emitted after filters and maps, which matters when a
		// flag such as -f depends on a mapping having been established.
		req.OutputArgs = append(req.OutputArgs, choice.OutputArgs...)
	}

	if opts.Output == "" {
		out, err := DefaultOutput(opts.Input, res)
		if err != nil {
			return nil, res, err
		}
		req.Output = out
	}

	cmd, err := ffmpeg.Build(req)
	return cmd, res, err
}

// DefaultOutput derives an output path next to the input, e.g.
// media/clip.mp4 -> media/clip_convert_video.mp4 (plan.md §6.5).
func DefaultOutput(input string, res Resolution) (string, error) {
	if input == "" {
		return "", errors.New("no input file")
	}
	ext := res.OutputExt()
	if ext == "" {
		return "", errors.New("no output extension: the task's answers did not select a container")
	}
	dir, base := filepath.Split(input)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		return "", fmt.Errorf("cannot derive an output name from %q", input)
	}
	return filepath.Join(dir, stem+"_out."+strings.TrimPrefix(ext, ".")), nil
}

// OutputExt returns the extension contributed by the first active choice that
// provides one, or "".
func (r Resolution) OutputExt() string {
	// Steps are stored in order, so walk them rather than the map, which would
	// be non-deterministic when several steps set an extension.
	for _, step := range r.Active {
		if c, ok := r.Choices[step.ID]; ok && c.OutputExt != "" {
			return c.OutputExt
		}
	}
	return ""
}

// expectedDuration is how long the output should be, used for the progress bar
// (DECISIONS D3).
//
// For tasks that do not change the length that is the input duration. A trim
// shortens it, which M5 adds through the same mechanism; a speed change scales
// it through Resolution.TimeScale. A zero result means "unknown", and the UI
// falls back to an indeterminate bar.
func (e *Engine) expectedDuration(res Resolution) time.Duration {
	if e.Info == nil {
		return 0
	}
	duration := e.Info.Duration()
	if res.TimeScale > 0 && res.TimeScale != 1 {
		duration = time.Duration(float64(duration) / res.TimeScale)
	}
	return duration
}

var placeholderRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// templateArgs expands a step's templates into arguments: the output-side ones
// from Template (e.g. ["-t", "{value}"]) and the input-side ones from
// InputTemplate (e.g. ["-ss", "{value}"]).
//
// Only {value} is supported. Anything else is a load-time validation error, so
// this cannot silently emit a literal "{foo}" into a command.
func templateArgs(step Step, value string) (outputArgs, inputArgs []string) {
	if value == "" {
		return nil, nil
	}
	expand := func(tmpl []string) []string {
		args := make([]string, 0, len(tmpl))
		for _, t := range tmpl {
			args = append(args, strings.ReplaceAll(t, "{value}", value))
		}
		return args
	}
	return expand(step.Template), expand(step.InputTemplate)
}

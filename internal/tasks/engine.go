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
	// TaskID is the id of the task that was resolved. It is carried here so
	// DefaultOutput can name the output after the task (plan.md §6.5) without
	// every caller having to thread the task through.
	TaskID string
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
	// Shown is the inverse: the choices a step will actually offer, in order.
	// The UI needs it to render a choice step without re-deriving the gates.
	Shown map[string][]Choice
	// Pending holds the ids of active steps that have no answer yet: no default,
	// and nothing given. Their args appear as placeholder tokens so the live
	// preview can show what is still missing (plan.md §6).
	Pending map[string]bool

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
		TaskID:        e.Task.ID,
		Answers:       map[string]string{},
		Choices:       map[string]Choice{},
		Skipped:       map[string]string{},
		HiddenChoices: map[string][]Choice{},
		Shown:         map[string][]Choice{},
		Pending:       map[string]bool{},
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
		if step.Type == StepChoice {
			res.Shown[step.ID] = available
		}
		if step.Type == StepChoice && len(available) == 0 {
			// The choices were removed by a `when` condition, by a missing
			// encoder/muxer/filter, or both; the message must not claim to know
			// which, because HiddenChoices carries the detail.
			res.Skipped[step.ID] = "no option is available for this input or this ffmpeg build"
			continue
		}

		value, source := e.answerFor(step, available, raw[step.ID])
		if source == sourceNone {
			// Nothing could be settled: no answer, no default, and - for a
			// choice step - nothing available. Only these steps become a
			// placeholder in the preview; a default or a lone option is shown
			// as the value it will take, because that is what the step screen
			// highlights.
			res.Pending[step.ID] = true
		}

		if value != "" {
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

// answerSource records where an answer came from, which is what separates a
// deliberate choice from one the engine had to invent to produce a command.
type answerSource int

const (
	// sourceNone means the step is active but has no answer at all.
	sourceNone answerSource = iota
	// sourceUser means the user answered the step.
	sourceUser
	// sourceDefault means the step's declared default was used.
	sourceDefault
	// sourceInferred means there was no default and the engine picked the first
	// available option so that the command is complete.
	sourceInferred
)

// answerFor decides the answer for one step.
func (e *Engine) answerFor(step Step, available []Choice, given string) (string, answerSource) {
	if step.Type == StepChoice {
		return selectChoice(step, available, given)
	}
	if given != "" {
		return given, sourceUser
	}
	if step.Default != "" {
		return step.Default, sourceDefault
	}
	return "", sourceNone
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
// available, otherwise the step's default, otherwise the single default-marked
// choice in step order, and failing everything the first available option, so
// that a step with only one legal answer (VP9 in WebM) needs no YAML default.
func selectChoice(step Step, available []Choice, want string) (string, answerSource) {
	if want != "" && findChoiceOK(available, want) {
		return want, sourceUser
	}
	if step.Default != "" && findChoiceOK(available, step.Default) {
		return step.Default, sourceDefault
	}
	for _, c := range available {
		if c.Default {
			return c.Value, sourceDefault
		}
	}
	if len(available) > 0 {
		return available[0].Value, sourceInferred
	}
	return "", sourceNone
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
	cmd, _, res, err := e.assemble(raw, opts, modeCommand)
	return cmd, res, err
}

// Preview is a command as far as the answers go: the same request, but the
// arguments of steps that have no answer yet are replaced by a placeholder token
// (plan.md §6). It lets the UI show what is still missing without guessing.
type Preview struct {
	Command *ffmpeg.Command
	// Pending lists the ids of the active steps that contributed a placeholder,
	// in step order.
	Pending []string
}

// Preview resolves the answers and renders the command so far.
//
// The placeholder for a step is a single "<step-id>" token placed where that
// step's arguments will go. It is a real argument as far as ffmpeg.Build is
// concerned - it is only ever displayed, never executed - so the preview obeys
// exactly the same ordering rules as the command that will finally be run.
func (e *Engine) Preview(raw map[string]string, opts BuildOptions) (Preview, Resolution, error) {
	cmd, pending, res, err := e.assemble(raw, opts, modePreview)
	if err != nil {
		return Preview{}, res, err
	}
	return Preview{Command: cmd, Pending: pending}, res, nil
}

// placeholderFor is the marker a pending step leaves in the preview.
func placeholderFor(stepID string) string { return "<" + stepID + ">" }

type buildMode int

const (
	// modeCommand produces the command that will be executed.
	modeCommand buildMode = iota
	// modePreview marks unanswered steps instead of pretending they are settled.
	modePreview
)

// assemble turns a resolution into an ffmpeg request. It is shared by Build and
// Preview so the two can never disagree about argument order.
func (e *Engine) assemble(raw map[string]string, opts BuildOptions, mode buildMode) (*ffmpeg.Command, []string, Resolution, error) {
	res := e.Resolve(raw)

	if opts.Input == "" {
		return nil, nil, res, errors.New("no input file")
	}

	req := ffmpeg.Request{
		Inputs:           []ffmpeg.InputSpec{{Path: opts.Input}},
		Output:           opts.Output,
		Overwrite:        opts.Overwrite,
		ExpectedDuration: e.expectedDuration(res),
	}

	var pending []string

	// Argument placement follows the field docs in schema.go and DECISIONS D19:
	// input options before -i, everything else in step order after the inputs.
	for _, step := range res.Active {
		if mode == modePreview && res.Pending[step.ID] {
			// One token where the arguments will appear, and nothing else: the
			// filters and maps of an unsettled step must not leak into the
			// preview either, or the preview would show a command that could
			// never be produced.
			//
			// A step that contributes input options (a seek, say) puts its
			// token before -i, because that is where its real arguments go:
			// showing -ss after the output name would be a preview of a command
			// ffmpeg could not run.
			pending = append(pending, step.ID)
			if len(step.InputTemplate) > 0 {
				req.Inputs[0].Args = append(req.Inputs[0].Args, placeholderFor(step.ID))
			} else {
				req.OutputArgs = append(req.OutputArgs, placeholderFor(step.ID))
			}
			continue
		}

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
			// A preview before any container is chosen has nowhere to write.
			// That is not an error worth showing: the user has not finished.
			if mode == modePreview {
				out = ""
			} else {
				return nil, nil, res, err
			}
		}
		req.Output = out
	}
	if req.Output == "" {
		req.Output = placeholderFor("output")
		pending = append(pending, "output")
	}

	cmd, err := ffmpeg.Build(req)
	return cmd, pending, res, err
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
	suffix := res.TaskID
	if suffix == "" {
		// A Resolution built by hand (tests, and the `--dump` path before a
		// task is chosen) has no id; "out" keeps the name unique and obvious.
		suffix = "out"
	}
	return filepath.Join(dir, stem+"_"+suffix+"."+strings.TrimPrefix(ext, ".")), nil
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

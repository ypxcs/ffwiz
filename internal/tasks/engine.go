package tasks

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
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
	// Platform is the GOOS a `platform` condition is compared against. Empty
	// means the running platform; the golden tests set it explicitly so a task
	// gated on hardware encoders resolves the same way everywhere.
	Platform string
}

// platform is the OS that `platform` conditions are evaluated against.
func (e *Engine) platform() string {
	if e.Platform != "" {
		return e.Platform
	}
	return runtime.GOOS
}

// evalContext is everything a condition on this task can see.
func (e *Engine) evalContext(answers map[string]string) EvalContext {
	return EvalContext{Info: e.Info, Answers: answers, Platform: e.platform()}
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

	// Notes holds a plain-language explanation of a calculated answer, keyed by
	// step id: the "about 1196 kbit/s of video" line that plan.md §5.2 asks to
	// show alongside a target-size question. Steps that are not calculated have
	// no entry.
	Notes map[string]string
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
		Notes:         map[string]string{},
		TimeScale:     1,
	}

	skippedByAnswer := map[string]string{}

	for _, step := range e.Task.Steps {
		// A choice taken earlier in the task can switch this step off.
		if reason, skipped := skippedByAnswer[step.ID]; skipped {
			res.Skipped[step.ID] = reason
			continue
		}

		if !step.When.Eval(e.evalContext(res.Answers)) {
			res.Skipped[step.ID] = "does not apply: " + step.When.Explain()
			continue
		}

		// Capability- and answer-filtered choices, computed before defaults so
		// that a default pointing at a hidden choice cannot be selected.
		available, hidden := e.availableChoices(step, res.Answers)
		if len(hidden) > 0 {
			res.HiddenChoices[step.ID] = hidden
		}
		if step.Type == StepChoice || step.Type == StepMulti || step.Type == StepResolution {
			res.Shown[step.ID] = available
		}
		if (step.Type == StepChoice || step.Type == StepMulti) && len(available) == 0 {
			// The choices were removed by a `when` condition, by a missing
			// encoder/muxer/filter, or both; the message must not claim to know
			// which, because HiddenChoices carries the detail.
			res.Skipped[step.ID] = "no option is available for this input or this ffmpeg build"
			continue
		}

		value, source := e.answerFor(step, available, raw[step.ID], present(raw, step.ID))
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
			if step.Type == StepResolution {
				// A resolution's presets contribute no arguments of their own -
				// the step's template does that - but they may still switch a
				// later step off. A typed custom value matches no preset and so
				// skips nothing, which is the honest reading of it.
				for _, skip := range findChoice(available, value).Skips {
					markSkipped(skippedByAnswer, skip, fmt.Sprintf("not needed when %s is %q", step.ID, value))
				}
			}
			if step.Type == StepMulti {
				// A multi step's `skips` come from every choice that is ticked,
				// so that unticking one puts the later step back.
				for _, c := range selectedChoices(available, value) {
					for _, skip := range c.Skips {
						markSkipped(skippedByAnswer, skip, fmt.Sprintf("not needed when %s includes %q", step.ID, c.Value))
					}
				}
			}
			if step.Compute != nil {
				// A calculated answer explains itself: the same arithmetic that
				// produces its arguments also produces the sentence the wizard
				// shows next to the question (plan.md §5.2).
				if plan, err := e.planBitrate(step, res.Answers); err == nil {
					res.Notes[step.ID] = planNote(plan)
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
//
// given is the raw answer and present says whether the answer map held the key
// at all, which is the difference between "the user ticked nothing" and "the
// user has not reached this step": both read as "", but only one of them should
// fall back to a default.
func (e *Engine) answerFor(step Step, available []Choice, given string, answered bool) (string, answerSource) {
	switch step.Type {
	case StepChoice:
		return selectChoice(step, available, given)
	case StepMulti:
		return selectMulti(step, available, given, answered)
	case StepResolution:
		return selectResolution(step, available, given)
	case StepConfirm:
		return selectConfirm(step, given)
	default:
		if given != "" {
			return given, sourceUser
		}
		if step.Default != "" {
			return step.Default, sourceDefault
		}
		return "", sourceNone
	}
}

// present reports whether the answers map carries a key, which is how a
// deliberately empty answer (every checkbox unticked) is told apart from an
// unanswered step.
func present(answers map[string]string, id string) bool {
	_, ok := answers[id]
	return ok
}

// selectMulti settles a multi step: the ticked values that are actually on
// offer, joined in the order they are offered.
//
// An answer that is present but empty means every box was unticked, and that is
// an answer: falling back to the default there would re-tick boxes the user
// cleared.
func selectMulti(step Step, available []Choice, given string, answered bool) (string, answerSource) {
	if answered {
		return normaliseMulti(available, given), sourceUser
	}
	if v := normaliseMulti(available, step.Default); v != "" {
		return v, sourceDefault
	}
	if len(available) == 1 {
		return available[0].Value, sourceInferred
	}
	return "", sourceNone
}

// normaliseMulti keeps only the ticked values that are offered, in offer order.
func normaliseMulti(available []Choice, answer string) string {
	selected := selectedChoices(available, answer)
	if len(selected) == 0 {
		return ""
	}
	values := make([]string, 0, len(selected))
	for _, c := range selected {
		values = append(values, c.Value)
	}
	return strings.Join(values, ",")
}

// selectResolution settles a resolution step. A value that is not one of the
// presets is a typed one, which is allowed only when the step offers a custom
// field and only when it passes the step's own validation.
func selectResolution(step Step, available []Choice, given string) (string, answerSource) {
	if given != "" && !findChoiceOK(available, given) {
		if step.Custom && step.ValidateValue(given) == nil {
			return given, sourceUser
		}
		// Neither a preset nor a legal value: fall back to the default rather
		// than build a command that would fail.
		return selectChoice(step, available, "")
	}
	return selectChoice(step, available, given)
}

// selectConfirm settles a yes/no step.
//
// A confirm never has "no answer": a toggle that adds something must default to
// not adding it, so an undeclared answer is "no" rather than a placeholder. The
// source is *inferred* in that case, which keeps it distinguishable from a
// default the YAML asked for.
func selectConfirm(step Step, given string) (string, answerSource) {
	if given != "" {
		return normaliseYesNo(given), sourceUser
	}
	if step.Default != "" {
		return normaliseYesNo(step.Default), sourceDefault
	}
	return confirmNo, sourceInferred
}

// Answer values for a confirm step. They are spelled out because they appear in
// `when: {answer_is: ...}` conditions in YAML.
const (
	confirmYes = "yes"
	confirmNo  = "no"
)

// normaliseYesNo maps the spellings a YAML author might use onto the two
// canonical answers, so `default: true` and `default: "yes"` behave the same.
func normaliseYesNo(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "on", "1":
		return confirmYes
	default:
		return confirmNo
	}
}

// Answer reports whether a confirm-style answer means "yes".
func (r Resolution) Yes(stepID string) bool {
	return r.Answers[stepID] == confirmYes
}

// Selected returns the choices ticked in a multi step, in the order they are
// offered. The answer is a comma-separated list of choice values.
func (r Resolution) Selected(stepID string) []Choice {
	return selectedChoices(r.Shown[stepID], r.Answers[stepID])
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
		if c.When != nil && !c.When.Eval(e.evalContext(answers)) {
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

// selectedChoices picks the ticked choices out of a multi answer, keeping the
// order they are offered rather than the order they were ticked.
func selectedChoices(choices []Choice, answer string) []Choice {
	if answer == "" {
		return nil
	}
	wanted := strings.Split(answer, ",")
	var out []Choice
	for _, c := range choices {
		for _, v := range wanted {
			if c.Value == v {
				out = append(out, c)
				break
			}
		}
	}
	return out
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
//
// It says nothing about the local ffmpeg build; use AppliesTo for that.
func (t Task) Applies(info *ffmpeg.MediaInfo) (bool, string) {
	return t.AppliesTo(info, nil)
}

// AppliesTo additionally checks the task's capability requirement against the
// local build. A nil caps means "assume everything is present", which is what a
// task list before detection has to assume.
//
// The input and the build are checked together because they produce the same
// user-facing answer - "can I do this?" - and the task list shows one reason.
// The build is checked second: a task that the file cannot support at all is the
// more useful explanation.
func (t Task) AppliesTo(info *ffmpeg.MediaInfo, caps *ffmpeg.Capabilities) (bool, string) {
	if info != nil {
		if t.Requires.Video && !info.Has(ffmpeg.StreamVideo) {
			return false, "needs an input with a video stream"
		}
		if t.Requires.Audio && !info.Has(ffmpeg.StreamAudio) {
			return false, "needs an input with an audio stream"
		}
		if t.Requires.Subtitles && !info.Has(ffmpeg.StreamSubtitle) {
			return false, "needs an input with a subtitle stream"
		}
	}
	if caps != nil && t.RequiresFilter != "" && !caps.HasFilter(t.RequiresFilter) {
		// Phrased to follow "this task ...", which is how both the task list and
		// `--dump` introduce the reason.
		return false, fmt.Sprintf("needs an ffmpeg with the %s filter, which this build does not have", t.RequiresFilter)
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

		if step.Compute != nil {
			// The arguments are calculated rather than written down, so they
			// cannot be rendered before the input has been measured.
			args, err := e.computedArgs(step, res)
			if err != nil {
				return nil, nil, res, err
			}
			req.OutputArgs = append(req.OutputArgs, args...)
			continue
		}

		switch step.Type {
		case StepChoice:
			applyChoice(&req, res.Choices[step.ID])

		case StepResolution:
			// The step's template turns the answer into arguments, whether it
			// came from a preset or from the custom field.
			outputArgs, inputArgs := templateArgs(step, res)
			req.Inputs[0].Args = append(req.Inputs[0].Args, inputArgs...)
			req.OutputArgs = append(req.OutputArgs, outputArgs...)

		case StepMulti:
			// Every ticked option contributes, in the order it is offered, so
			// the command does not depend on the order the user ticked them.
			for _, c := range res.Selected(step.ID) {
				applyChoice(&req, c)
			}

		case StepConfirm:
			if !res.Yes(step.ID) {
				continue
			}
			req.OutputArgs = append(req.OutputArgs, step.Args...)
			if len(step.Filters) > 0 {
				if step.Target == FilterAudio {
					req.AudioFilters = append(req.AudioFilters, step.Filters...)
				} else {
					req.VideoFilters = append(req.VideoFilters, step.Filters...)
				}
			}

		default:
			// A non-choice step contributes through its template.
			outputArgs, inputArgs := templateArgs(step, res)
			req.Inputs[0].Args = append(req.Inputs[0].Args, inputArgs...)
			req.OutputArgs = append(req.OutputArgs, outputArgs...)
			if len(step.Filters) > 0 {
				filters := templatedFilters(step, res)
				if step.Target == FilterAudio {
					req.AudioFilters = append(req.AudioFilters, filters...)
				} else {
					req.VideoFilters = append(req.VideoFilters, filters...)
				}
			}
		}
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

// applyChoice folds one selected option into the request. It is shared by
// single-choice steps and by every ticked option of a multi step, so the two
// cannot drift apart in argument order.
func applyChoice(req *ffmpeg.Request, c Choice) {
	req.Inputs[0].Args = append(req.Inputs[0].Args, c.InputArgs...)
	req.OutputArgs = append(req.OutputArgs, c.Args...)
	req.Maps = append(req.Maps, c.Maps...)
	if c.FilterComplex != nil {
		req.FilterComplex = append(req.FilterComplex, c.FilterComplex...)
	}
	switch c.Target {
	case FilterAudio:
		req.AudioFilters = append(req.AudioFilters, c.Filters...)
	default:
		req.VideoFilters = append(req.VideoFilters, c.Filters...)
	}
	// AudioFilters land in -af regardless of Target, so a choice can change both
	// the picture and the sound.
	req.AudioFilters = append(req.AudioFilters, c.AudioFilters...)
	// OutputArgs are emitted after filters and maps, which matters when a
	// flag such as -f depends on a mapping having been established.
	req.OutputArgs = append(req.OutputArgs, c.OutputArgs...)
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
// The input's length is the starting point. A trim shortens it, a speed change
// scales it through Resolution.TimeScale. A zero result means "unknown", and the
// UI falls back to an indeterminate bar.
func (e *Engine) expectedDuration(res Resolution) time.Duration {
	if e.Info == nil {
		return 0
	}
	duration := e.Info.Duration()

	// A time_range step cuts the output down; several of them would apply to
	// the same timeline, so the last one wins rather than compounding, which is
	// what the command would actually do.
	for _, step := range res.Active {
		if step.Type != StepTimeRange {
			continue
		}
		if r, err := ParseRange(res.Answers[step.ID]); err == nil {
			duration = r.Duration()
		}
	}

	if res.TimeScale > 0 && res.TimeScale != 1 {
		duration = time.Duration(float64(duration) / res.TimeScale)
	}
	return duration
}

// expand replaces every {name} in a template with its value. Names left out of
// vars are left in place: unreachable for a validated task, but visible rather
// than silent if it ever happens.
func expand(tmpl []string, vars map[string]string) []string {
	args := make([]string, 0, len(tmpl))
	for _, t := range tmpl {
		args = append(args, placeholderRe.ReplaceAllStringFunc(t, func(m string) string {
			name := placeholderRe.FindStringSubmatch(m)[1]
			if v, ok := vars[name]; ok {
				return v
			}
			return m
		}))
	}
	return args
}

var placeholderRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// templateArgs expands a step's templates into arguments: the output-side ones
// from Template (e.g. ["-t", "{value}"]) and the input-side ones from
// InputTemplate (e.g. ["-ss", "{start}"]).
//
// The placeholders a step may use depend on its type, and anything else is a
// load-time validation error, so this cannot silently emit a literal "{foo}"
// into a command:
//
//   - {value}: the step's answer, for input, resolution and custom values.
//   - {start}, {end}, {duration}: the parts of a time_range answer.
func templateArgs(step Step, res Resolution) (outputArgs, inputArgs []string) {
	answer := res.Answers[step.ID]
	if answer == "" {
		return nil, nil
	}

	vars := map[string]string{"value": answer}
	if step.Type == StepTimeRange {
		if r, err := ParseRange(answer); err == nil {
			vars["start"] = FormatTimecode(r.Start)
			vars["end"] = FormatTimecode(r.End)
			vars["duration"] = FormatTimecode(r.Duration())
		}
	}
	return expand(step.Template, vars), expand(step.InputTemplate, vars)
}

// templatedFilters expands a step's filters with the answer, escaping it for
// filter syntax.
//
// The escaping is not cosmetic. A path goes inside a filter argument, where
// ffmpeg parses two levels deep - the filtergraph level splits on "," and ";"
// and reads "[label]" links, and the argument level splits "key:key" and
// "key=value" - so a name containing any of those characters changes the meaning
// of the filter unless it is escaped. EscapeFilterValue is where that lives, and
// where it was measured (DECISIONS D39).
func templatedFilters(step Step, res Resolution) []string {
	answer := res.Answers[step.ID]
	if answer == "" {
		return nil
	}
	vars := map[string]string{"value": ffmpeg.EscapeFilterValue(answer)}
	return expand(step.Filters, vars)
}

// planBitrate works out the bitrate plan for a compute step from its answer and
// the answers of the steps it reads.
func (e *Engine) planBitrate(step Step, answers map[string]string) (ffmpeg.BitratePlan, error) {
	if step.Compute == nil {
		return ffmpeg.BitratePlan{}, errors.New("this step has no calculation")
	}
	if e.Info == nil {
		return ffmpeg.BitratePlan{}, errors.New("cannot plan a bitrate without a probed input")
	}
	target, err := strconv.ParseFloat(strings.TrimSpace(answers[step.ID]), 64)
	if err != nil {
		return ffmpeg.BitratePlan{}, fmt.Errorf("%q is not a size in MiB", answers[step.ID])
	}
	audioKbps := step.Compute.AudioKbps
	if step.Compute.AudioStep != "" {
		// A stream copy or a codec name is not a bitrate, and then the declared
		// AudioKbps stands in for it.
		if kbps, ok := ffmpeg.ParseBitrateKbps(answers[step.Compute.AudioStep]); ok {
			audioKbps = kbps
		}
	}
	return ffmpeg.PlanBitrate(target, e.Info.Duration(), audioKbps, step.Compute.Reserve, step.Compute.MinKbps)
}

// computedArgs renders a compute step's arguments, and the note that explains
// them.
func (e *Engine) computedArgs(step Step, res Resolution) ([]string, error) {
	plan, err := e.planBitrate(step, res.Answers)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{
		"value":   res.Answers[step.ID],
		"bitrate": strconv.Itoa(plan.VideoKbps) + "k",
		"maxrate": strconv.Itoa(plan.MaxKbps) + "k",
		"bufsize": strconv.Itoa(plan.BufsizeKbps) + "k",
	}
	return expand(step.Compute.Template, vars), nil
}

// planNote is the sentence that shows the arithmetic, e.g.
// "about 1.2 Mbit/s of video, so 10 MiB fits in 1:00".
func planNote(plan ffmpeg.BitratePlan) string {
	parts := []string{
		fmt.Sprintf("about %s of video", format.BitRate(int64(plan.VideoKbps)*1000)),
	}
	spoken := make([]string, 0, 2)
	if plan.AudioKbps > 0 {
		spoken = append(spoken, fmt.Sprintf("%d kbit/s of audio", plan.AudioKbps))
	}
	spoken = append(spoken, fmt.Sprintf("%.0f%% of container overhead", plan.Reserve*100))
	parts = append(parts, "after "+strings.Join(spoken, " and "))
	parts = append(parts, fmt.Sprintf("so %.0f MiB fits in %s", plan.TargetMiB, format.Clock(plan.Duration)))
	return strings.Join(parts, ", ")
}

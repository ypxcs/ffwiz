package tasks

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Source is the file a task was loaded from. It is not part of the YAML.
// (Declared here next to the validation that uses it.)

// Validate checks a whole set. Every problem is reported with the file, task id
// and step id that caused it, because a typo in YAML that silently disables a
// question is the most expensive bug this project can have (plan.md §4.7).
func (s *Set) Validate() error {
	var errs []error
	seenTasks := map[string]bool{}

	for i := range s.Tasks {
		task := &s.Tasks[i]
		if task.ID == "" {
			errs = append(errs, fmt.Errorf("%s: task is missing an id", task.Source))
			continue
		}
		if seenTasks[task.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate task id %q", task.Source, task.ID))
			continue
		}
		seenTasks[task.ID] = true

		errs = append(errs, task.validate()...)
	}

	return errors.Join(errs...)
}

func (t *Task) validate() []error {
	var errs []error
	at := func(format string, args ...any) error {
		return fmt.Errorf("%s: task %s: %s", t.Source, t.ID, fmt.Sprintf(format, args...))
	}

	if t.Label == "" {
		errs = append(errs, at("missing label"))
	}
	if len(t.Steps) == 0 {
		errs = append(errs, at("has no steps"))
	}
	if t.RequiresFilter != "" && !capabilityNameRe.MatchString(t.RequiresFilter) {
		errs = append(errs, at("implausible requires_filter %q", t.RequiresFilter))
	}

	stepIDs := make(map[string]int, len(t.Steps))
	for i, step := range t.Steps {
		if step.ID == "" {
			errs = append(errs, at("step %d is missing an id", i))
			continue
		}
		if _, dup := stepIDs[step.ID]; dup {
			errs = append(errs, at("duplicate step id %q", step.ID))
		}
		stepIDs[step.ID] = i
	}

	for i := range t.Steps {
		errs = append(errs, t.validateStep(i, stepIDs)...)
	}
	errs = append(errs, t.validateFilterGraphs()...)
	return errs
}

// videoFilterSteps returns, in task order, the ids of steps that contribute a
// simple video filter - either through one of their choices or, for an input
// step, through filters of their own.
func (t *Task) videoFilterSteps() []string {
	var ids []string
	for _, step := range t.Steps {
		if len(step.Filters) > 0 && step.Target != FilterAudio {
			ids = append(ids, step.ID)
			continue
		}
		for _, c := range step.Choices {
			if len(c.Filters) > 0 && c.Target != FilterAudio {
				ids = append(ids, step.ID)
				break
			}
		}
	}
	return ids
}

// containsPlaceholder reports whether any of the templates uses {name}.
func containsPlaceholder(templates []string, name string) bool {
	want := "{" + name + "}"
	for _, t := range templates {
		if strings.Contains(t, want) {
			return true
		}
	}
	return false
}

// validateFilterGraphs enforces the rule that a complex filtergraph and simple
// filters cannot be mixed on one stream.
//
// ffmpeg 8.1.1 refuses the combination outright:
//
//	Filtergraph 'scale=64:-1' was specified for a stream fed from a complex
//	filtergraph. Simple and complex filtering cannot be used together for the
//	same stream.
//
// so a task that produces such a command is broken at run time with a cryptic
// message. The fix belongs in the YAML, where it is one `skips` entry, and it is
// caught here at load time instead.
func (t *Task) validateFilterGraphs() []error {
	videoSteps := t.videoFilterSteps()
	if len(videoSteps) == 0 {
		return nil
	}

	var errs []error
	at := func(format string, args ...any) error {
		return fmt.Errorf("%s: task %s: %s", t.Source, t.ID, fmt.Sprintf(format, args...))
	}

	for _, step := range t.Steps {
		for _, c := range step.Choices {
			if len(c.FilterComplex) == 0 {
				continue
			}
			for _, id := range videoSteps {
				if id == step.ID {
					continue
				}
				if !slices.Contains(c.Skips, id) {
					errs = append(errs, at("step %s: choice %q builds a filtergraph, but step %q adds a simple video filter; "+
						"add %q to that choice's skips, because ffmpeg cannot mix simple and complex filtering on one stream",
						step.ID, c.Value, id, id))
				}
			}
		}
	}
	return errs
}

func (t *Task) validateStep(i int, stepIDs map[string]int) []error {
	step := t.Steps[i]
	var errs []error
	at := func(format string, args ...any) error {
		return fmt.Errorf("%s: task %s: step %s: %s", t.Source, t.ID, step.ID, fmt.Sprintf(format, args...))
	}
	if step.Prompt == "" {
		errs = append(errs, at("missing prompt"))
	}

	// skips may only point forward: the engine resolves steps in a single pass.
	for _, skip := range step.Skips {
		errs = append(errs, checkForwardRef(skip, "skips", stepIDs, i, at)...)
	}

	switch step.Type {
	case StepChoice, StepMulti:
		if len(step.Choices) == 0 {
			errs = append(errs, at("type %s requires at least one choice", step.Type))
		}
		defaults := 0
		unconditionalDefaults := 0
		seen := map[string]bool{}
		for j, c := range step.Choices {
			if c.Label == "" {
				errs = append(errs, at("choice %d is missing a label", j))
			}
			if c.Value == "" {
				errs = append(errs, at("choice %q is missing a value", c.Label))
			}
			if seen[c.Value] {
				errs = append(errs, at("duplicate choice value %q", c.Value))
			}
			seen[c.Value] = true
			if c.Default {
				defaults++
				if c.When == nil {
					unconditionalDefaults++
				}
			}
			errs = append(errs, t.validateChoice(step, i, stepIDs, c, at)...)
			for _, skip := range c.Skips {
				errs = append(errs, checkForwardRef(skip, "choice "+c.Value+" skips", stepIDs, i, at)...)
			}
		}
		// Several conditional defaults are legitimate - the audio step picks
		// "copy" for MP4 and "Opus" for WebM - and the engine takes the first
		// available one. Two unconditional defaults are always a contradiction.
		if unconditionalDefaults > 1 {
			errs = append(errs, at("%d unconditional choices are marked default; exactly one is allowed", unconditionalDefaults))
		}
		if step.Default != "" && !seen[step.Default] {
			errs = append(errs, at("default %q is not one of the choices", step.Default))
		}
		if step.Type == StepMulti {
			// A multi step with nothing ticked has no answer at all, so it must
			// say what to start from or the preview can never settle.
			if step.Default == "" {
				errs = append(errs, at("type multi needs a default; without one there is nothing to start from"))
			}
			errs = append(errs, t.validateMulti(step, at)...)
		}

	case StepResolution:
		// A resolution step is a list of presets plus, optionally, room to type
		// a value of your own. Its arguments come from the step's template, so
		// its choices must not carry any of their own.
		if len(step.Choices) == 0 {
			errs = append(errs, at("type resolution requires at least one preset"))
		}
		defaults := 0
		seen := map[string]bool{}
		for j, c := range step.Choices {
			if c.Label == "" {
				errs = append(errs, at("preset %d is missing a label", j))
			}
			if c.Value == "" {
				errs = append(errs, at("preset %q is missing a value", c.Label))
			}
			if seen[c.Value] {
				errs = append(errs, at("duplicate preset value %q", c.Value))
			}
			seen[c.Value] = true
			if c.Default {
				defaults++
			}
			if len(c.Args) > 0 || len(c.InputArgs) > 0 || len(c.OutputArgs) > 0 ||
				len(c.Filters) > 0 || len(c.FilterComplex) > 0 || len(c.Maps) > 0 {
				errs = append(errs, at("preset %q carries arguments of its own; "+
					"a resolution step's arguments come from its template", c.Value))
			}
			if c.OutputExt != "" {
				errs = append(errs, at("preset %q sets output_ext; "+
					"the container is chosen by a different step", c.Value))
			}
			for _, skip := range c.Skips {
				errs = append(errs, checkForwardRef(skip, "preset "+c.Value+" skips", stepIDs, i, at)...)
			}
			for _, err := range c.When.validate(stepIDs, i) {
				errs = append(errs, at("preset %q: %v", c.Value, err))
			}
		}
		if defaults > 1 {
			errs = append(errs, at("%d presets are marked default; exactly one is allowed", defaults))
		}
		if step.Default != "" && !seen[step.Default] {
			errs = append(errs, at("default %q is not one of the presets", step.Default))
		}
		if len(step.Template) == 0 {
			errs = append(errs, at("type resolution needs a template that uses {value}"))
		}
		if len(step.InputTemplate) > 0 {
			errs = append(errs, at("type resolution cannot contribute input options"))
		}
		if step.Custom && step.Validate == nil {
			errs = append(errs, at("custom: true needs a validate block to bound the typed value"))
		}

	case StepConfirm:
		if len(step.Choices) > 0 {
			errs = append(errs, at("type confirm must not declare choices"))
		}
		if len(step.Template) > 0 || len(step.InputTemplate) > 0 {
			errs = append(errs, at("type confirm must not declare templates; use args"))
		}
		for j, a := range step.Args {
			if strings.TrimSpace(a) == "" {
				errs = append(errs, at("args entry %d is empty", j))
			}
		}

	case StepInput, StepTimeRange:
		if len(step.Choices) > 0 {
			errs = append(errs, at("type %s must not declare choices", step.Type))
		}
		if len(step.Args) > 0 {
			errs = append(errs, at("type %s contributes through its template, not args", step.Type))
		}
		if len(step.Filters) > 0 && step.Type == StepInput && !containsPlaceholder(step.Filters, "value") {
			// A filter on an input step is a template, so a step that forgets
			// the placeholder would silently contribute a constant filter while
			// its answer went nowhere - the answer typed into the question would
			// have no effect on the command.
			errs = append(errs, at("filters on an input step must use {value}; without it the answer never reaches the command"))
		}

	case "":
		errs = append(errs, at("missing type"))

	default:
		errs = append(errs, at("unknown type %q", step.Type))
	}

	// Templates must only use the placeholders their type provides; anything
	// else would be emitted literally into a command line.
	templates := [][]string{step.Template, step.InputTemplate}
	if step.Type == StepInput {
		templates = append(templates, step.Filters)
	}
	for _, tmpl := range templates {
		for _, arg := range tmpl {
			for _, m := range placeholderRe.FindAllStringSubmatch(arg, -1) {
				if !validPlaceholder(step.Type, m[1]) {
					errs = append(errs, at("template placeholder {%s} is not available for type %s (%s)",
						m[1], step.Type, placeholderHelp(step.Type)))
				}
			}
		}
	}

	if step.Target != "" && step.Target != FilterVideo && step.Target != FilterAudio {
		errs = append(errs, at("unknown filter target %q", step.Target))
	}
	if step.Target == FilterAudio && len(step.Filters) == 0 {
		errs = append(errs, at("target: audio without any filters"))
	}
	if step.Target != "" && len(step.Filters) == 0 {
		errs = append(errs, at("target is only meaningful with filters"))
	}

	// A step contributes to a filter chain either as a template (an input step,
	// expanded and escaped by the engine) or literally (a confirm step). Every
	// other type's filters would never be emitted at all.
	switch step.Type {
	case StepInput, StepConfirm:
	default:
		if len(step.Filters) > 0 {
			errs = append(errs, at("filters are only meaningful for an input or confirm step, not %s", step.Type))
		}
	}
	if step.Type == StepConfirm {
		// A confirm's filters are literal, so a placeholder in one would be
		// written into the command line unexpanded.
		for _, f := range step.Filters {
			if placeholderRe.MatchString(f) {
				errs = append(errs, at("a confirm step's filters are literal; %q contains a placeholder", f))
			}
		}
	}

	if step.Custom && step.Type != StepResolution {
		errs = append(errs, at("custom is only meaningful for type resolution"))
	}
	if len(step.InputTemplate) > 0 && step.Type == StepChoice {
		errs = append(errs, at("input_template is only meaningful for input-style steps, not %s", step.Type))
	}
	if len(step.Template) > 0 && step.Type == StepChoice {
		errs = append(errs, at("template is only meaningful for input-style steps, not %s", step.Type))
	}

	if v := step.Validate; v != nil {
		if !step.Type.AcceptsValueValidation() {
			errs = append(errs, at("validate is only meaningful for steps whose answer is a value, not %s", step.Type))
		}
		if v.Regex != "" {
			if _, err := regexp.Compile(v.Regex); err != nil {
				errs = append(errs, at("validate.regex does not compile: %v", err))
			}
		}
		if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
			errs = append(errs, at("validate.min (%v) is greater than validate.max (%v)", *v.Min, *v.Max))
		}
		if v.WithinInput && step.Type != StepInput {
			// A time_range is checked against the input's length by
			// CheckRangeAnswer, which knows about both of its times.
			errs = append(errs, at("validate.within_input is only meaningful for a step whose answer is a timestamp, not %s", step.Type))
		}
	}

	// Condition errors are wrapped separately so they carry the task and step,
	// which the condition validator cannot know about.
	for _, err := range step.When.validate(stepIDs, i) {
		errs = append(errs, at("%v", err))
	}

	if step.TimeScale {
		switch step.Type {
		case StepInput, StepTimeRange:
		case StepChoice:
			for _, c := range step.Choices {
				if v, err := strconv.ParseFloat(strings.TrimSuffix(c.Value, "x"), 64); err != nil || v <= 0 {
					errs = append(errs, at("time_scale choice %q is not a positive number; "+
						"the answer is a multiplier, e.g. \"2\" or \"0.5\"", c.Value))
				}
			}
		default:
			errs = append(errs, at("time_scale is not meaningful for type %q", step.Type))
		}
	}

	if step.Compute != nil {
		errs = append(errs, t.validateCompute(step, stepIDs, i, at)...)
	}

	return errs
}

// validateCompute checks a step whose arguments are calculated rather than
// written down.
func (t *Task) validateCompute(step Step, stepIDs map[string]int, index int, at func(string, ...any) error) []error {
	var errs []error
	c := step.Compute

	switch c.Kind {
	case "video_bitrate":
	default:
		errs = append(errs, at("compute.kind %q is unknown; the only kind is \"video_bitrate\"", c.Kind))
	}

	switch step.Type {
	case StepChoice, StepInput:
	default:
		errs = append(errs, at("compute needs an answer that is a number of MiB, so type must be choice or input, not %s", step.Type))
	}

	if len(c.Template) == 0 {
		errs = append(errs, at("compute.template is empty; there would be nothing to add to the command"))
	}
	if len(step.Template) > 0 || len(step.InputTemplate) > 0 {
		errs = append(errs, at("compute and template cannot both be set; the calculated arguments come from compute.template"))
	}
	if len(step.Args) > 0 || len(step.Filters) > 0 {
		errs = append(errs, at("compute contributes through compute.template, not args or filters"))
	}
	if c.Reserve < 0 || c.Reserve >= 1 {
		errs = append(errs, at("compute.reserve must be a fraction between 0 and 1, got %v", c.Reserve))
	}
	if c.MinKbps < 0 {
		errs = append(errs, at("compute.min_kbps cannot be negative, got %d", c.MinKbps))
	}
	if c.AudioKbps < 0 {
		errs = append(errs, at("compute.audio_kbps cannot be negative, got %d", c.AudioKbps))
	}
	if c.AudioStep != "" {
		idx, ok := stepIDs[c.AudioStep]
		if !ok {
			errs = append(errs, at("compute.audio_step references unknown step %q", c.AudioStep))
		} else if idx >= index {
			errs = append(errs, at("compute.audio_step references step %q, which is not earlier in the task", c.AudioStep))
		}
	}

	for _, arg := range c.Template {
		if strings.TrimSpace(arg) == "" {
			errs = append(errs, at("compute.template has an empty entry"))
		}
		for _, m := range placeholderRe.FindAllStringSubmatch(arg, -1) {
			switch m[1] {
			case "bitrate", "maxrate", "bufsize", "value":
			default:
				errs = append(errs, at("compute.template placeholder {%s} is not available (want {bitrate}, {maxrate}, {bufsize} or {value})", m[1]))
			}
		}
	}

	// The answer has to be a size in MiB, whether it comes from a preset or is
	// typed in.
	checkSize := func(where, value string) {
		if v, err := strconv.ParseFloat(value, 64); err != nil || v <= 0 {
			errs = append(errs, at("%s %q is not a positive number of MiB", where, value))
		}
	}
	if step.Type == StepChoice {
		for _, ch := range step.Choices {
			checkSize("compute choice value", ch.Value)
		}
	} else if step.Default != "" {
		checkSize("compute default", step.Default)
	}

	return errs
}

func (t *Task) validateChoice(step Step, stepIndex int, stepIDs map[string]int, c Choice, at func(string, ...any) error) []error {
	var errs []error

	// DECISIONS D2: a filtergraph without an explicit map makes ffmpeg fail
	// with "Output file does not contain any stream".
	if len(c.FilterComplex) > 0 && len(c.Maps) == 0 {
		errs = append(errs, at("choice %q has filter_complex but no maps", c.Value))
	}
	if len(c.Maps) > 0 && len(c.FilterComplex) == 0 && step.Type != StepChoice {
		// Harmless, but almost always a mistake worth flagging.
		errs = append(errs, at("choice %q has maps without filter_complex", c.Value))
	}

	if c.Target != "" && c.Target != FilterVideo && c.Target != FilterAudio {
		errs = append(errs, at("choice %q has unknown filter target %q", c.Value, c.Target))
	}
	if c.Target == FilterAudio && len(c.Filters) == 0 {
		errs = append(errs, at("choice %q sets target: audio but contributes no filters", c.Value))
	}

	for name, args := range map[string][]string{
		"args": c.Args, "input_args": c.InputArgs, "output_args": c.OutputArgs,
		"filters": c.Filters, "audio_filters": c.AudioFilters,
		"filter_complex": c.FilterComplex, "maps": c.Maps,
	} {
		for j, a := range args {
			if strings.TrimSpace(a) == "" {
				errs = append(errs, at("choice %q has an empty %s entry at index %d", c.Value, name, j))
			}
		}
	}

	// A flag given without its value ("-crf") is always a mistake.
	checkFlagPairs := func(name string, args []string) {
		for j, a := range args {
			if a == "-c:v" || a == "-c:a" || a == "-crf" || a == "-b:v" || a == "-b:a" || a == "-map" || a == "-vf" || a == "-af" || a == "-f" {
				if j+1 >= len(args) {
					errs = append(errs, at("choice %q has %s ending with the flag %q and no value", c.Value, name, a))
				}
			}
		}
	}
	checkFlagPairs("args", c.Args)
	checkFlagPairs("output_args", c.OutputArgs)
	checkFlagPairs("input_args", c.InputArgs)

	for kind, name := range map[string]string{
		"requires_encoder": c.RequiresEncoder,
		"requires_muxer":   c.RequiresMuxer,
		"requires_filter":  c.RequiresFilter,
	} {
		if name != "" && !capabilityNameRe.MatchString(name) {
			errs = append(errs, at("choice %q has implausible %s %q", c.Value, kind, name))
		}
	}

	if c.OutputExt != "" && strings.HasPrefix(c.OutputExt, ".") {
		errs = append(errs, at("choice %q has output_ext %q; write it without the leading dot", c.Value, c.OutputExt))
	}

	// A choice condition may only read earlier steps, for the same reason a
	// step condition may: that is all the engine knows when it evaluates it.
	for _, err := range c.When.validate(stepIDs, stepIndex) {
		errs = append(errs, at("choice %q: %v", c.Value, err))
	}

	return errs
}

// capabilityNameRe mirrors the shape of a real encoder/muxer/filter name.
var capabilityNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)

// validPlaceholder reports whether a step type provides a placeholder, so that
// {start} in an input step is a load-time error rather than a literal "{start}"
// in a command line.
func validPlaceholder(t StepType, name string) bool {
	switch name {
	case "value":
		return t.AcceptsValueValidation()
	case "start", "end", "duration":
		return t == StepTimeRange
	default:
		return false
	}
}

// placeholderHelp lists the placeholders a type provides, for the error message.
func placeholderHelp(t StepType) string {
	if t == StepTimeRange {
		return "want {start}, {end} or {duration}"
	}
	if t.AcceptsValueValidation() {
		return "want {value}"
	}
	return "this type has no templates"
}

// validateMulti checks a multi step's extra rules.
func (t *Task) validateMulti(step Step, at func(string, ...any) error) []error {
	var errs []error
	// A multi step's answer is a comma-separated list of values, so a value
	// containing a comma could never be selected unambiguously.
	for _, c := range step.Choices {
		if strings.Contains(c.Value, ",") {
			errs = append(errs, at("multi choice value %q contains a comma, which is the separator between selections", c.Value))
		}
	}
	for _, v := range strings.Split(step.Default, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		found := false
		for _, c := range step.Choices {
			if c.Value == v {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, at("default %q is not one of the choices", v))
		}
	}
	if step.TimeScale {
		errs = append(errs, at("time_scale is not meaningful for type multi"))
	}
	return errs
}

func checkForwardRef(ref, context string, stepIDs map[string]int, current int, at func(string, ...any) error) []error {
	idx, ok := stepIDs[ref]
	if !ok {
		return []error{at("%s references unknown step %q", context, ref)}
	}
	if idx <= current {
		return []error{at("%s references step %q, which is not later in the task; "+
			"a step can only switch off steps that follow it", context, ref)}
	}
	return nil
}

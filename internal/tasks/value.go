package tasks

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ValidateValue reports why an answer is not acceptable for this step, or nil.
//
// Load-time validation (validate.go) proves the *rule* is well formed; this
// checks a *value* against it, which the wizard does before letting the user
// move on. It lives here rather than in internal/wizard so the UI never owns a
// schema rule (plan.md §3, rule 3).
//
// Only input-style steps are constrained: a choice step's answers come from the
// task definition and capability filtering has already removed the ones this
// build cannot produce.
func (s Step) ValidateValue(value string) error {
	if !s.Type.AcceptsValueValidation() {
		// Choices are validated against the available options by the caller,
		// which is the only place that knows what survived capability gating.
		return nil
	}

	if s.Validate == nil {
		return nil
	}
	v := s.Validate

	if v.Regex != "" {
		re, err := regexp.Compile(v.Regex)
		if err != nil {
			// Unreachable for a loaded task, but a clear error beats a panic if
			// a Step is built in Go rather than parsed from YAML.
			return fmt.Errorf("this step's validation rule is broken: %w", err)
		}
		if !re.MatchString(value) {
			return fmt.Errorf("%q does not look right: it must match %s", value, v.Regex)
		}
	}

	if v.Min != nil || v.Max != nil {
		n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", value)
		}
		if v.Min != nil && n < *v.Min {
			return fmt.Errorf("%s is below the minimum of %v", value, *v.Min)
		}
		if v.Max != nil && n > *v.Max {
			return fmt.Errorf("%s is above the maximum of %v", value, *v.Max)
		}
	}

	return nil
}

// AcceptsValueValidation reports whether a step's answer is a value that a
// validate block can constrain.
//
// It is exported because the loader uses it to reject a rule that would never
// run, and the two must agree: a `validate` block on a choice is rejected at
// load time, and this is the same question asked at run time.
func (t StepType) AcceptsValueValidation() bool {
	switch t {
	case StepChoice, StepMulti, StepConfirm:
		// The answer comes from a fixed list, so a rule would either restate the
		// list or never fire.
		return false
	case StepConcat:
		// The answer is a list of paths, and a regex over several lines would be
		// unreadable; the step's own rule (CheckConcatAnswer) is what constrains
		// it (DECISIONS D41).
		return false
	default:
		return true
	}
}

// ErrValueRequired is returned for an empty answer to a step whose value would
// be written into the command.
var ErrValueRequired = errors.New("a value is required")

// RequiresValue reports whether the step must have a non-empty answer before it
// can be left. A step that contributes arguments cannot be left blank: the
// template would emit an empty string straight into the command line, and a
// step that contributes a filter would silently contribute nothing.
//
// A confirm never requires a value: it is a toggle, and an unanswered toggle
// means "no" rather than "ask me again" (see selectConfirm).
func (s Step) RequiresValue() bool {
	switch s.Type {
	case StepChoice, StepMulti, StepConfirm, StepConcat:
		// A concat step's files *are* its contribution: with none chosen there
		// is no input to read at all, so it cannot be left unanswered the way an
		// optional text field can.
		return true
	default:
		return len(s.Template) > 0 || len(s.InputTemplate) > 0 || len(s.Filters) > 0
	}
}

// CheckAnswer validates an answer to this step, refusing an empty one when the
// step needs a value.
func (s Step) CheckAnswer(value string) error {
	if s.RequiresValue() && strings.TrimSpace(value) == "" {
		return ErrValueRequired
	}
	return s.ValidateValue(value)
}

// CheckTimeAnswer validates a timestamp answer against the last usable point on
// the input, when the step asks for one (validate.within_input).
//
// Measured: a seek to the end of a clip selects no frame at all. ffmpeg then
// writes nothing, prints nothing about it and exits 0, so the failure would show
// up as "the file was not written" with no explanation (DECISIONS D36). A limit
// of zero means the input's length is unknown, and the answer is only checked
// for being a time.
func (s Step) CheckTimeAnswer(answer string, limit time.Duration) error {
	if s.Validate == nil || !s.Validate.WithinInput {
		return nil
	}
	t, err := ParseTime(answer)
	if err != nil {
		return err
	}
	if limit > 0 && t > limit {
		return fmt.Errorf("%s is past the end of the clip; the last usable point is %s",
			FormatTimecode(t), FormatTimecode(limit))
	}
	return nil
}

// ── time ranges ─────────────────────────────────────────────────────────────

// Range is a start and an end on the input's timeline.
type Range struct {
	Start time.Duration
	End   time.Duration
}

// Duration is how much of the input the range keeps.
func (r Range) Duration() time.Duration { return r.End - r.Start }

// ErrNotATime is returned for a time that cannot be read.
var ErrNotATime = errors.New("not a time")

// ParseTime reads a time in any of the forms ffmpeg accepts and that a person
// is likely to type: plain seconds ("30", "12.5"), or a clock ("0:30",
// "00:00:30.250", "1:02:03").
//
// A bare number is seconds, not minutes: `-ss 90` seeks to 1:30, and a parser
// that read "90" as 90 minutes would silently produce a wrong command.
func ParseTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: it is empty", ErrNotATime)
	}

	if !strings.Contains(s, ":") {
		secs, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrNotATime, s)
		}
		if secs < 0 {
			return 0, fmt.Errorf("%w: %q is negative", ErrNotATime, s)
		}
		return time.Duration(secs * float64(time.Second)).Round(time.Millisecond), nil
	}

	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("%w: %q has too many colons", ErrNotATime, s)
	}
	var total float64
	for i, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("%w: %q", ErrNotATime, s)
		}
		// The last field is seconds; the ones before it are minutes and hours.
		switch len(parts) - 1 - i {
		case 0:
			total += v
		case 1:
			total += v * 60
		default:
			total += v * 3600
		}
	}
	return time.Duration(total * float64(time.Second)).Round(time.Millisecond), nil
}

// FormatTimecode renders a duration the way it appears in a command preview and
// in a saved answer: HH:MM:SS.mmm. A single spelling means a value that has been
// through the wizard once is read back unchanged.
func FormatTimecode(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Round(time.Millisecond)
	hours := ms / time.Hour
	ms -= hours * time.Hour
	minutes := ms / time.Minute
	ms -= minutes * time.Minute
	return fmt.Sprintf("%02d:%02d:%06.3f", hours, minutes, ms.Seconds())
}

// rangeSeparator joins the two halves of a time_range answer. A comma cannot
// appear inside a time, so the split is unambiguous.
const rangeSeparator = ","

// ParseRange reads a time_range answer ("start,end") into its two times.
func ParseRange(answer string) (Range, error) {
	start, end, ok := strings.Cut(answer, rangeSeparator)
	if !ok {
		return Range{}, fmt.Errorf("%w: %q is not a start,end pair", ErrNotATime, answer)
	}
	s, err := ParseTime(start)
	if err != nil {
		return Range{}, fmt.Errorf("start: %w", err)
	}
	e, err := ParseTime(end)
	if err != nil {
		return Range{}, fmt.Errorf("end: %w", err)
	}
	if e <= s {
		return Range{}, fmt.Errorf("the end (%s) is not after the start (%s)",
			FormatTimecode(e), FormatTimecode(s))
	}
	return Range{Start: s, End: e}, nil
}

// CheckRangeAnswer validates a time_range answer against the input's length.
//
// A range that runs past the end of the input is not an error ffmpeg reports
// clearly - it simply writes a shorter file - so the wizard refuses it here
// rather than letting the user think they cut to 5 minutes of a 2 minute clip.
// A zero length means the input duration is unknown, and only the ordering of
// the two times is checked.
func (s Step) CheckRangeAnswer(answer string, input time.Duration) error {
	if strings.TrimSpace(answer) == "" {
		return ErrValueRequired
	}
	r, err := ParseRange(answer)
	if err != nil {
		return err
	}
	if input > 0 && r.End > input+time.Millisecond {
		return fmt.Errorf("the end (%s) is past the end of the input (%s)",
			FormatTimecode(r.End), FormatTimecode(input))
	}
	return nil
}

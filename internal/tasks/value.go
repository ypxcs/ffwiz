package tasks

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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
			return fmt.Errorf("this step's validation rule is broken: %v", err)
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
	default:
		return true
	}
}

// ErrValueRequired is returned for an empty answer to a step whose value would
// be written into the command.
var ErrValueRequired = errors.New("a value is required")

// RequiresValue reports whether the step must have a non-empty answer before it
// can be left. A step that contributes arguments cannot be left blank: the
// template would emit an empty string straight into the command line.
func (s Step) RequiresValue() bool {
	switch s.Type {
	case StepChoice, StepMulti, StepConfirm:
		return true
	default:
		return len(s.Template) > 0 || len(s.InputTemplate) > 0
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

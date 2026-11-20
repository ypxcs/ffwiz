package tasks

import (
	"errors"
	"strings"
	"testing"
)

// ptr returns a pointer to a float, for the Min/Max fields.
func ptr(f float64) *float64 { return &f }

func TestValidateValue(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		value   string
		wantErr string
	}{
		{
			name:  "no rule accepts anything",
			step:  Step{Type: StepInput},
			value: "whatever",
		},
		{
			name:  "regex accepts a match",
			step:  Step{Type: StepInput, Validate: &Validate{Regex: `^[0-9:.]+$`}},
			value: "1:02:03",
		},
		{
			name:    "regex rejects a mismatch",
			step:    Step{Type: StepInput, Validate: &Validate{Regex: `^[0-9:.]+$`}},
			value:   "half past two",
			wantErr: "does not look right",
		},
		{
			name:  "min accepts a value on the boundary",
			step:  Step{Type: StepInput, Validate: &Validate{Min: ptr(10)}},
			value: "10",
		},
		{
			name:    "min rejects a value below it",
			step:    Step{Type: StepInput, Validate: &Validate{Min: ptr(10)}},
			value:   "9.5",
			wantErr: "below the minimum",
		},
		{
			name:  "max accepts a value on the boundary",
			step:  Step{Type: StepInput, Validate: &Validate{Max: ptr(60)}},
			value: "60",
		},
		{
			name:    "max rejects a value above it",
			step:    Step{Type: StepInput, Validate: &Validate{Max: ptr(60)}},
			value:   "61",
			wantErr: "above the maximum",
		},
		{
			name:  "numbers may have surrounding space",
			step:  Step{Type: StepInput, Validate: &Validate{Min: ptr(1)}},
			value: " 2 ",
		},
		{
			name:    "a number is required for a range rule",
			step:    Step{Type: StepInput, Validate: &Validate{Min: ptr(1)}},
			value:   "two",
			wantErr: "is not a number",
		},
		{
			// A choice's answer comes from its own list, so a rule here would
			// either duplicate that list or silently never run; the loader
			// rejects the combination, and this is the runtime half of the same
			// decision.
			name:  "a rule on a choice step is ignored",
			step:  Step{Type: StepChoice, Validate: &Validate{Regex: `^never$`}},
			value: "anything",
		},
		{
			name:    "a broken rule is reported rather than panicking",
			step:    Step{Type: StepInput, Validate: &Validate{Regex: `([`}},
			value:   "x",
			wantErr: "broken",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.step.ValidateValue(tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateValue(%q) = %v, want nil", tc.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateValue(%q) succeeded, want an error mentioning %q", tc.value, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateValue(%q) = %q, want it to mention %q", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestStepRequiresValue(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want bool
	}{
		{
			// The value is substituted into the arguments, so an empty one would
			// emit an empty string straight into the command line.
			name: "input step with an output template",
			step: Step{Type: StepInput, Template: []string{"-t", "{value}"}},
			want: true,
		},
		{
			name: "input step with an input template",
			step: Step{Type: StepInput, InputTemplate: []string{"-ss", "{value}"}},
			want: true,
		},
		{
			name: "input step with no template",
			step: Step{Type: StepInput},
			want: false,
		},
		{
			name: "choice step",
			step: Step{Type: StepChoice},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.step.RequiresValue(); got != tc.want {
				t.Errorf("RequiresValue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckAnswer(t *testing.T) {
	step := Step{
		Type:     StepInput,
		Template: []string{"-t", "{value}"},
		Validate: &Validate{Regex: `^[0-9]+$`},
	}

	// An empty answer to a step that contributes arguments is refused before the
	// rule is even consulted, because the rule would accept "" for some steps.
	err := step.CheckAnswer("   ")
	if !errors.Is(err, ErrValueRequired) {
		t.Errorf("CheckAnswer of a blank answer = %v, want ErrValueRequired", err)
	}

	if err := step.CheckAnswer("30"); err != nil {
		t.Errorf("CheckAnswer(30) = %v, want nil", err)
	}

	// A bad value is reported by the rule, not as a missing value.
	err = step.CheckAnswer("thirty")
	if err == nil || errors.Is(err, ErrValueRequired) {
		t.Errorf("CheckAnswer(thirty) = %v, want a rule error", err)
	}

	// A step that contributes nothing accepts an empty answer: there is nothing
	// to put into the command either way.
	plain := Step{Type: StepInput}
	if err := plain.CheckAnswer(""); err != nil {
		t.Errorf("CheckAnswer on a template-less step = %v, want nil", err)
	}
}

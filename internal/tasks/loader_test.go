package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// minimalYAML is a valid one-step task, used as the base for override tests.
const minimalYAML = `
- id: base
  label: Base task
  description: A task defined in the built-in set.
  steps:
    - id: quality
      prompt: Quality
      hint: The original hint.
      type: choice
      choices:
        - { label: Balanced, value: balanced, args: ["-crf", "23"], default: true }
        - { label: Small, value: small, args: ["-crf", "28"] }
`

func loadWith(t *testing.T, builtin, user string) (*Set, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	if builtin != "" {
		fsys["builtin/tasks.yaml"] = &fstest.MapFile{Data: []byte(builtin)}
	}
	dir := t.TempDir()
	if user != "" {
		if err := os.WriteFile(filepath.Join(dir, "user.yaml"), []byte(user), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(LoadOptions{BuiltinFS: fsys, UserDir: dir})
}

func TestLoadBuiltin(t *testing.T) {
	set, err := loadWith(t, minimalYAML, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := set.IDs(); len(got) != 1 || got[0] != "base" {
		t.Fatalf("IDs() = %v, want [base]", got)
	}
	task, ok := set.Get("base")
	if !ok {
		t.Fatal("Get(base) failed")
	}
	if task.Source != "tasks.yaml" {
		t.Errorf("Source = %q, want tasks.yaml", task.Source)
	}
	if task.Steps[0].Hint != "The original hint." {
		t.Errorf("Hint = %q", task.Steps[0].Hint)
	}
}

func TestLoadMissingUserDirIsFine(t *testing.T) {
	fsys := fstest.MapFS{"builtin/tasks.yaml": &fstest.MapFile{Data: []byte(minimalYAML)}}
	set, err := Load(LoadOptions{
		BuiltinFS: fsys,
		UserDir:   filepath.Join(t.TempDir(), "does", "not", "exist"),
	})
	if err != nil {
		t.Fatalf("Load with a missing user directory: %v", err)
	}
	if len(set.Tasks) != 1 {
		t.Errorf("loaded %d tasks, want 1", len(set.Tasks))
	}
}

func TestLoadUserOverridesPatchFields(t *testing.T) {
	user := `
- id: base
  steps:
    - id: quality
      hint: The user's hint.
`
	set, err := loadWith(t, minimalYAML, user)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	task, _ := set.Get("base")

	if got := task.Steps[0].Hint; got != "The user's hint." {
		t.Errorf("Hint = %q, want the override", got)
	}
	// Everything not mentioned must survive: a user patching one hint must not
	// silently lose the choices.
	if got := task.Label; got != "Base task" {
		t.Errorf("Label = %q, want it unchanged", got)
	}
	if len(task.Steps[0].Choices) != 2 {
		t.Errorf("Choices = %+v, want both to survive the merge", task.Steps[0].Choices)
	}
	if task.Steps[0].Prompt != "Quality" {
		t.Errorf("Prompt = %q, want it unchanged", task.Steps[0].Prompt)
	}
	// The source names both files, so a later validation error is actionable.
	if !strings.Contains(task.Source, "user.yaml") {
		t.Errorf("Source = %q, want it to name the user file", task.Source)
	}
}

func TestLoadUserOverrideReplacesStepsField(t *testing.T) {
	// A step id that is not in the built-in task is appended, so a user can add
	// a question of their own.
	user := `
- id: base
  steps:
    - id: extra
      prompt: Extra question?
      type: confirm
`
	set, err := loadWith(t, minimalYAML, user)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	task, _ := set.Get("base")
	if len(task.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(task.Steps))
	}
	if task.Steps[0].ID != "quality" || task.Steps[1].ID != "extra" {
		t.Errorf("step order = %s,%s; want the built-in first", task.Steps[0].ID, task.Steps[1].ID)
	}
}

func TestLoadUserCanReplaceTopLevelFields(t *testing.T) {
	user := `
- id: base
  label: My own label
  disabled: true
`
	set, err := loadWith(t, minimalYAML, user)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	task, _ := set.Get("base")
	if task.Label != "My own label" {
		t.Errorf("Label = %q", task.Label)
	}
	if !task.Disabled {
		t.Error("Disabled = false, want the override to take effect")
	}
}

func TestLoadUserAddsNewTask(t *testing.T) {
	user := `
- id: mine
  label: My task
  steps:
    - id: only
      prompt: Only question
      type: confirm
`
	set, err := loadWith(t, minimalYAML, user)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := set.IDs(); len(got) != 2 {
		t.Fatalf("IDs() = %v, want the built-in and the user's", got)
	}
	if _, ok := set.Get("mine"); !ok {
		t.Error("the user's new task was not loaded")
	}
}

func TestLoadReportsBadYAML(t *testing.T) {
	tests := []struct {
		name    string
		builtin string
		user    string
		want    string
	}{
		{
			name:    "not a list",
			builtin: "id: base\n",
			want:    "expected a list of tasks",
		},
		{
			name:    "duplicate task id",
			builtin: minimalYAML + minimalYAML,
			want:    "duplicate task id",
		},
		{
			name:    "missing id",
			builtin: "- label: No id\n  steps: []\n",
			want:    "missing an id",
		},
		{
			name:    "missing label",
			builtin: "- id: x\n  steps:\n    - { id: a, prompt: A, type: confirm }\n",
			want:    "missing label",
		},
		{
			name:    "no steps",
			builtin: "- id: x\n  label: X\n",
			want:    "no steps",
		},
		{
			name:    "unknown step type",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: magic }\n",
			want:    "unknown type",
		},
		{
			name:    "duplicate step id",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: confirm }\n    - { id: a, prompt: B, type: confirm }\n",
			want:    "duplicate step id",
		},
		{
			name:    "skip to unknown step",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: confirm, skips: [nope] }\n",
			want:    "unknown step",
		},
		{
			name:    "skip backwards",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: confirm }\n    - { id: b, prompt: B, type: confirm, skips: [a] }\n",
			want:    "not later in the task",
		},
		{
			name:    "choice without a value",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: No value }]\n",
			want:    "missing a value",
		},
		{
			name:    "duplicate choice value",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: One, value: v }, { label: Two, value: v }]\n",
			want:    "duplicate choice value",
		},
		{
			name:    "two unconditional defaults",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: One, value: v, default: true }, { label: Two, value: w, default: true }]\n",
			want:    "exactly one is allowed",
		},
		{
			name:    "default is not a choice",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      default: nope\n      choices: [{ label: One, value: v }]\n",
			want:    "not one of the choices",
		},
		{
			name:    "choice type without choices",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: choice }\n",
			want:    "requires at least one choice",
		},
		{
			name:    "input type with choices",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: input, choices: [{ label: L, value: v }] }\n",
			want:    "must not declare choices",
		},
		{
			name:    "template on a choice step",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: choice, template: [\"-t\", \"{value}\"], choices: [{ label: L, value: v }] }\n",
			want:    "only meaningful for input-style steps",
		},
		{
			name:    "validate on a choice step",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: choice, validate: { regex: \"^v$\" }, choices: [{ label: L, value: v }] }\n",
			want:    "only meaningful for steps whose answer is a value",
		},
		{
			name:    "unknown template placeholder",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: input, template: [\"-ss\", \"{start}\"] }\n",
			want:    "not available for type input",
		},
		{
			name:    "bad regex",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: input, validate: { regex: \"[\" } }\n",
			want:    "does not compile",
		},
		{
			name:    "inverted min and max",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: input, validate: { min: 10, max: 1 } }\n",
			want:    "greater than",
		},
		{
			name:    "filtergraph without maps",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, filter_complex: [\"[0:v]gif[out]\"] }]\n",
			want:    "no maps",
		},
		{
			name:    "unknown filter target",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, filters: [\"x\"], target: sideways }]\n",
			want:    "unknown filter target",
		},
		{
			name:    "output_ext with a leading dot",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, output_ext: \".mp4\" }]\n",
			want:    "without the leading dot",
		},
		{
			name:    "dangling flag",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, args: [\"-c:v\"] }]\n",
			want:    "no value",
		},
		{
			name:    "implausible encoder name",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, requires_encoder: \"not a name\" }]\n",
			want:    "implausible",
		},
		{
			name:    "condition on an unknown step",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: confirm, when: { answer_is: { nope: yes } } }\n",
			want:    "unknown step",
		},
		{
			name:    "condition on a later step",
			builtin: "- id: x\n  label: X\n  steps:\n    - { id: a, prompt: A, type: confirm, when: { answer_is: { b: yes } } }\n    - { id: b, prompt: B, type: confirm }\n",
			want:    "not earlier in the task",
		},
		{
			name:    "time_scale on a choice with a non-numeric value",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      time_scale: true\n      choices: [{ label: L, value: fast }]\n",
			want:    "not a positive number",
		},
		{
			name:    "audio target without filters",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, target: audio }]\n",
			want:    "contributes no filters",
		},
		{
			name:    "filtergraph mixed with a simple video filter",
			builtin: "- id: x\n  label: X\n  steps:\n    - id: a\n      prompt: A\n      type: choice\n      choices: [{ label: L, value: v, filter_complex: [\"[0:v]gif[out]\"], maps: [\"[out]\"] }]\n    - id: b\n      prompt: B\n      type: choice\n      choices: [{ label: M, value: w, filters: [\"scale=320:-1\"] }]\n",
			want:    "cannot mix simple and complex filtering",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWith(t, tc.builtin, tc.user)
			if err == nil {
				t.Fatalf("Load accepted invalid YAML (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
			// Errors must locate the problem: file, task and step.
			if !strings.Contains(err.Error(), "tasks.yaml") && !strings.Contains(err.Error(), "user.yaml") {
				t.Errorf("error %q does not name the file", err)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	// Three separate mistakes in one task must all be reported: fixing YAML one
	// error per run is miserable.
	bad := `
- id: x
  label: X
  steps:
    - { id: a, prompt: A, type: choice }
    - { id: b, prompt: B, type: magic }
`
	_, err := loadWith(t, bad, "")
	if err == nil {
		t.Fatal("Load accepted an invalid task")
	}
	msg := err.Error()
	for _, want := range []string{"requires at least one choice", "unknown type"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
}

func TestLoadRejectsEmptyBuiltinSet(t *testing.T) {
	if _, err := loadWith(t, "", ""); err == nil {
		t.Fatal("Load succeeded with no built-in tasks")
	}
}

func TestLoadIgnoresNonYAMLFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"builtin/tasks.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
		"builtin/README.md":  &fstest.MapFile{Data: []byte("# not a task\n")},
		"builtin/notes.txt":  &fstest.MapFile{Data: []byte("also not\n")},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := Load(LoadOptions{BuiltinFS: fsys, UserDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := set.IDs(); len(got) != 1 {
		t.Errorf("IDs() = %v, want only the YAML task", got)
	}
}

func TestLoadOrderIsDeterministic(t *testing.T) {
	fsys := fstest.MapFS{
		"builtin/b.yaml": &fstest.MapFile{Data: []byte("- id: b\n  label: B\n  steps:\n    - { id: s, prompt: S, type: confirm }\n")},
		"builtin/a.yaml": &fstest.MapFile{Data: []byte("- id: a\n  label: A\n  steps:\n    - { id: s, prompt: S, type: confirm }\n")},
	}
	for range 5 {
		set, err := Load(LoadOptions{BuiltinFS: fsys, UserDir: t.TempDir()})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := strings.Join(set.IDs(), ","); got != "a,b" {
			t.Fatalf("IDs() = %q, want a,b (files are loaded in name order)", got)
		}
	}
}

func TestDefaultUserDir(t *testing.T) {
	t.Run("respects XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/config")
		got, err := DefaultUserDir()
		if err != nil {
			t.Fatalf("DefaultUserDir: %v", err)
		}
		if got != "/custom/config/ffwiz" {
			t.Errorf("DefaultUserDir() = %q", got)
		}
	})

	t.Run("falls back to ~/.config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/example")
		got, err := DefaultUserDir()
		if err != nil {
			t.Fatalf("DefaultUserDir: %v", err)
		}
		if got != "/home/example/.config/ffwiz" {
			t.Errorf("DefaultUserDir() = %q, want /home/example/.config/ffwiz", got)
		}
	})
}

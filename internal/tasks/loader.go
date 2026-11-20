package tasks

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

const builtinDir = "builtin"

// Set is a validated collection of tasks plus their provenance.
type Set struct {
	Tasks []Task
}

// Get returns the task with the given id.
func (s *Set) Get(id string) (Task, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// IDs returns the task ids in load order.
func (s *Set) IDs() []string {
	ids := make([]string, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		ids = append(ids, t.ID)
	}
	return ids
}

// LoadOptions controls where task definitions come from.
type LoadOptions struct {
	// UserDir overrides the directory that holds user YAML. Empty means the
	// default (~/.config/ffwiz, or $XDG_CONFIG_HOME/ffwiz); a directory that
	// does not exist is not an error.
	UserDir string
	// BuiltinFS overrides the embedded built-in definitions. Tests use this to
	// load fixture YAML.
	BuiltinFS fs.FS
}

// DefaultUserDir is where user overrides live.
//
// plan.md §2 specifies ~/.config/ffwiz. os.UserConfigDir would resolve to
// ~/Library/Application Support on macOS, which is not what the plan asks for,
// so XDG_CONFIG_HOME is honoured directly with ~/.config as the fallback.
func DefaultUserDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ffwiz"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return filepath.Join(home, ".config", "ffwiz"), nil
}

// Load reads the built-in tasks, merges any user overrides on top, validates the
// result and returns it.
func Load(opts LoadOptions) (*Set, error) {
	builtin := opts.BuiltinFS
	if builtin == nil {
		builtin = builtinFS
	}

	dir := opts.UserDir
	if dir == "" {
		var err error
		if dir, err = DefaultUserDir(); err != nil {
			return nil, err
		}
	}

	base, err := readDir(builtin, builtinDir)
	if err != nil {
		return nil, fmt.Errorf("reading built-in tasks: %w", err)
	}
	if len(base) == 0 {
		return nil, errors.New("no built-in tasks were embedded; the build is broken")
	}

	// A missing user directory is normal: most users never write one.
	if overrides, err := readDir(os.DirFS(dir), "."); err == nil {
		base = mergeAll(base, overrides)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading user tasks from %s: %w", dir, err)
	}

	set := &Set{}
	for _, doc := range base {
		var task Task
		if err := doc.Node.Decode(&task); err != nil {
			return nil, fmt.Errorf("%s: %w", doc.Source, err)
		}
		task.Source = doc.Source
		set.Tasks = append(set.Tasks, task)
	}

	if err := set.Validate(); err != nil {
		return nil, err
	}
	return set, nil
}

// doc is one parsed YAML document with the file it came from, kept as a node so
// user overrides can be merged field by field (DECISIONS D12).
type doc struct {
	Source string
	Node   yaml.Node
}

// readDir parses every *.yaml file in fsys/root as a list of tasks.
func readDir(fsys fs.FS, root string) ([]doc, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	// Sort so load order, and therefore error order, is deterministic.
	sort.Strings(names)

	var docs []doc
	for _, name := range names {
		path := name
		if root != "." {
			path = root + "/" + name
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, err
		}

		var file yaml.Node
		if err := yaml.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// An empty file decodes to a zero node; skip it rather than failing, so
		// a user can park a stub file.
		if len(file.Content) == 0 {
			continue
		}
		seq := file.Content[0]
		if seq.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s: expected a list of tasks at the top level", name)
		}
		for i := range seq.Content {
			docs = append(docs, doc{Source: name, Node: *seq.Content[i]})
		}
	}
	return docs, nil
}

func isYAML(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// mergeAll applies overrides on top of the built-in tasks, keyed by task id
// (DECISIONS D12).
func mergeAll(base, overrides []doc) []doc {
	out := make([]doc, len(base))
	copy(out, base)

	for _, ov := range overrides {
		id := scalarValue(mappingGet(&ov.Node, "id"))
		if id == "" {
			// Without an id there is nothing to merge against; keep it as-is so
			// validation reports the missing id.
			out = append(out, ov)
			continue
		}
		merged := false
		for i := range out {
			if scalarValue(mappingGet(&out[i].Node, "id")) != id {
				continue
			}
			mergeTaskNode(&out[i].Node, &ov.Node)
			// Report the user's file when a merged task fails validation: that
			// is the file the user can fix.
			out[i].Source = ov.Source + " (merged over " + out[i].Source + ")"
			merged = true
			break
		}
		if !merged {
			out = append(out, ov)
		}
	}
	return out
}

// mergeTaskNode patches base with override. Only the keys present in override
// are applied. "steps" is merged per step id rather than replaced, so a user can
// change one hint without forking the whole task.
func mergeTaskNode(base, override *yaml.Node) {
	if base.Kind != yaml.MappingNode || override.Kind != yaml.MappingNode {
		*base = *override
		return
	}
	for i := 0; i+1 < len(override.Content); i += 2 {
		key, value := override.Content[i], override.Content[i+1]
		switch key.Value {
		case "steps":
			mergeStepNodes(base, value)
		default:
			mappingSet(base, key.Value, value)
		}
	}
}

// mergeStepNodes patches base's steps with override's, matching on step id.
// Steps with an unknown id are appended. A step's own fields are patched
// individually; nested lists such as "choices" are replaced wholesale, because
// merging them by value would make it hard to remove an option.
func mergeStepNodes(base *yaml.Node, overrideSteps *yaml.Node) {
	baseSteps := mappingGet(base, "steps")
	if baseSteps == nil || baseSteps.Kind != yaml.SequenceNode {
		mappingSet(base, "steps", overrideSteps)
		return
	}
	if overrideSteps.Kind != yaml.SequenceNode {
		mappingSet(base, "steps", overrideSteps)
		return
	}

	for _, ovStep := range overrideSteps.Content {
		ovID := scalarValue(mappingGet(ovStep, "id"))
		var target *yaml.Node
		if ovID != "" {
			for _, bs := range baseSteps.Content {
				if scalarValue(mappingGet(bs, "id")) == ovID {
					target = bs
					break
				}
			}
		}
		if target == nil {
			baseSteps.Content = append(baseSteps.Content, ovStep)
			continue
		}
		for i := 0; i+1 < len(ovStep.Content); i += 2 {
			key, value := ovStep.Content[i], ovStep.Content[i+1]
			if key.Value == "id" {
				continue
			}
			mappingSet(target, key.Value, value)
		}
	}
}

// mappingGet returns the value node for key in a mapping node, or nil.
func mappingGet(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// mappingSet replaces the value for key, appending the pair when absent.
func mappingSet(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value)
}

// scalarValue returns a node's scalar text, or "" for anything else.
func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

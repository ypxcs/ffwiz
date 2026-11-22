package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/format"
	"github.com/ypxcs/ffwiz/internal/tasks"
)

// dumpFlags is the debug interface from plan.md §10 M2: it prints the command a
// task would produce, with no UI involved. It exists so the YAML engine can be
// driven and diffed from a shell (and from tests) before any screen exists, and
// it stays useful afterwards for bug reports.
type dumpFlags struct {
	TaskID    string
	Answers   map[string]string
	Input     string
	Output    string
	Overwrite bool
	// IgnoreCapabilities assumes every encoder, muxer and filter exists. The
	// default is to gate on what the local ffmpeg actually has, because that is
	// what the user will experience.
	IgnoreCapabilities bool
	// JSON prints the resolution as JSON, for tests and tooling.
	JSON bool
	// TempDir is where the files the command reads are written - today the
	// concatenation list. The caller owns its lifetime (DECISIONS D41).
	TempDir string
}

// parseAnswers splits repeated "--answers a=b,c=d" values into a map.
//
// A missing "=" is a typo that must not be silently ignored. Surrounding
// whitespace is trimmed from both sides, because "--answers ' a = b '" is what
// a shell-minded user types; a value that genuinely needs a leading space is not
// something this schema has, and a value containing a comma is expressed by
// repeating the flag with one pair per occurrence... which this does not support
// either, so the flag help says so.
func parseAnswers(values []string) (map[string]string, error) {
	answers := map[string]string{}
	for _, group := range values {
		for _, pair := range strings.Split(group, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				return nil, fmt.Errorf("invalid --answers entry %q: expected key=value", pair)
			}
			answers[key] = strings.TrimSpace(value)
		}
	}
	return answers, nil
}

// addAnswers merges --answer values, which are taken verbatim so a value may
// contain a comma (a time_range answer is "start,end").
func addAnswers(answers map[string]string, values []string) error {
	for _, pair := range values {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("invalid --answer entry %q: expected key=value", pair)
		}
		answers[key] = strings.TrimSpace(value)
	}
	return nil
}

// runDump loads the task set, resolves the task and prints the result.
func runDump(out io.Writer, tools ffmpeg.Tools, set *tasks.Set, flags dumpFlags) error {
	task, ok := set.Get(flags.TaskID)
	if !ok {
		return fmt.Errorf("unknown task %q (available: %s)", flags.TaskID, strings.Join(set.IDs(), ", "))
	}
	if flags.Input == "" {
		return errors.New("--dump needs --input: conditions and the output name depend on the file")
	}

	var (
		info *ffmpeg.MediaInfo
		caps *ffmpeg.Capabilities
	)
	if tools.FFprobe != "" {
		probed, err := ffmpeg.Probe(context.Background(), tools.FFprobe, flags.Input)
		if err != nil {
			return fmt.Errorf("probing %s: %w", flags.Input, err)
		}
		info = probed
	}
	if !flags.IgnoreCapabilities {
		detected, err := tools.Capabilities(context.Background())
		if err != nil {
			return fmt.Errorf("detecting ffmpeg capabilities: %w", err)
		}
		caps = &detected
	}

	engine := &tasks.Engine{Task: task, Info: info, Caps: caps, TempDir: flags.TempDir}

	if applies, reason := task.AppliesTo(info, caps); !applies {
		fmt.Fprintf(out, "warning: this task %s\n\n", reason)
	}

	cmd, res, err := engine.Build(flags.Answers, tasks.BuildOptions{
		Input:     flags.Input,
		Output:    flags.Output,
		Overwrite: flags.Overwrite,
	})
	if err != nil {
		return err
	}

	// A command may name a file ffwiz writes itself - the concatenation list.
	// The dump prints a command to be pasted into a shell, so the file has to
	// exist or the printed command cannot run (DECISIONS D41).
	if err := ffmpeg.WriteArtifacts(cmd); err != nil {
		return err
	}

	if flags.JSON {
		return writeDumpJSON(out, task, info, res, cmd)
	}
	writeDumpText(out, task, info, flags, engine, res, cmd)
	return nil
}

func writeDumpText(out io.Writer, task tasks.Task, info *ffmpeg.MediaInfo, flags dumpFlags, engine *tasks.Engine, res tasks.Resolution, cmd *ffmpeg.Command) {
	fmt.Fprintf(out, "task     %s — %s\n", task.ID, task.Label)
	fmt.Fprintf(out, "input    %s%s\n", flags.Input, describeInput(info))
	fmt.Fprintf(out, "output   %s\n", cmdOutput(cmd))

	fmt.Fprintf(out, "\nsteps\n")
	for _, step := range res.Active {
		answer := res.Answers[step.ID]
		_, given := flags.Answers[step.ID]
		source := "default"
		switch {
		case res.Pending[step.ID]:
			// No answer and no default: a question the user still has to answer
			// before there is a command (DECISIONS D23).
			source = "pending"
		case given:
			source = "answered"
		}
		fmt.Fprintf(out, "  %-14s %-8s %s=%q\n", step.ID, source, step.Type, answer)
	}

	if len(res.Skipped) > 0 {
		ids := make([]string, 0, len(res.Skipped))
		for id := range res.Skipped {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Fprintf(out, "\nskipped\n")
		for _, id := range ids {
			fmt.Fprintf(out, "  %-14s %s\n", id, res.Skipped[id])
		}
	}

	if len(res.HiddenChoices) > 0 {
		ids := make([]string, 0, len(res.HiddenChoices))
		for id := range res.HiddenChoices {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Fprintf(out, "\nhidden options\n")
		for _, id := range ids {
			for _, c := range res.HiddenChoices[id] {
				reason := engine.Unavailable(c)
				if reason == "" {
					reason = "does not apply to the answers so far"
				}
				fmt.Fprintf(out, "  %-14s %-22s %s\n", id, c.Value, reason)
			}
		}
	}

	// A step that was never answered contributes nothing to the command, so the
	// dump must say so rather than print a command that quietly left the answer
	// out. The wizard itself will not run such a command (DECISIONS D29).
	if pending := pendingSteps(res); len(pending) > 0 {
		fmt.Fprintf(out, "\nwarning: unanswered: %s — the command below leaves %s out\n",
			strings.Join(pending, ", "), plural(len(pending), "it", "them"))
	}

	fmt.Fprintf(out, "\ncommand\n%s\n", cmd.String())

	// A file ffwiz writes for the command is invisible in the command itself,
	// which just names it; saying where it came from is the difference between
	// a pasteable command and a mystery path.
	if arts := cmd.Artifacts(); len(arts) > 0 {
		fmt.Fprintf(out, "\nfiles written for this command\n")
		for _, a := range arts {
			fmt.Fprintf(out, "  %s\n", a.Path)
		}
	}
}

// pendingSteps lists the active steps with no answer, in step order.
func pendingSteps(res tasks.Resolution) []string {
	var ids []string
	for _, step := range res.Active {
		if res.Pending[step.ID] {
			ids = append(ids, step.ID)
		}
	}
	return ids
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// dumpJSON is the machine-readable form, for tests that assert on structure
// rather than on a formatted screen.
type dumpJSON struct {
	TaskID   string            `json:"task_id"`
	Active   []string          `json:"active_steps"`
	Answers  map[string]string `json:"answers"`
	Skipped  map[string]string `json:"skipped"`
	Pending  []string          `json:"pending"`
	Command  []string          `json:"command"`
	Display  string            `json:"display"`
	Expected string            `json:"expected_duration"`
}

func writeDumpJSON(out io.Writer, task tasks.Task, info *ffmpeg.MediaInfo, res tasks.Resolution, cmd *ffmpeg.Command) error {
	payload := dumpJSON{
		TaskID:   task.ID,
		Answers:  res.Answers,
		Skipped:  res.Skipped,
		Pending:  pendingSteps(res),
		Command:  cmd.Args(),
		Display:  cmd.String(),
		Expected: cmd.ExpectedDuration().String(),
	}
	for _, step := range res.Active {
		payload.Active = append(payload.Active, step.ID)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// describeInput is a short one-line summary of the probed file.
func describeInput(info *ffmpeg.MediaInfo) string {
	if info == nil {
		return ""
	}
	var parts []string
	if d := info.Duration(); d > 0 {
		parts = append(parts, format.Duration(d))
	}
	if v, ok := info.FirstStream(ffmpeg.StreamVideo); ok {
		parts = append(parts, fmt.Sprintf("video %s %dx%d", v.CodecName, v.Width, v.Height))
	}
	if a, ok := info.FirstStream(ffmpeg.StreamAudio); ok {
		parts = append(parts, fmt.Sprintf("audio %s", a.CodecName))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// runList prints the task catalogue, with each task marked when it does not
// apply to the probed input or to this ffmpeg build.
func runList(out io.Writer, set *tasks.Set, info *ffmpeg.MediaInfo, caps *ffmpeg.Capabilities) {
	for _, task := range set.Tasks {
		marker := ""
		if applies, reason := task.AppliesTo(info, caps); !applies {
			marker = "  [" + reason + "]"
		}
		fmt.Fprintf(out, "%-18s %s%s\n", task.ID, task.Label, marker)
	}
}

func cmdOutput(cmd *ffmpeg.Command) string {
	args := cmd.DisplayArgs()
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

package ffmpeg

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// InputSpec is one input file plus the flags that must appear before -i.
//
// Flags for input-side options (notably -ss for a fast seek, and -t) belong
// here, not in Request.OutputArgs: ffmpeg applies input options to the input
// that follows them, and putting a seek after -i forces a slow decode from the
// start of the file. See Request's field docs and plan.md §7.
type InputSpec struct {
	Path string
	// Args are emitted immediately before "-i <path>", in the order given.
	Args []string
}

// Artifact is a file the command reads from disk but ffwiz writes itself -
// today the concatenation list (plan.md §5.9).
//
// The engine produces an artifact as data rather than writing it, because
// building a command has to stay free of side effects: the preview rebuilds the
// command on every keystroke. The files are created by Start, immediately before
// ffmpeg runs, so a list cannot be missing when the command that names it needs
// it.
type Artifact struct {
	// Path is where the file has to exist for the command to run. It is the
	// path that appears in the argument list.
	Path string
	// Content is what to write there.
	Content string
}

// Request is a fully resolved description of one ffmpeg invocation.
//
// It is produced by the task engine (internal/tasks) and consumed by Build.
// Keeping it as data - rather than as a partially built argument list - means
// the ordering rules live in exactly one place.
type Request struct {
	// Inputs is usually one file; the concatenate task passes several.
	Inputs []InputSpec
	// Output is the destination path.
	Output string

	// OutputArgs are output-side options, emitted after filters and maps and
	// before the output path. Trim durations (-t/-to) belong here. Order is
	// preserved exactly.
	OutputArgs []string

	// VideoFilters and AudioFilters are filter chains joined into a single -vf
	// and a single -af. Duplicates are dropped; order is preserved (§4.6).
	VideoFilters []string
	AudioFilters []string

	// FilterComplex holds complete filtergraph strings, joined with ";".
	// When it is non-empty, Maps must be non-empty too: without an explicit
	// -map, ffmpeg fails with "Output file does not contain any stream".
	FilterComplex []string

	// Maps are stream specifiers emitted as "-map <value>" each, e.g. "[out]"
	// or "0:a:0".
	Maps []string

	// Format, when set, adds "-f <format>".
	Format string

	// Artifacts are files ffwiz writes for the command to read, keyed by the
	// path the arguments already refer to. The concatenation list is one.
	Artifacts []Artifact

	// Overwrite selects "-y" instead of the default "-n". ffwiz never sets
	// this without an explicit user confirmation (plan.md §8).
	Overwrite bool

	// ExpectedDuration is how long the *output* is expected to be, used for
	// the progress bar. It is not always the input duration: trims shorten it
	// and speed changes scale it. Zero means unknown.
	ExpectedDuration time.Duration
}

// ErrNoInputs is returned by Build when a request has nothing to read.
var ErrNoInputs = errors.New("no input files")

// ErrSameFile is returned when the output path would clobber an input.
var ErrSameFile = errors.New("output path is the same as an input file")

// Command is a validated, ordered ffmpeg invocation.
type Command struct {
	req Request
}

// execOnlyArgs are added to the executed command but hidden from the displayed
// and copied command, so the preview stays readable (plan.md §7):
//
//   - -hide_banner    keeps the build/configuration noise out of the log pane
//   - -nostdin        stops ffmpeg reading the terminal. The TUI owns stdin and
//     ffmpeg would otherwise treat keystrokes as interactive
//     commands, which can hang or swallow input. (DECISIONS D5)
//   - -progress pipe:1 -nostats
//     machine-readable progress on stdout, no human
//     stats line-drawing on stderr
var execOnlyArgs = []string{"-hide_banner", "-nostdin", "-progress", "pipe:1", "-nostats"}

// Build validates req and produces a Command.
func Build(req Request) (*Command, error) {
	if len(req.Inputs) == 0 {
		return nil, ErrNoInputs
	}
	if strings.TrimSpace(req.Output) == "" {
		return nil, errors.New("no output path")
	}
	if len(req.FilterComplex) > 0 && len(req.Maps) == 0 {
		// See Request.FilterComplex and DECISIONS D2.
		return nil, errors.New("filter_complex requires at least one map")
	}
	for _, in := range req.Inputs {
		same, err := samePath(in.Path, req.Output)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, fmt.Errorf("%w: %s", ErrSameFile, req.Output)
		}
	}
	return &Command{req: req}, nil
}

// Args returns the argument list to execute. It never includes the program
// name and is never passed through a shell (plan.md §8).
func (c *Command) Args() []string {
	args := make([]string, 0, 32)
	args = append(args, execOnlyArgs...)
	args = append(args, c.commonArgs()...)
	return args
}

// DisplayArgs returns the arguments as the user should see them: the same order
// as Args, without the execution-only flags, and with the program name omitted.
func (c *Command) DisplayArgs() []string {
	return c.commonArgs()
}

// String renders the shell-quoted command for display, copying and script
// export. The result is safe to paste into a POSIX shell.
func (c *Command) String() string {
	parts := append([]string{"ffmpeg"}, c.DisplayArgs()...)
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = Quote(p)
	}
	return strings.Join(quoted, " ")
}

// Script renders the command as a runnable shell script (plan.md §6.6).
func (c *Command) Script() string {
	return "#!/bin/sh\nset -e\n\n" + c.String() + "\n"
}

// ExpectedDuration reports how long the output is expected to be, or 0 when
// unknown (plan.md §7, DECISIONS D3).
func (c *Command) ExpectedDuration() time.Duration {
	return c.req.ExpectedDuration
}

// Overwrites reports whether the command may clobber an existing file.
func (c *Command) Overwrites() bool { return c.req.Overwrite }

// Artifacts are the files ffwiz must write before this command can run.
func (c *Command) Artifacts() []Artifact { return c.req.Artifacts }

// commonArgs builds the argument order shared by both the exec and display
// forms. The ordering rules here are the reason this type exists:
//
//	[overwrite policy] ... inputs (input opts, -i, path) ...
//	-filter_complex, -vf, -af, -map ... output opts ... output path
//
// Input options must precede their -i. Filters and maps must follow the inputs.
// The output path must be last.
func (c *Command) commonArgs() []string {
	var args []string

	// ffwiz never overwrites silently: -n is the default, -y only after an
	// explicit confirmation (plan.md §8).
	if c.req.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}

	for _, in := range c.req.Inputs {
		args = append(args, in.Args...)
		args = append(args, "-i", guardPath(in.Path))
	}

	if graph := joinGraphs(c.req.FilterComplex); graph != "" {
		args = append(args, "-filter_complex", graph)
	}
	if vf := mergeFilters(c.req.VideoFilters); vf != "" {
		args = append(args, "-vf", vf)
	}
	if af := mergeFilters(c.req.AudioFilters); af != "" {
		args = append(args, "-af", af)
	}
	for _, m := range c.req.Maps {
		args = append(args, "-map", m)
	}
	if c.req.Format != "" {
		args = append(args, "-f", c.req.Format)
	}

	args = append(args, c.req.OutputArgs...)
	args = append(args, guardPath(c.req.Output))
	return args
}

// mergeFilters joins filter chains into a single -vf or -af value.
//
// plan.md §4.6 requires exactly one -vf and one -af no matter how many steps
// contribute filters, so the chains are comma-joined here rather than emitted
// as repeated flags. Order is the caller's, which is what makes the merge
// deterministic.
//
// Repeats are kept, not deduplicated: an atempo chain is necessarily the same
// filter written twice (0.25x is atempo=0.5,atempo=0.5), so dropping "exact
// duplicates" would silently produce a clip at the wrong speed.
func mergeFilters(chain []string) string {
	parts := make([]string, 0, len(chain))
	for _, f := range chain {
		if f = strings.TrimSpace(f); f != "" {
			parts = append(parts, f)
		}
	}
	return strings.Join(parts, ",")
}

// joinGraphs joins complete filtergraphs with ";", which is how ffmpeg chains
// independent graphs in a single -filter_complex.
func joinGraphs(graphs []string) string {
	parts := make([]string, 0, len(graphs))
	for _, g := range graphs {
		if g = strings.TrimSpace(g); g != "" {
			parts = append(parts, g)
		}
	}
	return strings.Join(parts, ";")
}

// shellSafe matches the characters that never need quoting in a POSIX shell.
// Non-ASCII bytes are deliberately excluded: quoting them protects the filename
// from being mangled by a shell running in a different locale.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Quote quotes a single argument so a rendered command can be pasted into a
// shell. Values that are already unambiguous are left alone to keep the preview
// readable.
//
// It is exported because the wizard renders the command token by token, and a
// pane that quoted differently from String would show one command while the
// clipboard, the script export and the Run action produced another. The preview
// is the contract (DECISIONS D27), so there is one quoting rule, here.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// guardPath defuses paths that ffmpeg would mistake for an option.
//
// A filename beginning with "-" is parsed as a flag no matter how it is quoted,
// because the shell strips the quotes before ffmpeg sees it. Prefixing "./"
// keeps the same file while making it unambiguous (plan.md §8). Absolute paths
// cannot start with "-" so they are returned unchanged.
func guardPath(p string) string {
	if strings.HasPrefix(p, "-") && !filepath.IsAbs(p) {
		return "./" + p
	}
	return p
}

// samePath reports whether two paths refer to the same file.
//
// Comparison is on cleaned absolute paths; this is a best-effort check against
// the common accident of writing the output over the input, not a guarantee
// against symlinks or hard links (the -n default is the real guard).
func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", a, err)
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", b, err)
	}
	return absA == absB, nil
}

// Command ffwiz is a TUI wizard that helps people with little or no ffmpeg
// knowledge build a correct ffmpeg command.
//
// This file is startup only: flags, the checks that must happen before a
// terminal is taken over, and the two headless debug commands from plan.md §10.
// Everything interactive lives in internal/wizard.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
	"github.com/ypxcs/ffwiz/internal/wizard"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ffwiz: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		ffmpegFlag  = flag.String("ffmpeg", "", "path to the ffmpeg binary (default: $FFWIZ_FFMPEG, then PATH)")
		ffprobeFlag = flag.String("ffprobe", "", "path to the ffprobe binary (default: $FFWIZ_FFPROBE, then PATH)")
		configDir   = flag.String("config-dir", "", "directory holding user task overrides (default: ~/.config/ffwiz)")
		showVersion = flag.Bool("version", false, "print the ffmpeg version ffwiz will use, then exit")
		debug       = flag.Bool("debug", false, "write a debug log to the user cache directory")

		dumpTask  = flag.String("dump", "", "print the ffmpeg command a task produces, without starting the UI")
		listTasks = flag.Bool("list", false, "list the available tasks, then exit")
		inputFlag = flag.String("input", "", "input file; skips the file picker")
		outputArg = flag.String("output", "", "output file (with --dump; default: derived from the input)")
		overwrite = flag.Bool("overwrite", false, "use -y instead of -n in the dumped command")
		noCaps    = flag.Bool("ignore-capabilities", false, "do not hide options this ffmpeg build cannot produce")
		jsonOut   = flag.Bool("json", false, "print the dump as JSON")
	)
	var answerFlags []string
	flag.Func("answers", "comma-separated step=value answers, repeatable; values may not contain commas (with --dump)", func(v string) error {
		answerFlags = append(answerFlags, v)
		return nil
	})
	// A time_range answer is "start,end", which the comma-separated flag above
	// cannot express. --answer takes one pair verbatim so every answer a step
	// can hold is reachable from the shell.
	var answerVerbatim []string
	flag.Func("answer", "one step=value answer, taken verbatim, repeatable (with --dump)", func(v string) error {
		answerVerbatim = append(answerVerbatim, v)
		return nil
	})
	flag.Parse()

	// The TUI owns stdout and stderr, so a log file is the only way to see what
	// startup did (DECISIONS D11).
	logger, closeLog, logErr := openDebugLog(*debug)
	if closeLog != nil {
		defer closeLog.Close()
	}
	if *debug && logErr != nil {
		fmt.Fprintf(os.Stderr, "ffwiz: cannot write the debug log: %v\n", logErr)
	}
	logf := func(format string, args ...any) {
		if logger != nil {
			logger.Printf(format, args...)
		}
	}
	logf("ffwiz starting: args=%q", os.Args)

	ctx := context.Background()
	tools, err := ffmpeg.Locate(ctx, ffmpeg.Overrides{
		FFmpeg:  *ffmpegFlag,
		FFprobe: *ffprobeFlag,
	})
	if err != nil {
		// Startup checks are the one place where a plain stderr message beats a
		// TUI: there is nothing to interact with until ffmpeg exists
		// (plan.md §6.1).
		return fmt.Errorf("%w\n\n%s", err, ffmpeg.InstallHint())
	}
	logf("ffmpeg %q at %s; ffprobe at %s", tools.Version, tools.FFmpeg, tools.FFprobe)

	if *showVersion {
		version := tools.Version
		if version == "" {
			version = "(unknown)"
		}
		fmt.Printf("ffmpeg %s\n  %s\n  %s\n", version, tools.FFmpeg, tools.FFprobe)
		return nil
	}

	// Loading validates every built-in definition, so a malformed YAML file
	// fails here with the file and task rather than at some later screen
	// (plan.md §4.7).
	set, err := tasks.Load(tasks.LoadOptions{UserDir: *configDir})
	if err != nil {
		return err
	}
	logf("loaded %d tasks: %v", len(set.Tasks), set.IDs())

	switch {
	case *listTasks:
		runList(os.Stdout, set, nil)
		return nil
	case *dumpTask != "":
		answers, err := parseAnswers(answerFlags)
		if err != nil {
			return err
		}
		if err := addAnswers(answers, answerVerbatim); err != nil {
			return err
		}
		return runDump(os.Stdout, tools, set, dumpFlags{
			TaskID:             *dumpTask,
			Answers:            answers,
			Input:              *inputFlag,
			Output:             *outputArg,
			Overwrite:          *overwrite,
			IgnoreCapabilities: *noCaps,
			JSON:               *jsonOut,
		})
	}

	// Capability detection happens before the alternate screen opens, so the
	// first frame already hides what this build cannot produce instead of
	// offering options that flicker away (DECISIONS D23).
	caps, err := tools.Capabilities(ctx)
	if err != nil {
		logf("capability detection failed (%v); offering every option", err)
		caps = ffmpeg.Capabilities{}
	}
	logf("capabilities: %d encoders, %d muxers, %d filters",
		len(caps.Encoders), len(caps.Muxers), len(caps.Filters))

	input := *inputFlag
	if input == "" {
		input = flag.Arg(0)
	}

	startDir, err := os.Getwd()
	if err != nil {
		// A working directory that cannot be read is not fatal: the picker
		// falls back to the home directory rather than refusing to start.
		logf("cannot read the working directory: %v", err)
		startDir, _ = os.UserHomeDir()
	}

	model := wizard.New(wizard.Deps{
		Set:          set,
		Caps:         &caps,
		Tools:        tools,
		Input:        input,
		StartDir:     startDir,
		Clipboard:    wizard.NewSystemClipboard(),
		ProbeTimeout: 30 * time.Second,
	})
	program := tea.NewProgram(model)
	final, err := program.Run()
	if err != nil {
		logf("the wizard exited with an error: %v", err)
		return err
	}
	_ = final
	logf("the wizard exited")
	return nil
}

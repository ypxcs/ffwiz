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
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
	"github.com/ypxcs/ffwiz/internal/tempdir"
	"github.com/ypxcs/ffwiz/internal/wizard"
)

// version is the release this binary came from. GoReleaser sets it at build time
// (see .goreleaser.yaml); anything built from a checkout - `make build`, go
// install - says "dev", which is the honest answer for a tree that may be dirty.
var version = "dev"

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
		showVersion = flag.Bool("version", false, "print the ffwiz and ffmpeg versions, then exit")
		debug       = flag.Bool("debug", false, "write a debug log to the user cache directory")
		themeFlag   = flag.String("theme", "auto", "colour scheme: auto (follow the terminal), light or dark")

		dumpTask  = flag.String("dump", "", "print the ffmpeg command a task produces, without starting the UI")
		listTasks = flag.Bool("list", false, "list the available tasks, then exit")
		inputFlag = flag.String("input", "", "input file; skips the file picker")
		outputArg = flag.String("output", "", "output file (with --dump; default: derived from the input)")
		overwrite = flag.Bool("overwrite", false, "use -y instead of -n in the dumped command")
		noCaps    = flag.Bool("ignore-capabilities", false, "do not hide options this ffmpeg build cannot produce")
		jsonOut   = flag.Bool("json", false, "print the dump as JSON")
		tempDir   = flag.String("temp-dir", "", "where --dump writes the files the command reads (default: a fresh temporary directory)")
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
		defer func() { _ = closeLog.Close() }()
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

	// --theme is rejected before the terminal is taken over, so a typo is a
	// message rather than a session in the wrong colours (DECISIONS D46).
	theme, err := wizard.ParseTheme(*themeFlag)
	if err != nil {
		logf("rejecting --theme: %v", err)
		return err
	}
	logf("theme: %s", theme)

	// A signal has to clean up the temporary files even though nothing can run a
	// deferred call: the process is going away (plan.md §8, DECISIONS D10). In
	// the TUI ctrl+c is a key rather than a signal, so this covers SIGTERM and
	// the window before the terminal is in raw mode.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		ffmpegVersion := tools.Version
		if ffmpegVersion == "" {
			ffmpegVersion = "(unknown)"
		}
		fmt.Printf("ffwiz %s\nffmpeg %s\n  %s\n  %s\n", version, ffmpegVersion, tools.FFmpeg, tools.FFprobe)
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
		// The catalogue is marked up with what this build cannot run, so the
		// listing needs the same capability detection the UI does. A failure to
		// detect is not fatal here: the listing then claims nothing is missing.
		caps, err := tools.Capabilities(ctx)
		if err != nil {
			logf("capability detection failed (%v); listing every task as runnable", err)
			caps = ffmpeg.Capabilities{}
		}
		return runList(os.Stdout, set, nil, &caps)
	case *dumpTask != "":
		answers, err := parseAnswers(answerFlags)
		if err != nil {
			return err
		}
		if err := addAnswers(answers, answerVerbatim); err != nil {
			return err
		}
		// A dumped command may name a file ffwiz writes for it - today the
		// concat list. It is written into a fresh directory that is *not*
		// removed: the printed command has to stay runnable, and a one-shot CLI
		// has no exit to clean up at (DECISIONS D41).
		dumpTemp := *tempDir
		if dumpTemp == "" {
			if reg, err := tempdir.New("ffwiz-*"); err == nil {
				dumpTemp = reg.Dir()
			} else {
				logf("cannot make a temporary directory (%v); using the system one", err)
			}
		}
		return runDump(os.Stdout, tools, set, dumpFlags{
			TaskID:             *dumpTask,
			Answers:            answers,
			Input:              *inputFlag,
			Output:             *outputArg,
			Overwrite:          *overwrite,
			IgnoreCapabilities: *noCaps,
			JSON:               *jsonOut,
			TempDir:            dumpTemp,
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

	// The session's generated files - the concat list - live in one directory
	// that is removed however the session ends: a normal exit, a failure, or a
	// signal (plan.md §8, DECISIONS D10).
	tmp, err := tempdir.New("ffwiz-*")
	if err != nil {
		logf("cannot make a temporary directory (%v); generated files go to the system one", err)
	} else {
		logf("temporary directory: %s", tmp.Dir())
	}
	defer func() {
		if err := tmp.Flush(); err != nil {
			logf("could not remove %s: %v", tmp.Dir(), err)
		}
	}()
	tmp.Watch(ctx)

	model := wizard.New(wizard.Deps{
		Set:          set,
		Caps:         &caps,
		Tools:        tools,
		Input:        input,
		StartDir:     startDir,
		Clipboard:    wizard.NewSystemClipboard(),
		TempDir:      tmp.Dir(),
		Theme:        theme,
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

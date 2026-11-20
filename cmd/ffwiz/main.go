// Command ffwiz is a TUI wizard that helps people with little or no ffmpeg
// knowledge build a correct ffmpeg command.
//
// M2 SCOPE: the flag parsing, startup checks and the debug command from
// plan.md §10. The wizard screens arrive in M3, at which point the smoke screen
// below is deleted and this file keeps only startup.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
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
		inputFlag = flag.String("input", "", "input file (with --dump)")
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
	flag.Parse()

	// The TUI owns stdout and stderr, so a debug log file is the only way to
	// see what startup did (DECISIONS D11).
	if *debug {
		// The log itself lands in M3 alongside the wizard; for now report where
		// it would go so the flag is not a silent no-op.
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		fmt.Fprintf(os.Stderr, "ffwiz: debug logging to %s\n", dir)
	}

	tools, err := ffmpeg.Locate(context.Background(), ffmpeg.Overrides{
		FFmpeg:  *ffmpegFlag,
		FFprobe: *ffprobeFlag,
	})
	if err != nil {
		// Startup checks are the one place where a plain stderr message beats a
		// TUI: there is nothing to interact with until ffmpeg exists
		// (plan.md §6.1).
		return fmt.Errorf("%w\n\n%s", err, ffmpeg.InstallHint())
	}

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

	switch {
	case *listTasks:
		runList(os.Stdout, set, nil)
		return nil
	case *dumpTask != "":
		answers, err := parseAnswers(answerFlags)
		if err != nil {
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

	model := newSmokeModel(tools, flag.Arg(0))
	if _, err := tea.NewProgram(model).Run(); err != nil {
		return err
	}
	return nil
}

// smokeModel is a throwaway screen from M1. It is replaced by the wizard in M3.
type smokeModel struct {
	tools  ffmpeg.Tools
	input  string
	info   *ffmpeg.MediaInfo
	err    error
	width  int
	height int
}

// probeDoneMsg carries the result of an off-thread ffprobe run back to Update.
type probeDoneMsg struct {
	info *ffmpeg.MediaInfo
	err  error
}

func newSmokeModel(tools ffmpeg.Tools, input string) smokeModel {
	return smokeModel{tools: tools, input: input}
}

// Init starts any required I/O. Probing happens here rather than in Update so
// the UI never blocks (plan.md §3, rule 4).
func (m smokeModel) Init() tea.Cmd {
	if m.input == "" {
		return nil
	}
	// A closure returning a Msg is the idiomatic tea.Cmd.
	return func() tea.Msg {
		info, err := ffmpeg.Probe(context.Background(), m.tools.FFprobe, m.input)
		return probeDoneMsg{info: info, err: err}
	}
}

func (m smokeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case probeDoneMsg:
		m.info, m.err = msg.info, msg.err
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m smokeModel) View() tea.View {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Faint(true)

	b.WriteString(title.Render("ffwiz") + dim.Render(" — M1 smoke screen") + "\n\n")
	fmt.Fprintf(&b, "  ffmpeg   %s\n", orUnknown(m.tools.Version))
	fmt.Fprintf(&b, "  binary   %s\n", m.tools.FFmpeg)
	fmt.Fprintf(&b, "  ffprobe  %s\n\n", m.tools.FFprobe)

	switch {
	case m.input == "":
		fmt.Fprintf(&b, "  %s\n", dim.Render("Pass a media file to see its details:  ffwiz some-video.mp4"))
	case m.err != nil:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("probe failed") + "\n")
		fmt.Fprintf(&b, "  %v\n", m.err)
	case m.info == nil:
		// View runs before the probe command returns, so the model must have a
		// defined loading state. Rendering the result unconditionally here was a
		// nil dereference in the M1 build.
		b.WriteString("  " + dim.Render("probing "+m.input+" …") + "\n")
	default:
		b.WriteString(summarize(m.info))
	}

	b.WriteString("\n" + dim.Render("  q quit"))
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// summarize renders the probe result as plain lines. A nil or empty result
// renders nothing rather than panicking, so a partial probe cannot crash View.
func summarize(info *ffmpeg.MediaInfo) string {
	if info == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  container  %s\n", info.Format.Name)
	fmt.Fprintf(&b, "  duration   %s\n", roundDuration(info.Duration()))
	fmt.Fprintf(&b, "  size       %s\n", humanBytes(info.Format.Size))
	if info.Format.BitRate > 0 {
		fmt.Fprintf(&b, "  bitrate    %s\n", humanBitRate(info.Format.BitRate))
	}
	b.WriteString("\n")
	for _, s := range info.Streams {
		switch s.Type {
		case ffmpeg.StreamVideo:
			fmt.Fprintf(&b, "  video #%d   %s %dx%d %s %.3g fps\n",
				s.Index, s.CodecName, s.Width, s.Height, s.PixelFormat, s.FrameRate)
		case ffmpeg.StreamAudio:
			fmt.Fprintf(&b, "  audio #%d   %s %s %d Hz\n",
				s.Index, s.CodecName, or(s.ChannelLayout, fmt.Sprintf("%dch", s.Channels)), s.SampleRate)
		case ffmpeg.StreamSubtitle:
			fmt.Fprintf(&b, "  subs  #%d   %s %s\n", s.Index, s.CodecName, s.Language)
		default:
			fmt.Fprintf(&b, "  other #%d   %s\n", s.Index, s.CodecName)
		}
	}
	return b.String()
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// roundDuration formats a media duration for display, truncating below the
// millisecond because anything finer is noise for media and truncating is
// honest (a 1.9999s clip must not read as 2s).
func roundDuration(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	rounded := d.Truncate(time.Millisecond)
	if rounded == 0 {
		return "<1ms"
	}
	return rounded.String()
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMG"[exp])
}

func humanBitRate(bps int64) string {
	switch {
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mbit/s", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.0f kbit/s", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d bit/s", bps)
	}
}

package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
)

// Screen identifies which pane the wizard is showing (plan.md §6).
type Screen int

const (
	// ScreenInput picks a file and shows what was found in it.
	ScreenInput Screen = iota
	// ScreenTask picks a job from the catalogue.
	ScreenTask
	// ScreenStep asks the current step's question.
	ScreenStep
	// ScreenOutput chooses where to write.
	ScreenOutput
	// ScreenReview shows the finished command.
	ScreenReview
)

// Deps is everything the wizard needs from outside. Nothing here reaches out to
// the filesystem or the network on its own, which is what lets the model tests
// run a whole session without ffmpeg or a terminal (plan.md §3, rule 4).
type Deps struct {
	// Set is the validated task catalogue.
	Set *tasks.Set
	// Caps is the local ffmpeg build's feature set. Nil means "assume
	// everything is available", which hides nothing.
	Caps *ffmpeg.Capabilities
	// Tools locates the ffmpeg and ffprobe binaries used for probing.
	Tools ffmpeg.Tools
	// Input, when set, skips the file picker (plan.md §6.2).
	Input string
	// StartDir is the directory the file picker opens in. Empty means the
	// working directory.
	StartDir string
	// Clipboard writes the command on the review screen. Nil means this machine
	// has none, and copying explains itself instead of failing silently
	// (DECISIONS D16).
	Clipboard Clipboard
	// IsDark selects the colour scheme until the terminal says otherwise.
	IsDark bool
	// ProbeTimeout bounds the ffprobe run. Zero uses the package default.
	ProbeTimeout time.Duration
}

// Model is the root Bubble Tea model.
type Model struct {
	styles Styles
	keys   bindings
	help   help.Model

	set     *tasks.Set
	caps    *ffmpeg.Capabilities
	tools   ffmpeg.Tools
	clip    Clipboard
	timeout time.Duration

	width, height int
	screen        Screen

	// picker and probing drive the input screen.
	picker   filepicker.Model
	filtered bool
	probing  bool
	// chosen is true once a file has been read and its summary is on screen.
	chosen bool

	// notice is a one-line message for the current screen. It is either a
	// warning (something the user should know) or an error (something that
	// stopped them).
	notice        string
	noticeIsError bool

	input string
	info  *ffmpeg.MediaInfo

	taskIndex int

	// engine, answers and stepIndex drive the step screens. answers holds only
	// what the user actually chose; defaults are applied by the engine on every
	// resolve, so a changed answer can never leave a stale one behind.
	engine    *tasks.Engine
	answers   map[string]string
	stepIndex int
	choiceIdx int
	stepInput textinput.Model

	outputPath  string
	outputInput textinput.Model

	copied bool
}

// New builds the wizard in its starting state.
func New(deps Deps) Model {
	// A nil clipboard becomes one that refuses every write, so the copy action
	// has a single code path rather than a nil check in the middle of it.
	clip := deps.Clipboard
	if clip == nil {
		clip = UnavailableClipboard{}
	}

	m := Model{
		styles:  NewStyles(deps.IsDark),
		keys:    newBindings(),
		help:    help.New(),
		set:     deps.Set,
		caps:    deps.Caps,
		tools:   deps.Tools,
		clip:    clip,
		timeout: deps.ProbeTimeout,
		answers: map[string]string{},
		width:   80,
		height:  24,
		screen:  ScreenInput,
		input:   deps.Input,
	}
	m.help.Styles = help.DefaultStyles(deps.IsDark)

	m.picker = filepicker.New()
	m.picker.FileAllowed = true
	m.picker.DirAllowed = false
	m.picker.AutoHeight = false
	m.picker.ShowPermissions = false
	m.picker.ShowSize = true
	m.picker.ShowHidden = false
	start := deps.StartDir
	if start == "" {
		start, _ = os.Getwd()
	}
	if deps.Input != "" {
		if abs, err := filepath.Abs(deps.Input); err == nil {
			start = filepath.Dir(abs)
			m.picker.Path = abs
		}
	}
	m.picker.CurrentDirectory = start
	m.setFilter(true)

	m.stepInput = textinput.New()
	m.outputInput = textinput.New()
	m.applyStyles()

	if deps.Input != "" {
		m.probing = true
	}
	return m
}

// setFilter switches the file picker between media files and everything.
func (m *Model) setFilter(mediaOnly bool) {
	m.filtered = mediaOnly
	if mediaOnly {
		m.picker.AllowedTypes = mediaExtensions
	} else {
		m.picker.AllowedTypes = nil
	}
}

// mediaExtensions is the file picker's default filter. It is a filter, not a
// guarantee: ffmpeg reads far more than this, and `a` shows everything.
var mediaExtensions = []string{
	".mp4", ".m4v", ".mkv", ".mov", ".avi", ".webm", ".flv", ".wmv", ".mpg", ".mpeg", ".ts", ".m2ts",
	".mp3", ".m4a", ".aac", ".flac", ".wav", ".ogg", ".oga", ".opus", ".wma", ".aiff",
	".gif", ".apng", ".webp",
	".srt", ".ass", ".ssa", ".vtt", ".sub",
	".jpg", ".jpeg", ".png", ".bmp", ".tiff",
}

// applyStyles pushes the current style set into the sub-components.
func (m *Model) applyStyles() {
	// The picker's defaults are fine; only the cursor needs to match the theme.
	m.picker.Cursor = m.styles.Key.Render(">")
	m.picker.Styles.Cursor = m.styles.Key
	m.picker.Styles.Selected = m.styles.Key
	m.picker.Styles.Directory = m.styles.Muted
	m.picker.Styles.DisabledFile = m.styles.ChoiceDisabled
	m.picker.Styles.Symlink = m.styles.Warning
	m.picker.Styles.File = lipgloss.NewStyle()
	m.picker.Styles.Permission = m.styles.Muted
	m.picker.Styles.FileSize = m.styles.Muted
	m.picker.Styles.EmptyDirectory = m.styles.Muted

	isDark := m.styles.ChoiceDisabled.GetForeground() != lipgloss.Color("242")
	m.stepInput.SetStyles(textinput.DefaultStyles(isDark))
	m.outputInput.SetStyles(textinput.DefaultStyles(isDark))
}

// Init starts the wizard: it asks the terminal about its background and either
// probes the file given on the command line or reads the directory.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.input != "" {
		cmds = append(cmds, m.probeCmd(m.input))
	} else {
		cmds = append(cmds, m.picker.Init())
	}
	return tea.Batch(cmds...)
}

// probeMsg carries the outcome of an ffprobe run.
type probeMsg struct {
	path string
	info *ffmpeg.MediaInfo
	err  error
}

// probeCmd runs ffprobe off the event loop (plan.md §3, rule 4).
func (m Model) probeCmd(path string) tea.Cmd {
	ffprobe := m.tools.FFprobe
	timeout := m.timeout
	return func() tea.Msg {
		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		info, err := ffmpeg.ProbeWithTimeout(ctx, ffprobe, path, timeout)
		return probeMsg{path: path, info: info, err: err}
	}
}

// copyMsg reports the outcome of a clipboard write.
type copyMsg struct{ err error }

// Update routes a message to the current screen.
//
// The model is copied on every Update, so anything that must survive has to be
// written back before returning. Maps are the exception: m.answers is shared, and
// mutating it in place is deliberate, because it is the one piece of state that
// outlives a screen.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case tea.BackgroundColorMsg:
		m.styles = NewStyles(msg.IsDark())
		m.help.Styles = help.DefaultStyles(msg.IsDark())
		m.applyStyles()
		return m, nil

	case probeMsg:
		return m.handleProbe(msg)

	case copyMsg:
		if msg.err != nil {
			m.notice, m.noticeIsError = "could not copy: "+msg.err.Error(), true
		} else {
			m.copied = true
			m.notice, m.noticeIsError = "command copied to the clipboard", false
		}
		return m, nil
	}

	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		if key.Matches(keyMsg, m.keys.Quit) && !m.textEntryFocused() {
			return m, tea.Quit
		}
		if key.Matches(keyMsg, m.keys.Help) && !m.textEntryFocused() {
			m.help.ShowAll = !m.help.ShowAll
			m.resize()
			return m, nil
		}
	}

	switch m.screen {
	case ScreenInput:
		return m.updateInput(msg, isKey, keyMsg)
	case ScreenTask:
		return m.updateTask(keyMsg, isKey)
	case ScreenStep:
		return m.updateStep(msg, keyMsg, isKey)
	case ScreenOutput:
		return m.updateOutput(msg, keyMsg, isKey)
	case ScreenReview:
		return m.updateReview(keyMsg, isKey)
	}
	return m, nil
}

// textEntryFocused reports whether a letter key belongs to a text field rather
// than to the wizard. Without it, typing "q" into the output path would quit and
// typing "e" would go back (DECISIONS D14).
func (m Model) textEntryFocused() bool {
	switch m.screen {
	case ScreenOutput:
		return true
	case ScreenStep:
		step, ok := m.currentStep()
		return ok && step.Type != tasks.StepChoice && step.Type != tasks.StepConfirm
	}
	return false
}

// handleProbe records the result of probing and shows the file summary.
func (m Model) handleProbe(msg probeMsg) (tea.Model, tea.Cmd) {
	m.probing = false
	if msg.err != nil {
		m.input, m.info, m.chosen = "", nil, false
		m.screen = ScreenInput
		m.notice = fmt.Sprintf("could not read %s: %v", filepath.Base(msg.path), msg.err)
		m.noticeIsError = true
		return m, m.picker.Init()
	}
	m.input, m.info = msg.path, msg.info
	m.chosen = true
	m.notice, m.noticeIsError = "", false
	m.screen = ScreenInput
	return m, nil
}

// ── layout ──────────────────────────────────────────────────────────────────

// resize tells the sub-components how much room they have.
func (m *Model) resize() {
	// The picker has to be sized explicitly: AutoHeight is off so that the
	// wizard controls the layout (a picker that resizes itself fights the
	// preview pane).
	m.picker.SetHeight(bodyHeightFor(m.height, m.help.ShowAll))
	width := visibleWidth(m.width)
	m.stepInput.SetWidth(width - 4)
	m.outputInput.SetWidth(width - 4)
	m.help.SetWidth(m.width)
}

// bodyHeightFor is the height left for the main pane once the header, rules,
// hint line, command pane and help line have taken their share.
//
// Rules rather than borders keep this affordable at 24 rows, which plan.md §6
// requires the wizard to survive.
func bodyHeightFor(height int, fullHelp bool) int {
	if height <= 0 {
		height = 24
	}
	helpLines := 1
	if fullHelp {
		helpLines = 6
	}
	const chrome = 1 /*header*/ + 4 /*rules*/ + 1 /*hint*/ + 1 /*command title*/ + 1 /*help*/
	body := height - chrome - helpLines - commandLines
	if body < 3 {
		body = 3
	}
	return body
}

// commandLines is how many lines the command preview gets.
const commandLines = 3

// View renders the whole screen.
func (m Model) View() tea.View {
	width := visibleWidth(m.width)
	bodyHeight := bodyHeightFor(m.height, m.help.ShowAll)

	var b strings.Builder
	b.WriteString(m.header(width))
	b.WriteString("\n")
	b.WriteString(m.styles.rule(width))
	b.WriteString("\n")
	b.WriteString(fitLines(m.body(width), bodyHeight))
	b.WriteString("\n")
	b.WriteString(m.styles.rule(width))
	b.WriteString("\n")
	b.WriteString(fitLines(m.hintLine(width), 1))
	b.WriteString("\n")
	b.WriteString(m.styles.rule(width))
	b.WriteString("\n")
	b.WriteString(fitLines(m.commandPane(width), commandLines+1))
	b.WriteString("\n")
	b.WriteString(m.styles.rule(width))
	b.WriteString("\n")
	b.WriteString(m.help.View(m.helpBindings()))

	// A last clamp on the whole frame. Every pane above tries to fit, but a long
	// path or a file name in the picker must never be able to widen the frame:
	// one overlong line is enough to make a terminal wrap and scramble the
	// layout. MaxWidth truncates per line and is escape-aware, so it cannot cut
	// an SGR sequence in half.
	content := lipgloss.NewStyle().MaxWidth(width).Render(b.String())

	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

// body renders the main pane of the current screen.
func (m Model) body(width int) string {
	switch m.screen {
	case ScreenInput:
		return m.inputBody(width)
	case ScreenTask:
		return m.taskBody(width)
	case ScreenStep:
		return m.stepBody(width)
	case ScreenOutput:
		return m.outputBody(width)
	default:
		return m.reviewBody(width)
	}
}

// hint is the "why" sentence for whatever is highlighted on the current screen.
func (m Model) hint() string {
	switch m.screen {
	case ScreenInput:
		return m.hintInput()
	case ScreenTask:
		return m.hintTask()
	case ScreenStep:
		return m.hintStep()
	case ScreenOutput:
		return m.hintOutput()
	default:
		return m.hintReview()
	}
}

// header is the app name and the breadcrumb (plan.md §6).
func (m Model) header(width int) string {
	crumbs := []struct {
		label  string
		screen Screen
	}{
		{"Input", ScreenInput},
		{"Task", ScreenTask},
		{"Options", ScreenStep},
		{"Review", ScreenReview},
	}
	parts := make([]string, 0, len(crumbs))
	for _, c := range crumbs {
		active := m.screen == c.screen ||
			// The output screen is still part of the options phase.
			(c.screen == ScreenStep && m.screen == ScreenOutput)
		style := m.styles.Breadcrumb
		if active {
			style = m.styles.BreadcrumbOn
		}
		parts = append(parts, style.Render(c.label))
	}
	left := m.styles.Heading.Render("ffwiz")
	right := strings.Join(parts, m.styles.Breadcrumb.Render(" > "))
	return row(width, left, right)
}

// hintLine is the "why" text for whatever is highlighted (plan.md §6). A notice
// takes precedence, because a message the user has not seen yet is more
// important than an explanation they can read again.
func (m Model) hintLine(width int) string {
	switch {
	case m.probing:
		return m.styles.Muted.Render("reading " + filepath.Base(m.input) + "...")
	case m.notice != "":
		style := m.styles.Warning
		if m.noticeIsError {
			style = m.styles.Error
		}
		return style.Render(truncate(m.notice, width))
	}
	return m.styles.Hint.Render(truncate(m.hint(), width))
}

// helpBindings is the footer help for the current screen.
func (m Model) helpBindings() helpView {
	k := m.keys
	switch m.screen {
	case ScreenInput:
		return helpView{
			short: []key.Binding{k.Up, k.Down, k.Select, k.ShowAll, k.Quit},
			full: [][]key.Binding{
				{k.Up, k.Down, k.Select},
				{k.ShowAll, k.Quit, k.Help},
			},
		}
	case ScreenTask:
		return helpView{
			short: []key.Binding{k.Up, k.Down, k.Select, k.Back, k.Quit},
			full: [][]key.Binding{
				{k.Up, k.Down, k.Select},
				{k.Back, k.Quit, k.Help},
			},
		}
	case ScreenStep:
		return helpView{
			short: []key.Binding{k.Up, k.Down, k.Select, k.Back, k.Quit},
			full: [][]key.Binding{
				{k.Up, k.Down, k.Select},
				{k.Back, k.Quit, k.Help},
			},
		}
	case ScreenOutput:
		return helpView{
			short: []key.Binding{k.Select, k.Back, k.Quit},
			full: [][]key.Binding{
				{k.Select, k.Back},
				{k.Quit, k.Help},
			},
		}
	default:
		bindings := []key.Binding{k.Copy, k.Back, k.Quit}
		return helpView{
			short: bindings,
			full: [][]key.Binding{
				{k.Copy, k.Back},
				{k.Quit, k.Help},
			},
		}
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// buildOptions are the parts of an ffmpeg request that are not answers.
func buildOptions(input, output string) tasks.BuildOptions {
	return tasks.BuildOptions{Input: input, Output: output}
}

// row places two strings at the ends of a line of the given width.
func row(width int, left, right string) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// fitLines pads a block to exactly height lines, or trims it to fit. Padding
// matters: without it the panes below would jump up and down as content changes
// and the command preview would move while the user is reading it.
func fitLines(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// truncate cuts a string to width visible columns, keeping the start and
// marking the cut. Use it for messages, where the beginning is the point.
func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) > width-3 {
		runes = runes[:max(width-3, 0)]
	}
	return string(runes) + "..."
}

// shortPath cuts a path to width visible columns, keeping the end of it.
//
// The tail of a path is the part that identifies the file, so cutting it off
// would leave the user looking at the one part they already know.
func shortPath(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		return truncate(s, width)
	}
	runes := []rune(s)
	keep := width - 3
	if keep > len(runes) {
		keep = len(runes)
	}
	return "..." + string(runes[len(runes)-keep:])
}

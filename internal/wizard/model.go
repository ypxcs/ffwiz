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
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
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
	// ScreenPreset picks a named answer set for that job, when it has one
	// (plan.md §5).
	ScreenPreset
	// ScreenStep asks the current step's question.
	ScreenStep
	// ScreenOutput chooses where to write.
	ScreenOutput
	// ScreenReview shows the finished command.
	ScreenReview
	// ScreenEdit is the review screen's hand-editing mode (plan.md §6.6).
	ScreenEdit
	// ScreenRun shows the encode in progress.
	ScreenRun
	// ScreenDone reports how the run ended.
	ScreenDone
)

// Runner starts ffmpeg and hands back a handle to watch.
//
// It is a parameter rather than a direct call to ffmpeg.Start so that the model
// tests can drive a whole run - progress, cancellation, failure - without
// spawning a process (plan.md §3, rule 4).
type Runner func(ctx context.Context, ffmpegPath string, cmd *ffmpeg.Command, opts ffmpeg.RunOptions) (RunHandle, error)

// RunHandle is a running ffmpeg: a stream of events and a way to stop it.
// *ffmpeg.Run satisfies it.
type RunHandle interface {
	// Events is closed after the final Done event.
	Events() <-chan ffmpeg.Event
	// Cancel stops the process, politely at first.
	Cancel()
}

// startFFmpeg is the Runner used when a caller does not supply one.
func startFFmpeg(ctx context.Context, ffmpegPath string, cmd *ffmpeg.Command, opts ffmpeg.RunOptions) (RunHandle, error) {
	return ffmpeg.Start(ctx, ffmpegPath, cmd, opts)
}

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
	// TempDir is where a command's own files are written - today the
	// concatenation list. Empty means the system temporary directory; the
	// wizard a user runs passes a directory it cleans up on exit (plan.md §8).
	TempDir string
	// FreeSpace reports the bytes available on the filesystem holding a
	// directory. Nil means ffmpeg.FreeSpace, which is only implemented on
	// Unix; it returns an error elsewhere and the disk-space warning is simply
	// not shown (plan.md §8, "best effort").
	FreeSpace func(dir string) (int64, error)
	// Run starts ffmpeg. Nil means use ffmpeg.Start.
	Run Runner
	// Reveal shows a file in the desktop's file manager, best effort. Nil means
	// the platform default.
	Reveal func(path string) error
	// Theme pins the colour scheme. The zero value follows the terminal's own
	// answer about its background (DECISIONS D46).
	Theme Theme
	// ProbeTimeout bounds the ffprobe run. Zero uses the package default.
	ProbeTimeout time.Duration
}

// Model is the root Bubble Tea model.
type Model struct {
	styles Styles
	keys   bindings
	help   help.Model
	// theme is the - possibly pinned - colour scheme, kept so that a terminal
	// reporting its background cannot override an explicit --theme.
	theme Theme

	set     *tasks.Set
	caps    *ffmpeg.Capabilities
	tools   ffmpeg.Tools
	clip    Clipboard
	timeout time.Duration
	tempDir string
	space   func(dir string) (int64, error)

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
	// presetIndex is the row under the cursor on the preset screen. It runs
	// over the task's presets and then one past the end, which is the "no
	// preset" row (DECISIONS D45).
	presetIndex int

	// engine, answers and stepIndex drive the step screens. answers holds only
	// what the user actually chose; defaults are applied by the engine on every
	// resolve, so a changed answer can never leave a stale one behind.
	engine    *tasks.Engine
	answers   map[string]string
	stepIndex int
	choiceIdx int
	stepInput textinput.Model

	// The richer step types need state of their own, seeded from the resolved
	// answer in syncStep so that going back and forward restores what was
	// entered (plan.md §6.4).
	//
	// toggles is the checkbox state of the multi step on screen, keyed by choice
	// value; confirmYes is the answer of the confirm step on screen; rangeStart
	// and rangeEnd are the two fields of a time_range step, with rangeField
	// naming which one has the cursor; customInput holds a typed resolution.
	toggles     map[string]bool
	confirmYes  bool
	rangeStart  textinput.Model
	rangeEnd    textinput.Model
	rangeField  int
	customInput textinput.Model

	// The concat step browses the filesystem itself rather than through the
	// shared filepicker, because it needs three things the picker does not
	// offer: a checkbox per entry, a stable order for the clips chosen, and `/`
	// to narrow the listing (DECISIONS D13).
	//
	// concatPicked is the answer - the clips in playing order. concatEntries is
	// the current directory, concatCursor is the row under the cursor, and
	// concatInfo holds what has been probed of each clip, which is what the
	// "streams differ" warning is computed from (plan.md §5.9).
	concatDir       string
	concatEntries   []concatEntry
	concatCursor    int
	concatPicked    []string
	concatInfo      map[string]*ffmpeg.MediaInfo
	concatProbing   map[string]bool
	concatFilter    textinput.Model
	concatFiltering bool
	concatShowAll   bool
	concatErr       string

	outputPath  string
	outputInput textinput.Model
	// overwrite is true only after the user has confirmed replacing an existing
	// file on the output screen. It is what turns -n into -y, and it is never
	// set on the user's behalf (plan.md §8).
	overwrite bool
	// conflict is the output screen's "that file already exists" sub-state, and
	// conflictIdx is the option the cursor is on.
	conflict    bool
	conflictIdx int

	copied bool

	// The review screen's edit mode (plan.md §6.6). edited holds the arguments
	// the user produced by hand, or nil while the wizard still owns the
	// command; editArea is the text area they type in (DECISIONS D44).
	edited   []string
	editArea textarea.Model
	// editErr is the message from the last refused edit, shown under the text
	// area rather than in the one-line notice, because the cursor is there.
	editErr string

	// The run screen. handle is nil until ffmpeg has been started; progress,
	// logs and elapsed are what it has reported so far.
	runner    Runner
	reveal    func(path string) error
	handle    RunHandle
	starting  bool
	progress  ffmpeg.Progress
	elapsed   time.Duration
	startedAt time.Time
	logs      []string
	showLog   bool
	bar       progress.Model
	spin      spinner.Model
	// askingToStop is the "stop the encode?" confirmation (plan.md §6.7).
	askingToStop bool

	// The done screen.
	result   ffmpeg.Result
	outcome  ffmpeg.Outcome
	hintText string
	// removed records that the partial output was deleted after a cancel, so
	// the screen does not offer to delete it twice.
	removed bool
}

// maxLogLines bounds the log pane. ffmpeg's stderr is short with -nostats, but a
// failing run can repeat a line for every frame, and an unbounded slice in a
// long-lived model is a leak.
const maxLogLines = 500

// New builds the wizard in its starting state.
func New(deps Deps) Model {
	// A nil clipboard becomes one that refuses every write, so the copy action
	// has a single code path rather than a nil check in the middle of it.
	clip := deps.Clipboard
	if clip == nil {
		clip = UnavailableClipboard{}
	}
	// Likewise a nil FreeSpace: the disk-space warning asks a function that
	// answers "unknown" where the platform cannot (plan.md §8).
	space := deps.FreeSpace
	if space == nil {
		space = ffmpeg.FreeSpace
	}

	m := Model{
		styles:  NewStyles(deps.Theme.IsDark()),
		keys:    newBindings(),
		help:    help.New(),
		theme:   deps.Theme,
		set:     deps.Set,
		caps:    deps.Caps,
		tools:   deps.Tools,
		clip:    clip,
		runner:  deps.Run,
		reveal:  deps.Reveal,
		timeout: deps.ProbeTimeout,
		tempDir: deps.TempDir,
		space:   space,
		answers: map[string]string{},
		width:   80,
		height:  24,
		screen:  ScreenInput,
		input:   deps.Input,
	}
	m.help.Styles = help.DefaultStyles(deps.Theme.IsDark())
	if m.runner == nil {
		m.runner = startFFmpeg
	}
	if m.reveal == nil {
		m.reveal = revealInFileManager
	}

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
	m.rangeStart = textinput.New()
	m.rangeEnd = textinput.New()
	m.customInput = textinput.New()
	m.concatFilter = textinput.New()
	m.editArea = textarea.New()
	// The command is one long line, and ffwiz writes it out in full: wrapping
	// would put a line break in the middle of a path where there is none.
	m.editArea.ShowLineNumbers = false
	m.toggles = map[string]bool{}
	m.concatInfo = map[string]*ffmpeg.MediaInfo{}
	m.concatProbing = map[string]bool{}
	m.bar = progress.New(progress.WithWidth(40), progress.WithoutPercentage())
	m.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
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
	m.rangeStart.SetStyles(textinput.DefaultStyles(isDark))
	m.rangeEnd.SetStyles(textinput.DefaultStyles(isDark))
	m.customInput.SetStyles(textinput.DefaultStyles(isDark))
	m.concatFilter.SetStyles(textinput.DefaultStyles(isDark))
	m.editArea.SetStyles(textarea.DefaultStyles(isDark))
	// The default prompt is a thick left border, which in this layout reads as
	// a dialog to be dismissed rather than a field being typed into; every
	// other pane here is delimited by a rule. The editor gets an indent instead.
	m.editArea.Prompt = "  "
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
		// An explicit --theme wins: the user is overriding what the terminal
		// says about itself, which is the reason the flag exists (DECISIONS D46).
		if m.theme.Pinned() {
			return m, nil
		}
		m.styles = NewStyles(msg.IsDark())
		m.help.Styles = help.DefaultStyles(msg.IsDark())
		m.applyStyles()
		return m, nil

	case probeMsg:
		return m.handleProbe(msg)

	case concatProbeMsg:
		// A clip read for the concat browser: it never reaches the input
		// screen's probe handling, which is about the file being edited.
		return m.handleConcatProbe(msg)

	case copyMsg:
		if msg.err != nil {
			m.notice, m.noticeIsError = "could not copy: "+msg.err.Error(), true
		} else {
			m.copied = true
			m.notice, m.noticeIsError = "command copied to the clipboard", false
		}
		return m, nil

	case runStartedMsg:
		return m.handleRunStarted(msg)

	case runEventMsg:
		return m.handleRunEvent(msg)

	case revealMsg:
		if msg.err != nil {
			m.notice, m.noticeIsError = "could not open the folder: "+msg.err.Error(), true
		} else {
			m.notice, m.noticeIsError = "asked the file manager to show the file", false
		}
		return m, nil

	case tickMsg:
		return m.handleTick(msg)

	case spinner.TickMsg:
		// The spinner reschedules itself, so it is started once and stopped by
		// no longer forwarding its command when the run is over.
		if m.screen != ScreenRun {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		// A run owns its keys. `q` and ctrl+c ask to stop rather than quit the
		// wizard, because a half-written output file is not something to walk
		// away from by accident (plan.md §6.7).
		if m.screen == ScreenRun {
			return m.updateRun(keyMsg)
		}
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
	case ScreenPreset:
		return m.updatePreset(keyMsg, isKey)
	case ScreenStep:
		return m.updateStep(msg, keyMsg, isKey)
	case ScreenOutput:
		return m.updateOutput(msg, keyMsg, isKey)
	case ScreenReview:
		return m.updateReview(keyMsg, isKey)
	case ScreenEdit:
		return m.updateEdit(msg, keyMsg, isKey)
	case ScreenDone:
		return m.updateDone(msg, keyMsg, isKey)
	}
	return m, nil
}

// textEntryFocused reports whether a letter key belongs to a text field rather
// than to the wizard. Without it, typing "q" into the output path would quit and
// typing "e" would go back (DECISIONS D14).
func (m Model) textEntryFocused() bool {
	switch m.screen {
	case ScreenEdit:
		// The text area owns every letter (plan.md §6.6).
		return true
	case ScreenOutput:
		// The conflict prompt is a menu, not the name field: with it open, `q`
		// must quit and `e` must not be swallowed by the input.
		return !m.conflict
	case ScreenStep:
		step, ok := m.currentStep()
		if !ok {
			return false
		}
		switch step.Type {
		case tasks.StepInput, tasks.StepTimeRange:
			// A text field owns every letter, so `q`, `e` and `j` are text.
			return true
		case tasks.StepConcat:
			// Only while a filter is being typed; the browser itself is a list.
			return m.concatFiltering
		case tasks.StepResolution:
			// Only the "Custom..." row is a text field; the presets are a list.
			shown := m.resolution().Shown[step.ID]
			return step.Custom && m.choiceIdx == customRowIndex(step, shown)
		}
		return false
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
	m.picker.SetHeight(m.bodyHeight())
	width := visibleWidth(m.width)
	m.stepInput.SetWidth(width - 4)
	m.outputInput.SetWidth(width - 4)
	m.rangeStart.SetWidth(width - 4)
	m.rangeEnd.SetWidth(width - 4)
	m.customInput.SetWidth(width - 4)
	m.concatFilter.SetWidth(width - 4)
	m.editArea.SetWidth(width - 4)
	// The text area takes the main pane minus its heading, its explanation and
	// the error line, which is why it is not simply the body height.
	m.editArea.SetHeight(max(m.bodyHeight()-5, 3))
	m.help.SetWidth(m.width)
	// The bar leaves room for the percentage that follows it and for the indent
	// the run screen gives its content.
	m.bar.SetWidth(clamp(width-20, 8, 60))
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

// bodyHeight is the height of the main pane on the current screen.
//
// The edit screen takes the command pane's rows for itself: there the command
// *is* the main pane, so a second copy of it underneath would waste the four
// lines that the text area needs most at 24 rows (plan.md §6.6).
func (m Model) bodyHeight() int {
	h := bodyHeightFor(m.height, m.help.ShowAll)
	if m.screen == ScreenEdit {
		h += commandLines + 2 // the pane's rows and the rules around it
	}
	return h
}

// View renders the whole screen.
func (m Model) View() tea.View {
	width := visibleWidth(m.width)
	bodyHeight := m.bodyHeight()

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
	if m.screen != ScreenEdit {
		b.WriteString(m.styles.rule(width))
		b.WriteString("\n")
		b.WriteString(fitLines(m.commandPane(width), commandLines+1))
		b.WriteString("\n")
	}
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
	case ScreenPreset:
		return m.presetBody(width)
	case ScreenStep:
		return m.stepBody(width)
	case ScreenOutput:
		return m.outputBody(width)
	case ScreenEdit:
		return m.editBody(width)
	case ScreenRun:
		return m.runBody(width)
	case ScreenDone:
		return m.doneBody(width)
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
	case ScreenPreset:
		return m.hintPreset()
	case ScreenStep:
		return m.hintStep()
	case ScreenOutput:
		return m.hintOutput()
	case ScreenEdit:
		return m.hintEdit()
	case ScreenRun:
		return m.hintRun()
	case ScreenDone:
		return m.hintDone()
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
		{"Run", ScreenRun},
	}
	parts := make([]string, 0, len(crumbs))
	for _, c := range crumbs {
		active := m.screen == c.screen ||
			// The preset screen is still the task phase - it is a way of
			// choosing the job, one level more specific - the output screen is
			// still part of the options phase, the edit screen still part of the
			// review, and the done screen part of the run.
			(c.screen == ScreenTask && m.screen == ScreenPreset) ||
			(c.screen == ScreenStep && m.screen == ScreenOutput) ||
			(c.screen == ScreenReview && m.screen == ScreenEdit) ||
			(c.screen == ScreenRun && m.screen == ScreenDone)
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
	case ScreenPreset:
		return helpView{
			short: []key.Binding{k.Up, k.Down, k.Select, k.Back, k.Quit},
			full: [][]key.Binding{
				{k.Up, k.Down, k.Select},
				{k.Back, k.Quit, k.Help},
			},
		}
	case ScreenStep:
		short := []key.Binding{k.Up, k.Down, k.Select, k.Back, k.Quit}
		if step, ok := m.currentStep(); ok {
			switch step.Type {
			case tasks.StepMulti, tasks.StepConfirm:
				short = []key.Binding{k.Up, k.Down, k.Toggle, k.Select, k.Back, k.Quit}
			case tasks.StepTimeRange:
				short = []key.Binding{k.Tab, k.Select, k.Back, k.Quit}
			case tasks.StepConcat:
				if m.concatFiltering {
					short = []key.Binding{k.Select, k.Filter}
				} else {
					short = []key.Binding{k.Up, k.Down, k.Toggle, k.Open, k.Filter, k.Select, k.Back, k.Quit}
				}
			}
		}
		return helpView{
			short: short,
			full: [][]key.Binding{
				short,
				{k.Help},
			},
		}
	case ScreenOutput:
		if m.conflict {
			return helpView{
				short: []key.Binding{k.Up, k.Down, k.Select, k.Quit},
				full: [][]key.Binding{
					{k.Up, k.Down, k.Select},
					{k.Back, k.Quit, k.Help},
				},
			}
		}
		return helpView{
			short: []key.Binding{k.Select, k.Back, k.Quit},
			full: [][]key.Binding{
				{k.Select, k.Back},
				{k.Quit, k.Help},
			},
		}
	case ScreenEdit:
		return helpView{
			short: []key.Binding{k.Apply, k.BackEsc, k.Quit},
			full: [][]key.Binding{
				{k.Apply, k.BackEsc, k.Quit},
				{k.Help},
			},
		}
	case ScreenRun:
		return helpView{
			short: []key.Binding{k.Cancel, k.Log},
			full: [][]key.Binding{
				{k.Cancel, k.Log},
				{k.Help},
			},
		}
	case ScreenDone:
		// Only the keys that lead somewhere are listed, matching what the screen
		// offers: no `o` on a run that wrote nothing, no `d` without a partial
		// file (DECISIONS D32).
		bindings := []key.Binding{k.Back}
		if m.outcome == ffmpeg.OutcomeCancelled && !m.removed && m.outputExists() {
			bindings = append(bindings, k.Delete)
		}
		if m.canReveal() {
			bindings = append(bindings, k.Reveal)
		}
		bindings = append(bindings, k.Quit)
		return helpView{
			short: bindings,
			full: [][]key.Binding{
				bindings,
				{k.Help},
			},
		}
	default:
		bindings := []key.Binding{k.Run, k.Copy, k.Edit, k.Script, k.Back, k.Quit}
		if m.edited != nil {
			// An edited command is the user's own, so the offer to undo it is
			// part of the same line (DECISIONS D44).
			bindings = []key.Binding{k.Run, k.Copy, k.Revert, k.Edit, k.Back, k.Quit}
		}
		return helpView{
			short: bindings,
			full: [][]key.Binding{
				{k.Run, k.Copy},
				{k.Edit, k.Script, k.Revert},
				{k.Back, k.Quit, k.Help},
			},
		}
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// buildOptions are the parts of an ffmpeg request that are not answers.
func (m Model) buildOptions() tasks.BuildOptions {
	return tasks.BuildOptions{Input: m.input, Output: m.outputPath, Overwrite: m.overwrite}
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

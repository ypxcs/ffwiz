package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
	"github.com/ypxcs/ffwiz/internal/tasks"
)

// The concat step is the one question a fixed list of choices cannot express:
// "which files, in what order". It browses the filesystem itself rather than
// through the shared filepicker, for three reasons the picker does not cover
// (DECISIONS D13):
//
//   - a clip has to stay ticked while the user moves to the next one, possibly
//     in another directory, so the chosen list is state of its own;
//   - the order the clips are ticked is the order they play, so the answer is a
//     list rather than a set;
//   - `/` has to narrow the listing by name, which needs the entries, not just
//     a selected path.
//
// It also warns when the clips do not share their stream layout, because the
// concat demuxer copies streams rather than re-encoding them (plan.md §5.9).

// concatEntry is one row of the clip browser.
type concatEntry struct {
	Name  string
	Path  string
	IsDir bool
	// parent marks the ".." row, which is always offered and is the keyboard's
	// way up as well as the cursor's.
	parent bool
}

// concatExtensions are the files the browser offers when it is not showing
// everything. Unlike the input picker this excludes subtitles and still images:
// a concat list holds clips, and offering a .srt would only invite a command
// that cannot work.
var concatExtensions = []string{
	".mp4", ".m4v", ".mkv", ".mov", ".avi", ".webm", ".flv", ".wmv", ".mpg", ".mpeg", ".ts", ".m2ts", ".ogv",
	".mp3", ".m4a", ".aac", ".flac", ".wav", ".ogg", ".oga", ".opus", ".wma", ".aiff",
}

// concatMaxEntries bounds what one directory contributes. A directory with a
// hundred thousand files would otherwise be read on the event loop and rendered
// in full; `/` is the way through a directory that large.
const concatMaxEntries = 5000

// concatProbeMsg carries the outcome of reading one clip, so the streams of the
// chosen files can be compared.
type concatProbeMsg struct {
	path string
	info *ffmpeg.MediaInfo
	err  error
}

// concatProbeCmd reads one clip off the event loop.
func (m Model) concatProbeCmd(path string) tea.Cmd {
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
		return concatProbeMsg{path: path, info: info, err: err}
	}
}

// handleConcatProbe records what a clip turned out to hold.
func (m Model) handleConcatProbe(msg concatProbeMsg) (tea.Model, tea.Cmd) {
	delete(m.concatProbing, msg.path)
	if msg.err != nil {
		m.notice = fmt.Sprintf("could not read %s: %v", filepath.Base(msg.path), msg.err)
		m.noticeIsError = true
		return m, nil
	}
	m.concatInfo[msg.path] = msg.info
	return m, nil
}

// syncConcat points the browser at the concat step, restoring the clips already
// chosen so stepping back and forward keeps them and their order (plan.md §6.4).
func (m *Model) syncConcat(step tasks.Step) {
	m.concatPicked = tasks.ParseFileList(m.resolution().Answers[step.ID])
	m.concatFiltering = false
	m.concatFilter.SetValue("")
	m.concatFilter.Blur()
	if m.concatDir == "" {
		m.concatDir = m.defaultConcatDir()
	}
	m.setConcatDir(m.concatDir)
}

// defaultConcatDir is where the browser opens: beside the input file, which is
// where the clips usually are, or the working directory when there is no input.
func (m Model) defaultConcatDir() string {
	if m.input != "" {
		if abs, err := filepath.Abs(m.input); err == nil {
			return filepath.Dir(abs)
		}
		return filepath.Dir(m.input)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return string(filepath.Separator)
}

// setConcatDir reads a directory into the browser.
func (m *Model) setConcatDir(dir string) {
	entries, errMsg := readConcatDir(dir, m.concatShowAll)
	m.concatDir = dir
	m.concatEntries = entries
	m.concatErr = errMsg
	m.concatCursor = 0
	if errMsg != "" {
		m.notice, m.noticeIsError = "cannot read "+shortPath(dir, 40)+": "+errMsg, true
	}
}

// readConcatDir lists a directory: ".." first, then directories, then files,
// each alphabetically. showAll also offers hidden entries and files whose
// extension is not a media one.
//
// It returns the entries and, separately, a read error, so the browser can say
// a directory could not be opened and carry on showing nothing rather than
// leaving the screen blank with no explanation.
func readConcatDir(dir string, showAll bool) ([]concatEntry, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err.Error()
	}

	var dirs, files []concatEntry
	for _, e := range entries {
		name := e.Name()
		if !showAll && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			// A symlink to a directory is worth navigating into; os.DirEntry
			// reports a symlink as neither a file nor a directory.
			if info, err := os.Stat(full); err == nil && info.IsDir() {
				isDir = true
			}
		}
		if isDir {
			dirs = append(dirs, concatEntry{Name: name, Path: full, IsDir: true})
			continue
		}
		if !showAll && !hasExtension(name, concatExtensions) {
			continue
		}
		files = append(files, concatEntry{Name: name, Path: full})
	}

	byName := func(s []concatEntry) {
		sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	}
	byName(dirs)
	byName(files)

	out := make([]concatEntry, 0, len(dirs)+len(files)+1)
	if parent := filepath.Dir(dir); parent != dir {
		out = append(out, concatEntry{Name: "..", Path: parent, IsDir: true, parent: true})
	}
	out = append(out, dirs...)
	out = append(out, files...)
	if len(out) > concatMaxEntries {
		out = out[:concatMaxEntries]
	}
	return out, ""
}

// hasExtension reports whether name ends in one of the given extensions.
func hasExtension(name string, exts []string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range exts {
		if ext == e {
			return true
		}
	}
	return false
}

// concatVisible is what the browser shows: the directory's entries with the name
// filter applied. The ".." row is always kept, so a filter that matches nothing
// still leaves a way out of the directory.
func (m Model) concatVisible() []concatEntry {
	filter := strings.ToLower(strings.TrimSpace(m.concatFilter.Value()))
	if filter == "" {
		return m.concatEntries
	}
	out := make([]concatEntry, 0, len(m.concatEntries))
	for _, e := range m.concatEntries {
		if e.parent || strings.Contains(strings.ToLower(e.Name), filter) {
			out = append(out, e)
		}
	}
	return out
}

// indexOfPath finds a path in the chosen list, or -1.
func indexOfPath(paths []string, path string) int {
	for i, p := range paths {
		if p == path {
			return i
		}
	}
	return -1
}

// concatBody renders the chosen clips and the browser.
func (m Model) concatBody(width int) string {
	var b strings.Builder

	b.WriteString(m.concatPickedBlock(width))
	b.WriteString("\n")

	header := m.styles.Label.Render("Choose clips")
	if m.concatShowAll {
		header += "  " + m.styles.Warning.Render("(showing every file)")
	}
	b.WriteString(header + "  " + m.styles.Muted.Render(shortPath(m.concatDir, max(width-22, 10))) + "\n")

	if m.concatFiltering || m.concatFilter.Value() != "" {
		b.WriteString("  " + m.styles.Label.Render("filter: ") + m.concatFilter.View() + "\n")
	}

	if m.concatErr != "" {
		b.WriteString("  " + m.styles.Error.Render(truncate(m.concatErr, max(width-4, 10))) + "\n")
	}

	shown := m.concatVisible()
	if len(shown) == 0 {
		b.WriteString("  " + m.styles.Muted.Render("nothing here; h goes up a directory") + "\n")
	}
	for i, e := range shown {
		if i >= concatWindow {
			b.WriteString("  " + m.styles.Muted.Render(fmt.Sprintf("... %d more", len(shown)-i)) + "\n")
			break
		}
		b.WriteString(m.concatEntryLine(e, i == m.concatCursor, width) + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// concatWindow is how many browser rows are shown. The body is clipped to the
// terminal anyway; this only stops a large directory from being built in full
// on every frame.
const concatWindow = 12

// concatPickedBlock renders the clips chosen so far, in order, with the warning
// about a mismatch when there is one.
func (m Model) concatPickedBlock(width int) string {
	var b strings.Builder

	count := fmt.Sprintf("%d chosen", len(m.concatPicked))
	b.WriteString(m.styles.Label.Render("Clips, in order") + "  " + m.styles.Muted.Render(count) + "\n")
	if len(m.concatPicked) == 0 {
		b.WriteString("  " + m.styles.Muted.Render("none yet: put the cursor on a file and press space") + "\n")
		return strings.TrimRight(b.String(), "\n")
	}

	const show = 4
	for i, p := range m.concatPicked {
		if i == show {
			b.WriteString("  " + m.styles.Muted.Render(fmt.Sprintf("... and %d more", len(m.concatPicked)-i)) + "\n")
			break
		}
		label := shortPath(p, max(width-8, 10))
		b.WriteString("  " + m.styles.Muted.Render(fmt.Sprintf("%d. ", i+1)) + label + "\n")
	}

	if warning := m.concatWarning(); warning != "" {
		b.WriteString("  " + m.styles.Warning.Render(truncate(warning, max(width-2, 10))) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// concatEntryLine renders one row of the browser: a tick box for a file, a
// trailing slash for a directory.
func (m Model) concatEntryLine(e concatEntry, highlighted bool, width int) string {
	cursor := "  "
	if highlighted {
		cursor = m.styles.Key.Render("> ")
	}

	if e.IsDir {
		name := truncate(e.Name+string(filepath.Separator), max(width-6, 8))
		if highlighted {
			name = lipgloss.NewStyle().Bold(true).Render(name)
		} else {
			name = m.styles.Muted.Render(name)
		}
		return cursor + name
	}

	ticked := indexOfPath(m.concatPicked, e.Path) >= 0
	box := "[ ]"
	if ticked {
		box = "[x]"
	}
	name := truncate(e.Name, max(width-12, 8))
	switch {
	case ticked:
		name = m.styles.ChoiceCurrent.Render(name)
	case highlighted:
		name = lipgloss.NewStyle().Bold(true).Render(name)
	default:
		name = m.styles.Muted.Render(name)
	}
	return cursor + m.styles.Key.Render(box) + " " + name
}

// concatWarning explains the first way the chosen clips do not match, or "" when
// they all do or not all of them have been read yet.
//
// The concat demuxer copies the streams, so a mismatch is the difference between
// a joined file and a failed run; saying which attribute differs is what makes
// the warning actionable (plan.md §5.9).
func (m Model) concatWarning() string {
	if len(m.concatPicked) < 2 {
		return ""
	}
	var first *ffmpeg.MediaInfo
	for _, p := range m.concatPicked {
		info := m.concatInfo[p]
		if info == nil {
			return "" // still probing: better to say nothing than guess
		}
		if first == nil {
			first = info
			continue
		}
		if reason := streamsDiffer(first, info); reason != "" {
			return "the clips do not all match: " + reason +
				". They are copied rather than re-encoded, so they only join cleanly when they do."
		}
	}
	return ""
}

// streamsDiffer names the first attribute of two clips that would stop the
// concat demuxer copying them into one another.
func streamsDiffer(a, b *ffmpeg.MediaInfo) string {
	av, aVideo := a.FirstStream(ffmpeg.StreamVideo)
	bv, bVideo := b.FirstStream(ffmpeg.StreamVideo)
	if aVideo != bVideo {
		return "one clip has a picture stream and another does not"
	}
	if aVideo {
		if av.CodecName != bv.CodecName {
			return "the picture codecs differ (" + av.CodecName + " and " + bv.CodecName + ")"
		}
		if av.Width != bv.Width || av.Height != bv.Height {
			return fmt.Sprintf("the picture sizes differ (%dx%d and %dx%d)", av.Width, av.Height, bv.Width, bv.Height)
		}
		if av.FrameRate > 0 && bv.FrameRate > 0 && absFloat(av.FrameRate-bv.FrameRate) > 0.01 {
			return fmt.Sprintf("the frame rates differ (%.3g and %.3g)", av.FrameRate, bv.FrameRate)
		}
	}

	aa, aAudio := a.FirstStream(ffmpeg.StreamAudio)
	ba, bAudio := b.FirstStream(ffmpeg.StreamAudio)
	if aAudio != bAudio {
		return "one clip has a sound stream and another does not"
	}
	if aAudio {
		if aa.CodecName != ba.CodecName {
			return "the sound codecs differ (" + aa.CodecName + " and " + ba.CodecName + ")"
		}
		if aa.Channels != ba.Channels {
			return fmt.Sprintf("the channel counts differ (%d and %d)", aa.Channels, ba.Channels)
		}
		if aa.SampleRate != ba.SampleRate {
			return fmt.Sprintf("the sample rates differ (%d and %d)", aa.SampleRate, ba.SampleRate)
		}
	}
	return ""
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// updateConcatStep drives the clip browser. Answering is not done here: `enter`
// falls through to the shared stepAdvance, which is where every step type builds
// the answer it has just been given.
func (m Model) updateConcatStep(msg tea.Msg, keyMsg tea.KeyPressMsg, isKey bool) (tea.Model, tea.Cmd) {
	if _, ok := m.currentStep(); !ok {
		// Not on a step at all: the wizard has moved on, so the keys belong to
		// whatever is on screen now.
		return m.enterOutput()
	}

	if m.concatFiltering {
		if isKey {
			switch {
			case key.Matches(keyMsg, m.keys.BackEsc):
				// Give up on the filter, not on the step.
				m.concatFiltering = false
				m.concatFilter.SetValue("")
				m.concatFilter.Blur()
				m.concatCursor = 0
				return m, nil
			case key.Matches(keyMsg, m.keys.Select):
				m.concatFiltering = false
				m.concatFilter.Blur()
				m.concatCursor = 0
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.concatFilter, cmd = m.concatFilter.Update(msg)
		m.concatCursor = 0
		return m, cmd
	}

	if !isKey {
		return m, nil
	}

	shown := m.concatVisible()
	switch {
	case key.Matches(keyMsg, m.keys.Back):
		return m.stepBack()

	case key.Matches(keyMsg, m.keys.Filter):
		m.concatFiltering = true
		m.notice, m.noticeIsError = "", false
		return m, m.concatFilter.Focus()

	case key.Matches(keyMsg, m.keys.ShowAll):
		m.concatShowAll = !m.concatShowAll
		m.setConcatDir(m.concatDir)
		return m, nil

	case key.Matches(keyMsg, m.keys.Up):
		if len(shown) > 0 {
			m.concatCursor = wrap(m.concatCursor-1, len(shown))
		}

	case key.Matches(keyMsg, m.keys.Down):
		if len(shown) > 0 {
			m.concatCursor = wrap(m.concatCursor+1, len(shown))
		}

	case key.Matches(keyMsg, m.keys.Parent):
		m.concatGoUp()

	case key.Matches(keyMsg, m.keys.Open):
		m.concatOpen(shown)

	case key.Matches(keyMsg, m.keys.Toggle):
		return m.concatToggle(shown)

	case key.Matches(keyMsg, m.keys.Select):
		return m.stepAdvance()
	}
	return m, nil
}

// concatGoUp moves to the parent directory, unless it is already at the top.
func (m *Model) concatGoUp() {
	parent := filepath.Dir(m.concatDir)
	if parent == m.concatDir {
		m.notice, m.noticeIsError = "that is the top of the filesystem", false
		return
	}
	m.clearConcatFilter()
	m.setConcatDir(parent)
}

// concatOpen descends into the highlighted directory, or explains that a file is
// added rather than opened.
func (m *Model) concatOpen(shown []concatEntry) {
	e, ok := concatAt(shown, m.concatCursor)
	if !ok {
		return
	}
	if !e.IsDir {
		m.notice, m.noticeIsError = filepath.Base(e.Path)+" is a file: space adds it to the clips", false
		return
	}
	m.clearConcatFilter()
	m.setConcatDir(e.Path)
}

// clearConcatFilter drops the name filter. It is cleared on navigation, because
// a filter typed for one directory rarely means anything in the next.
func (m *Model) clearConcatFilter() {
	m.concatFiltering = false
	m.concatFilter.SetValue("")
	m.concatFilter.Blur()
}

// concatToggle adds or removes the highlighted file, keeping the order the clips
// were added in. A file is only read once, and only when it is first added.
func (m *Model) concatToggle(shown []concatEntry) (tea.Model, tea.Cmd) {
	e, ok := concatAt(shown, m.concatCursor)
	if !ok {
		return *m, nil
	}
	if e.IsDir {
		m.notice, m.noticeIsError = "that is a folder: l opens it", false
		return *m, nil
	}

	if i := indexOfPath(m.concatPicked, e.Path); i >= 0 {
		m.concatPicked = append(m.concatPicked[:i], m.concatPicked[i+1:]...)
		m.notice, m.noticeIsError = "", false
		return *m, nil
	}

	m.concatPicked = append(m.concatPicked, e.Path)
	m.notice, m.noticeIsError = "", false
	if m.concatInfo[e.Path] == nil && !m.concatProbing[e.Path] {
		m.concatProbing[e.Path] = true
		return *m, m.concatProbeCmd(e.Path)
	}
	return *m, nil
}

// concatAdvance records the clips, refusing fewer than two of them.
func (m Model) concatAdvance(step tasks.Step) (tea.Model, tea.Cmd) {
	answer := tasks.FileList(m.concatPicked)
	if err := step.CheckConcatAnswer(answer); err != nil {
		m.notice, m.noticeIsError = err.Error(), true
		return m, nil
	}
	return m.recordAndAdvance(step.ID, answer)
}

// concatAt is the entry at a row, if the row exists.
func concatAt(entries []concatEntry, i int) (concatEntry, bool) {
	if i < 0 || i >= len(entries) {
		return concatEntry{}, false
	}
	return entries[i], true
}

// hintConcat is the concat step's "why" line: the highlighted entry when there
// is one, and a reminder of the rule that makes joining possible.
func (m Model) hintConcat() string {
	if m.concatFiltering {
		return "type to narrow the list; enter keeps the filter, esc drops it"
	}
	shown := m.concatVisible()
	if e, ok := concatAt(shown, m.concatCursor); ok {
		if e.IsDir {
			return "l opens this folder; h goes back up"
		}
		if indexOfPath(m.concatPicked, e.Path) >= 0 {
			return "space takes this clip out of the join"
		}
		return "space adds this clip to the end of the join"
	}
	return "space adds the clip under the cursor; enter finishes"
}

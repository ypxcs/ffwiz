package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file runs ffmpeg (plan.md §7, "Running"). It knows nothing about the TUI:
// it reports what happens on a channel of Events, and the wizard turns those
// into messages. Keeping it that way is what makes a run testable without a
// terminal (plan.md §3, rule 4).

// Progress is one snapshot of a running encode, decoded from the key=value
// stream that `-progress pipe:1` writes to stdout.
type Progress struct {
	// OutTime is how far into the output ffmpeg has written. It can be
	// negative in unusual cases; Percent clamps it.
	OutTime time.Duration
	// TotalSize is the number of bytes written so far.
	TotalSize int64
	// Bitrate is the output bit rate in bits per second, or 0 when ffmpeg
	// reported none (it writes "N/A" until it has enough to measure).
	Bitrate float64
	// Speed is the encode speed as a multiple of realtime (1.0 means the
	// encode takes as long as the video plays). Zero means unknown.
	Speed float64
	// Frame is the number of frames written.
	Frame int64
	// Ended is true for the block finished by "progress=end".
	Ended bool
}

// Percent is how far through an output of the given length the encode is, in
// 0..100.
//
// The denominator is the *output* duration, not the input's: a trim or a speed
// change makes the output shorter, and using the input's length would leave the
// bar stuck below 100% (DECISIONS D3). A zero total means the length is unknown
// and there is nothing honest to report, so the caller must show progress some
// other way (plan.md §7).
func (p Progress) Percent(total time.Duration) float64 {
	if total <= 0 {
		return 0
	}
	pct := float64(p.OutTime) / float64(total) * 100
	switch {
	case pct < 0:
		return 0
	case pct > 100:
		return 100
	default:
		return pct
	}
}

// ETA estimates the time left, given the output's expected length. Zero means
// unknown (no speed reported, or the length is unknown).
func (p Progress) ETA(total time.Duration) time.Duration {
	if total <= 0 || p.Speed <= 0 || p.OutTime >= total {
		return 0
	}
	remaining := total - p.OutTime
	return time.Duration(float64(remaining) / p.Speed)
}

// ProgressDecoder turns the `-progress` key=value lines into Progress snapshots.
//
// ffmpeg writes each update as a block of lines and terminates it with
// `progress=continue` or `progress=end`. A snapshot is reported only at those
// boundaries, because mid-block the state is half-updated: reading a new
// out_time together with the previous block's speed would produce a nonsense
// ETA.
type ProgressDecoder struct {
	cur Progress
}

// Line consumes one line and reports a snapshot when the line ends a block.
func (d *ProgressDecoder) Line(line string) (Progress, bool) {
	key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return Progress{}, false
	}
	value = strings.TrimSpace(value)

	switch strings.TrimSpace(key) {
	case "frame":
		d.cur.Frame = parseCount(value, d.cur.Frame)
	case "total_size":
		d.cur.TotalSize = parseCount(value, d.cur.TotalSize)
	case "out_time_us", "out_time_ms":
		// DECISIONS D4: despite the name, out_time_ms carries *microseconds* in
		// ffmpeg's -progress output, exactly like out_time_us. Reading it as
		// milliseconds would run the bar a thousand times too fast.
		d.cur.OutTime = parseMicros(value, d.cur.OutTime)
	case "out_time":
		// The same instant as out_time_us, formatted as a clock. It is the
		// fallback for a build that omits the microsecond fields.
		if v, ok := parseClock(value); ok {
			d.cur.OutTime = v
		}
	case "bitrate":
		if v, ok := parseBitrate(value); ok {
			d.cur.Bitrate = v
		}
	case "speed":
		if v, ok := parseSpeed(value); ok {
			d.cur.Speed = v
		}
	case "progress":
		d.cur.Ended = value == "end"
		return d.cur, true
	}
	return Progress{}, false
}

// unknown reports ffmpeg's marker for a field it cannot measure yet. It is
// distinct from an absent field: "N/A" means "known to be unknown", and must
// clear an earlier value rather than leave a stale one behind.
func unknown(s string) bool { return s == "N/A" }

// parseCount reads a plain integer, keeping a previous value when the field is
// absent or unreadable.
func parseCount(s string, prev int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return prev
	}
	return v
}

// parseMicros reads a microsecond count.
func parseMicros(s string, prev time.Duration) time.Duration {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return prev
	}
	return time.Duration(v) * time.Microsecond
}

// parseClock reads ffmpeg's HH:MM:SS.ffffff form.
func parseClock(s string) (time.Duration, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds*float64(time.Second)), true
}

// parseBitrate reads ffmpeg's output bit rate, e.g. "183.6kbits/s", and returns
// bits per second. A field ffmpeg reports as "N/A" yields a known zero, so the
// caller can clear a stale rate; an absent or unreadable field yields false, so
// the caller keeps what it had.
func parseBitrate(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	if unknown(s) {
		return 0, true
	}
	digits := leadingNumber(s)
	if digits == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, false
	}
	switch rest := strings.ToLower(strings.TrimSpace(s[len(digits):])); {
	case strings.HasPrefix(rest, "k"):
		v *= 1e3
	case strings.HasPrefix(rest, "m"):
		v *= 1e6
	case strings.HasPrefix(rest, "g"):
		v *= 1e9
	}
	return v, true
}

// parseSpeed reads ffmpeg's speed field, e.g. "1.02x". It follows the same
// absent/N/A distinction as parseBitrate.
func parseSpeed(s string) (float64, bool) {
	s = strings.TrimSuffix(s, "x")
	if s == "" {
		return 0, false
	}
	if unknown(s) {
		return 0, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// leadingNumber returns the numeric prefix of s, or "" when s does not start
// with a digit or a dot.
func leadingNumber(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' {
			continue
		}
		if i == 0 {
			return ""
		}
		return s[:i]
	}
	return s
}

// Event is one thing that happened during a run.
//
// Exactly one of Progress, Log and Done is meaningful: a progress update has
// Progress set, a line of ffmpeg's log has Log set, and the final event has Done
// set. Log lines are separate from progress because the run screen shows them in
// a log pane (plan.md §6.7).
type Event struct {
	Progress *Progress
	Log      string
	Done     bool
	Result   Result
}

// Result is how a run ended.
type Result struct {
	// Err is non-nil when ffmpeg could not be started or exited non-zero. A
	// non-zero status is not by itself proof of failure, nor is a zero status
	// proof of success: see Diagnose (DECISIONS D9).
	Err error
	// ExitCode is the process exit status, or -1 when it never ran.
	ExitCode int
	// Cancelled is true when the run was stopped through Cancel or the
	// context, rather than by ffmpeg finishing on its own.
	Cancelled bool
	// Progress is the last snapshot received.
	Progress Progress
	// Stderr holds the last StderrLimit lines of ffmpeg's log.
	Stderr []string
}

// RunOptions tunes a run. The zero value is usable.
type RunOptions struct {
	// Dir is the process working directory. Empty inherits the current one.
	Dir string
	// Throttle is the shortest interval between progress updates, so a fast
	// encode does not flood the UI (plan.md §7). Zero means DefaultThrottle.
	Throttle time.Duration
	// KillAfter is how long ffmpeg is given to stop after an interrupt before
	// it is killed. Zero means DefaultKillAfter.
	KillAfter time.Duration
	// StderrLimit is how many log lines Result keeps for the error report.
	// Zero means DefaultStderrLimit.
	StderrLimit int
}

const (
	// DefaultThrottle caps progress updates at ten per second (plan.md §7).
	DefaultThrottle = 100 * time.Millisecond
	// DefaultKillAfter is the grace period between interrupt and kill.
	DefaultKillAfter = 5 * time.Second
	// DefaultStderrLimit is how much of the log is kept for an error report.
	DefaultStderrLimit = 50
	// eventBuffer is the event channel's capacity. It is generous enough that a
	// UI reading a frame at a time never falls behind; a snapshot dropped under
	// extreme pressure costs nothing, since the next one carries the same
	// information.
	eventBuffer = 128
)

func (o RunOptions) withDefaults() RunOptions {
	if o.Throttle <= 0 {
		o.Throttle = DefaultThrottle
	}
	if o.KillAfter <= 0 {
		o.KillAfter = DefaultKillAfter
	}
	if o.StderrLimit <= 0 {
		o.StderrLimit = DefaultStderrLimit
	}
	return o
}

// ErrNoCommand is returned by Start when there is nothing to run.
var ErrNoCommand = errors.New("no command to run")

// Run is one executing ffmpeg process.
type Run struct {
	events  chan Event
	proc    *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	kill    time.Duration
	once    sync.Once
	mu      sync.Mutex
	stopped bool
}

// Start launches cmd and returns immediately. The caller reads Events until the
// channel is closed; the last event always has Done set.
//
// Cancelling ctx stops the process in the same way Cancel does, so a wizard that
// is quitting does not leave ffmpeg behind.
func Start(ctx context.Context, ffmpegPath string, cmd *Command, opts RunOptions) (*Run, error) {
	if ffmpegPath == "" {
		return nil, errors.New("ffmpeg path is empty")
	}
	if cmd == nil {
		return nil, ErrNoCommand
	}
	opts = opts.withDefaults()

	runCtx, cancel := context.WithCancel(ctx)
	// Not exec.CommandContext: its cancellation is a bare SIGKILL, and ffmpeg
	// needs the chance to write the container trailer so a stopped file is not
	// corrupt. Cancel does the interrupt-then-kill dance itself.
	proc := exec.Command(ffmpegPath, cmd.Args()...)
	proc.Dir = opts.Dir

	stdout, err := proc.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reading ffmpeg's progress: %w", err)
	}
	stderr, err := proc.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reading ffmpeg's log: %w", err)
	}

	if err := proc.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	r := &Run{
		events: make(chan Event, eventBuffer),
		proc:   proc,
		cancel: cancel,
		done:   make(chan struct{}),
		kill:   opts.KillAfter,
	}
	go func() {
		<-runCtx.Done()
		// A run that ended on its own closes done before the context is
		// cancelled, so this only fires for a real stop.
		select {
		case <-r.done:
		default:
			r.stop()
		}
	}()
	go r.pump(stdout, stderr, opts)
	return r, nil
}

// Events is the stream of updates. It is closed after the Done event.
func (r *Run) Events() <-chan Event { return r.events }

// Cancel stops the process: an interrupt so ffmpeg can close the output file
// properly, then a kill if it has not gone within the grace period. It is safe
// to call more than once and from any goroutine.
func (r *Run) Cancel() {
	r.cancel()
	r.stop()
}

// stop performs the interrupt-then-kill, at most once.
func (r *Run) stop() {
	r.once.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()

		if r.proc.Process != nil {
			_ = r.proc.Process.Signal(os.Interrupt)
		}
		go func() {
			select {
			case <-r.done:
			case <-time.After(r.kill):
				if r.proc.Process != nil {
					_ = r.proc.Process.Kill()
				}
			}
		}()
	})
}

// wasStopped reports whether Cancel was used, so a non-zero exit status can be
// attributed to the user rather than to ffmpeg.
func (r *Run) wasStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// pump reads both pipes to EOF, then reaps the process and reports the result.
//
// The pipes must be drained before Wait, which exec.Cmd documents as closing
// them; hence the readers run concurrently and Wait comes last.
func (r *Run) pump(stdout, stderr io.Reader, opts RunOptions) {
	// The order of these matters. defers run last-in-first-out, so done is
	// closed *before* the context is cancelled: the watcher in Start selects on
	// done first and must not mistake a normal exit for a stop.
	defer r.cancel()
	defer close(r.events)
	defer close(r.done)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		logs []string
		last Progress
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		r.readProgress(stdout, opts, &mu, &last)
	}()
	go func() {
		defer wg.Done()
		r.readLog(stderr, opts, &mu, &logs)
	}()

	wg.Wait()
	err := r.proc.Wait()

	mu.Lock()
	result := Result{
		Err:       err,
		ExitCode:  exitCode(r.proc),
		Cancelled: r.wasStopped(),
		Progress:  last,
		Stderr:    logs,
	}
	mu.Unlock()

	// The Done event is delivered unconditionally: dropping it would leave the
	// UI waiting forever for a run that has already finished.
	r.events <- Event{Done: true, Result: result}
}

// exitCode is the process status, or -1 when there is none to report.
func exitCode(proc *exec.Cmd) int {
	if proc.ProcessState == nil {
		return -1
	}
	return proc.ProcessState.ExitCode()
}

// readProgress decodes the key=value stream and emits throttled snapshots.
func (r *Run) readProgress(stdout io.Reader, opts RunOptions, mu *sync.Mutex, last *Progress) {
	var decoder ProgressDecoder
	var lastEmit time.Time
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for scanner.Scan() {
		progress, ok := decoder.Line(scanner.Text())
		if !ok {
			continue
		}
		mu.Lock()
		*last = progress
		mu.Unlock()

		// The final block is always delivered: it is the one that says the
		// encode finished, and dropping it would leave the bar short.
		if !progress.Ended && time.Since(lastEmit) < opts.Throttle {
			continue
		}
		lastEmit = time.Now()

		snapshot := progress
		r.emit(Event{Progress: &snapshot})
	}
	_ = scanner.Err()
}

// readLog collects ffmpeg's stderr, keeping the tail for the error report and
// forwarding each line to the UI's log pane.
func (r *Run) readLog(stderr io.Reader, opts RunOptions, mu *sync.Mutex, logs *[]string) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		mu.Lock()
		*logs = append(*logs, line)
		if len(*logs) > opts.StderrLimit {
			*logs = (*logs)[len(*logs)-opts.StderrLimit:]
		}
		mu.Unlock()
		r.emit(Event{Log: line})
	}
	_ = scanner.Err()
}

// emit delivers an event, dropping it when the reader has fallen a whole buffer
// behind. Only progress and log lines go through here, and both are snapshots
// or history: losing one under pressure costs nothing.
func (r *Run) emit(ev Event) {
	select {
	case r.events <- ev:
	default:
	}
}

// Outcome is what a finished run means for the person who asked for it.
type Outcome int

const (
	// OutcomeSucceeded means the output file was written.
	OutcomeSucceeded Outcome = iota
	// OutcomeCancelled means the run was stopped before it finished.
	OutcomeCancelled
	// OutcomeRefused means ffmpeg declined to replace an existing file.
	OutcomeRefused
	// OutcomeFailed means ffmpeg reported an error, or claimed success without
	// writing a usable file.
	OutcomeFailed
)

// String names the outcome, for tests and logs.
func (o Outcome) String() string {
	switch o {
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeCancelled:
		return "cancelled"
	case OutcomeRefused:
		return "refused"
	default:
		return "failed"
	}
}

// Diagnose interprets a finished run (plan.md §6.8).
//
// The exit status alone cannot be trusted in either direction. With -n, ffmpeg
// prints "File '...' already exists. Exiting." and *then exits 0*, having written
// nothing at all, so a zero status is not proof of success; conversely a stopped
// run needs no error message. Success therefore also requires the output file to
// exist and be non-empty (DECISIONS D9).
//
// The returned hint is a plain-language explanation, or "" when there is nothing
// useful to add.
func Diagnose(res Result, outputPath string) (Outcome, string) {
	if res.Cancelled {
		return OutcomeCancelled, ""
	}

	// ffmpeg says this and exits 0, which is the trap D9 describes.
	if containsLine(res.Stderr, "already exists") {
		return OutcomeRefused, "the output file already exists; go back and choose another name"
	}

	written := false
	if info, err := os.Stat(outputPath); err == nil && !info.IsDir() && info.Size() > 0 {
		written = true
	}

	if res.Err != nil {
		return OutcomeFailed, hintFor(res)
	}
	if !written {
		// Exit status 0 with nothing on disk: the run did not do what it said.
		return OutcomeFailed, fmt.Sprintf("%s was not written; ffmpeg reported no error", outputPath)
	}
	return OutcomeSucceeded, ""
}

// hintFor maps the common ffmpeg failures onto something actionable (plan.md
// §6.8). It deliberately stays short: a wall of text is not a hint.
func hintFor(res Result) string {
	text := strings.Join(res.Stderr, "\n")
	switch {
	case strings.Contains(text, "Unknown encoder"),
		strings.Contains(text, "Encoder (codec"),
		strings.Contains(text, "not found for output stream"):
		return "this ffmpeg build does not have a codec the task asked for"
	case strings.Contains(text, "Permission denied"):
		return "ffmpeg was not allowed to write there; check the folder's permissions"
	case strings.Contains(text, "No such file or directory"):
		return "a path in the command does not exist"
	case strings.Contains(text, "Invalid argument"),
		strings.Contains(text, "Invalid duration"),
		strings.Contains(text, "out of range"):
		return "one of the values does not fit this input; check the times and sizes"
	case strings.Contains(text, "No space left on device"):
		return "the disk is full"
	}
	if mentionsHardwareEncoder(res.Stderr) {
		// The encoder is present and the name is right; what failed is the
		// device behind it (driver missing, GPU busy, no permission). ffmpeg's
		// wording for that varies per API, so the encoder's own name in the log
		// is the reliable signal (plan.md §7).
		return "the hardware encoder could not start on this machine; try the software encoder instead"
	}
	if len(res.Stderr) > 0 {
		return "ffmpeg reported: " + strings.TrimSpace(res.Stderr[len(res.Stderr)-1])
	}
	return ""
}

// mentionsHardwareEncoder reports whether the log names a hardware encoder ffwiz
// knows about. A word-split is used rather than a substring test so that
// "h264_nvenc" matches but a coincidental run of letters inside a path does not.
func mentionsHardwareEncoder(lines []string) bool {
	for _, line := range lines {
		for _, field := range strings.FieldsFunc(line, func(r rune) bool {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				return false
			case r == '_' || r == '-' || r == '.':
				return false
			default:
				return true
			}
		}) {
			if IsHardwareEncoder(field) {
				return true
			}
		}
	}
	return false
}

// containsLine reports whether any log line contains substr.
func containsLine(lines []string, substr string) bool {
	for _, line := range lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// LastLines returns at most n of the run's log lines, for an error report.
func (res Result) LastLines(n int) []string {
	if n <= 0 || len(res.Stderr) <= n {
		return res.Stderr
	}
	return res.Stderr[len(res.Stderr)-n:]
}

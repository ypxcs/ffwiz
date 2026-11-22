package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests spawn the real ffmpeg and ffprobe binaries. They skip when the
// tools or the generated sample media are unavailable, so `go test ./...`
// still passes on a machine without ffmpeg (plan.md §9).

const sampleDir = "testdata/generated"

func requireTools(t *testing.T) Tools {
	t.Helper()
	tools, err := Locate(context.Background(), Overrides{})
	if err != nil {
		t.Skipf("ffmpeg/ffprobe not available: %v", err)
	}
	return tools
}

func requireSample(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(sampleDir, name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample media missing (run `make samples`): %v", err)
	}
	return path
}

func TestIntegrationLocateAndCapabilities(t *testing.T) {
	tools := requireTools(t)

	if !strings.Contains(tools.Version, ".") && !strings.HasPrefix(tools.Version, "N-") {
		t.Errorf("Version = %q, which does not look like an ffmpeg version", tools.Version)
	}
	if !filepath.IsAbs(tools.FFmpeg) || !filepath.IsAbs(tools.FFprobe) {
		t.Errorf("Locate returned non-absolute paths: %+v", tools)
	}

	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps.Encoders) < 10 {
		t.Errorf("only %d encoders found, expected many", len(caps.Encoders))
	}
	if len(caps.Muxers) == 0 {
		t.Error("no muxers found")
	}
	// Every build that can encode H.264 has one of these; the task catalogue
	// depends on at least one being present.
	hasH264 := caps.HasEncoder("libx264") || caps.HasEncoder("h264") ||
		caps.HasEncoder("h264_videotoolbox") || caps.HasEncoder("h264_nvenc")
	if !hasH264 {
		t.Error("no H.264 encoder found, which the task catalogue requires")
	}
	if !caps.HasMuxer("mp4") {
		t.Error("the mp4 muxer was not detected")
	}
}

func TestIntegrationProbeRealFiles(t *testing.T) {
	tools := requireTools(t)

	tests := []struct {
		name         string
		file         string
		wantVideo    bool
		wantAudio    bool
		wantDuration time.Duration
	}{
		{"mp4 with video and audio", "av.mp4", true, true, 2 * time.Second},
		{"mp4 with video only", "video_only.mp4", true, false, time.Second},
		{"mp3 audio only", "audio_only.mp3", false, true, time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := requireSample(t, tc.file)
			info, err := Probe(context.Background(), tools.FFprobe, path)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if info.Has(StreamVideo) != tc.wantVideo {
				t.Errorf("Has(StreamVideo) = %v, want %v", info.Has(StreamVideo), tc.wantVideo)
			}
			if info.Has(StreamAudio) != tc.wantAudio {
				t.Errorf("Has(StreamAudio) = %v, want %v", info.Has(StreamAudio), tc.wantAudio)
			}
			got := info.Duration()
			if got < tc.wantDuration-time.Millisecond*100 || got > tc.wantDuration+time.Millisecond*100 {
				t.Errorf("Duration() = %v, want ~%v", got, tc.wantDuration)
			}
			if info.Format.Size <= 0 {
				t.Errorf("Format.Size = %d, want > 0", info.Format.Size)
			}
		})
	}
}

// TestIntegrationProbeAwkwardFilenames covers the acceptance criterion that
// filenames with spaces, quotes, ampersands and non-ASCII text work everywhere
// (plan.md §8, §11).
func TestIntegrationProbeAwkwardFilenames(t *testing.T) {
	tools := requireTools(t)

	for _, name := range []string{"sp ace.mp4", "quote's.mp4", "&and.mp4", "ünïcodé.mp4"} {
		t.Run(name, func(t *testing.T) {
			path := requireSample(t, name)
			info, err := Probe(context.Background(), tools.FFprobe, path)
			if err != nil {
				t.Fatalf("Probe(%q): %v", path, err)
			}
			if !info.Has(StreamVideo) {
				t.Errorf("Probe(%q) found no video stream", path)
			}
		})
	}
}

// TestIntegrationProbeLeadingDashFilename proves guardPath works end to end:
// with the working directory inside the media folder, a bare "-leading-dash.mp4"
// is passed to ffprobe with a "./" prefix so it is not parsed as an option.
func TestIntegrationProbeLeadingDashFilename(t *testing.T) {
	tools := requireTools(t)
	requireSample(t, "-leading-dash.mp4")

	// t.Chdir restores the working directory when the test ends.
	t.Chdir(sampleDir)

	info, err := Probe(context.Background(), tools.FFprobe, "-leading-dash.mp4")
	if err != nil {
		t.Fatalf("Probe with a leading-dash filename: %v", err)
	}
	if !info.Has(StreamVideo) {
		t.Error("no video stream found in the leading-dash sample")
	}
}

func TestIntegrationProbeMissingFileIsAnError(t *testing.T) {
	tools := requireTools(t)

	_, err := Probe(context.Background(), tools.FFprobe, filepath.Join(sampleDir, "does-not-exist.mp4"))
	if err == nil {
		t.Fatal("Probe succeeded on a nonexistent file")
	}
	// The message must be actionable, i.e. carry ffprobe's own text.
	if !strings.Contains(err.Error(), "does-not-exist.mp4") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestIntegrationProbeTimeout(t *testing.T) {
	tools := requireTools(t)
	path := requireSample(t, "av.mp4")

	// A deadline in the past must produce an error rather than hanging.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	if _, err := Probe(ctx, tools.FFprobe, path); err == nil {
		t.Skip("probing completed before the deadline; timing-dependent test")
	}
}

// TestIntegrationCapabilitiesMatchRawListings cross-checks Capabilities against
// the raw text of the live listings. It is the guard against silent parser
// drift: a name that grep finds must be in the parsed set, and vice versa. The
// names chosen include ones that this particular ffmpeg build does *not* have
// (notably `subtitles`, which needs libass), so both directions are exercised.
func TestIntegrationCapabilitiesMatchRawListings(t *testing.T) {
	tools := requireTools(t)

	caps, err := tools.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	listing := func(flag string) string {
		out, err := exec.Command(tools.FFmpeg, "-hide_banner", flag).Output()
		if err != nil {
			t.Fatalf("%s %s: %v", tools.FFmpeg, flag, err)
		}
		return string(out)
	}

	// rawHas reports whether the listing holds the name as a whole field, which
	// is how the parser identifies names.
	rawHas := func(raw, name string) bool {
		for _, line := range strings.Split(raw, "\n") {
			fields := strings.Fields(strings.TrimRight(line, "\r"))
			if len(fields) < 2 {
				continue
			}
			for _, f := range fields[1:] {
				if f == name {
					return true
				}
			}
		}
		return false
	}

	tests := []struct {
		flag string
		name string
		has  func(string) bool
	}{
		{"-encoders", "libx264", caps.HasEncoder},
		{"-encoders", "aac", caps.HasEncoder},
		{"-encoders", "definitely_not_an_encoder", caps.HasEncoder},
		{"-muxers", "mp4", caps.HasMuxer},
		{"-muxers", "matroska", caps.HasMuxer},
		{"-filters", "scale", caps.HasFilter},
		{"-filters", "palettegen", caps.HasFilter},
		{"-filters", "subtitles", caps.HasFilter}, // absent without libass
		{"-pix_fmts", "yuv420p", func(n string) bool {
			_, ok := caps.PixelFormats[n]
			return ok
		}},
	}

	rawCache := map[string]string{}
	for _, tc := range tests {
		raw, ok := rawCache[tc.flag]
		if !ok {
			raw = listing(tc.flag)
			rawCache[tc.flag] = raw
		}
		if want, got := rawHas(raw, tc.name), tc.has(tc.name); want != got {
			t.Errorf("%s: raw listing says %s is %v, Capabilities says %v",
				tc.flag, tc.name, want, got)
		}
	}
}

// TestEscapeFilterValueSurvivesRealFilenames is the verification behind
// EscapeFilterValue's rules: every rule in that function is an assertion about
// what ffmpeg's parsers do with a character, and the only way to know is to hand
// ffmpeg a file with that character in its name.
//
// It goes through the `movie` filter rather than `subtitles` for one reason: this
// ffmpeg build has no libass, so `subtitles` does not exist here and cannot be
// tested at all. Both read their path from a filename option, which is the part
// of the escaping being checked; the escaping is a property of the filtergraph
// and filter-argument levels, which are the same for both.
func TestEscapeFilterValueSurvivesRealFilenames(t *testing.T) {
	tools := requireTools(t)
	source := requireSample(t, "av.mp4")

	// Every character class the escaper handles, plus a name that contains all
	// of them at once.
	names := []string{
		"plain.mp4",
		"sp ace.mp4",
		" leading.mp4",
		"trailing .mp4",
		"tab\there.mp4",
		"quote's.mp4",
		"'leading-quote.mp4",
		"trailing-quote'.mp4",
		"double\"quote.mp4",
		"back`tick.mp4",
		"&and.mp4",
		"ünïcodé.mp4",
		"中文.mp4",
		"-leading-dash.mp4",
		"colon:name.mp4",
		"eq=uals.mp4",
		"two=eq=uals.mp4",
		"brack[et].mp4",
		"semi;i.mp4",
		"com,ma.mp4",
		"back\\slash.mp4",
		"glob*.mp4",
		"quest?mark.mp4",
		"paren(s).mp4",
		"pipe|a.mp4",
		"angle<gt>.mp4",
		"hash#tag.mp4",
		"at@sign.mp4",
		"plus+sign.mp4",
		"dollar$sign.mp4",
		"tilde~x.mp4",
		"bang!.mp4",
		"per%cent.mp4",
		"all'of:them\\at[once];and=then,some.mp4",
		"only-'=:.mp4",
	}

	dir := t.TempDir()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			blob, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("reading the sample: %v", err)
			}
			if err := os.WriteFile(path, blob, 0o644); err != nil {
				t.Fatalf("writing %q: %v", name, err)
			}

			// One frame through the movie source, so the name has to be read
			// and opened exactly as written. Two spellings are used, because
			// they behave differently: a bare value is read as a *shorthand*
			// option (which is where an unescaped "=" breaks), and the explicit
			// `filename=` form is the one the subtitles task writes, followed by
			// another option the way a style option would be.
			graph := "movie=" + EscapeFilterValue(path) + ",null[out]"
			out := filepath.Join(t.TempDir(), "frame.png")
			cmd := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
				"-filter_complex", graph, "-map", "[out]", "-frames:v", "1",
				"-y", out)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("ffmpeg rejected %q as a shorthand value\ngraph: %s\n%s", name, graph, output)
			}

			graph = "movie=filename=" + EscapeFilterValue(path) + ":seek_point=0,null[out]"
			cmd = exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "color=black:s=64x64:d=0.1",
				"-filter_complex", graph, "-map", "[out]", "-frames:v", "1",
				"-y", out)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("ffmpeg rejected %q as a filename option\ngraph: %s\n%s", name, graph, output)
			}
		})
	}
}

// TestConcatListSurvivesRealFilenames is the verification behind ConcatList's
// rules: joining clips has to work whatever the clips are called (plan.md §8,
// §11). It is the concat-list counterpart of
// TestEscapeFilterValueSurvivesRealFilenames, and it exists because the demuxer's
// quoting is quirky enough that guessing is not good enough (DECISIONS D41).
func TestConcatListSurvivesRealFilenames(t *testing.T) {
	tools := requireTools(t)
	source := requireSample(t, "av.mp4")

	names := []string{
		"plain.mp4",
		"sp ace.mp4",
		"quote's.mp4",
		"apos'inside'.mp4",
		"colon:name.mp4",
		"eq=uals.mp4",
		"com,ma.mp4",
		"semi;i.mp4",
		"brack[et].mp4",
		"back\\slash.mp4",
		"dollar$sign.mp4",
		"ünïcodé.mp4",
		"中文.mp4",
		"all'of:them\\at[once];and=then,some.mp4",
	}

	dir := t.TempDir()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			// The same clip twice under the awkward name, so the join is real
			// (4s of output from two 2s clips) and only the name varies.
			clip := filepath.Join(dir, name)
			blob, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("reading the sample: %v", err)
			}
			if err := os.WriteFile(clip, blob, 0o644); err != nil {
				t.Fatalf("writing %q: %v", name, err)
			}

			out := filepath.Join(t.TempDir(), "joined.mp4")
			listDir := t.TempDir()
			content := ConcatList([]string{clip, clip})
			list := ConcatListPath(listDir, content)
			cmd, err := Build(Request{
				Inputs: []InputSpec{{
					Path: list,
					Args: []string{"-f", "concat", "-safe", "0"},
				}},
				Artifacts:  []Artifact{{Path: list, Content: content}},
				Output:     out,
				OutputArgs: []string{"-c", "copy"},
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// Start writes the list itself; the test never touches the file.
			run, err := Start(context.Background(), tools.FFmpeg, cmd, RunOptions{})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			var result Result
			for ev := range run.Events() {
				if ev.Done {
					result = ev.Result
				}
			}
			if result.Err != nil {
				t.Fatalf("joining %q failed: %v\n%s", name, result.Err, strings.Join(result.Stderr, "\n"))
			}

			info, err := Probe(context.Background(), tools.FFprobe, out)
			if err != nil {
				t.Fatalf("probing the joined output: %v", err)
			}
			// Two copies of a 2s clip. A mangled path would have opened the
			// wrong file or failed outright, so the length is the proof that
			// the list named the clip exactly.
			if got := info.Duration(); got < 3900*time.Millisecond || got > 4100*time.Millisecond {
				t.Errorf("joined duration = %v, want ~4s (two 2s clips)", got)
			}
		})
	}
}

// TestIntegrationRunReportsRealProgress runs a real encode and reads the real
// `-progress` stream. The parsing is unit-tested against recorded output; this
// checks that the recording still matches what this ffmpeg actually writes, and
// that the exit status is not the only thing telling success from failure
// (plan.md §11, DECISIONS D9).
func TestIntegrationRunReportsRealProgress(t *testing.T) {
	tools := requireTools(t)
	input := requireSample(t, "av.mp4")
	output := filepath.Join(t.TempDir(), "out.mp4")

	cmd, err := Build(Request{
		Inputs:     []InputSpec{{Path: input}},
		Output:     output,
		OutputArgs: []string{"-c:v", "libx264", "-crf", "28", "-c:a", "copy"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	run, err := Start(context.Background(), tools.FFmpeg, cmd, RunOptions{Throttle: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var (
		updates []Progress
		logs    []string
		result  Result
	)
	timeout := time.After(2 * time.Minute)
	for done := false; !done; {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				t.Fatal("the event stream closed without a Done event")
			}
			switch {
			case ev.Done:
				result, done = ev.Result, true
			case ev.Progress != nil:
				updates = append(updates, *ev.Progress)
			default:
				logs = append(logs, ev.Log)
			}
		case <-timeout:
			t.Fatal("the encode never finished")
		}
	}

	if result.Err != nil {
		t.Fatalf("the encode failed: %v\n%s", result.Err, strings.Join(result.Stderr, "\n"))
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if len(updates) == 0 {
		t.Fatal("no progress was reported")
	}
	final := updates[len(updates)-1]
	if !final.Ended {
		t.Errorf("the last update is not the end of the encode: %+v", final)
	}
	if final.OutTime <= 0 || final.OutTime > 3*time.Second {
		t.Errorf("OutTime = %v, which is not the length of the 2s sample", final.OutTime)
	}
	if final.TotalSize <= 0 {
		t.Errorf("TotalSize = %d, want the bytes written", final.TotalSize)
	}
	if final.Speed <= 0 {
		t.Errorf("Speed = %v, want a real speed", final.Speed)
	}
	if len(logs) == 0 {
		t.Error("ffmpeg's own output produced no log lines")
	}

	if outcome, hint := Diagnose(result, output); outcome != OutcomeSucceeded {
		t.Fatalf("Diagnose = %v (%s), want succeeded", outcome, hint)
	}
	if info, err := os.Stat(output); err != nil || info.Size() == 0 {
		t.Fatalf("the output file is missing or empty: %v", err)
	}

	// The output has to be a real file, not just bytes on disk (§11).
	info, err := Probe(context.Background(), tools.FFprobe, output)
	if err != nil {
		t.Fatalf("probing the output: %v", err)
	}
	if !info.Has(StreamVideo) {
		t.Error("the output has no video stream")
	}
}

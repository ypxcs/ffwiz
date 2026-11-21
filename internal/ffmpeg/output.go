package ffmpeg

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// An output can be one file or a numbered sequence. ffmpeg's image2 muxer
// writes a frame per file and requires a printf-style number in the name
// ("frames.%04d.png"), which means the rest of the program cannot treat "the
// output" as a single path: existence, size, deletion and reveal all have to ask
// what the name actually wrote (DECISIONS D34).
//
// The measured behaviour that makes this necessary: with image2, -n does *not*
// protect existing frames. ffmpeg overwrites them, prints nothing and exits 0,
// so the wizard has to notice the files itself rather than rely on ffmpeg's
// refusal (contrast DECISIONS D9, where -n does refuse for a single file).

// sequenceNumberRe matches the frame-number placeholder in an output name,
// capturing the width ("04" in "%04d") when one is given.
var sequenceNumberRe = regexp.MustCompile(`%([0-9]*)d`)

// IsSequence reports whether path asks ffmpeg for a numbered sequence rather
// than a single file (plan.md §5.11).
func IsSequence(path string) bool {
	return sequenceNumberRe.MatchString(path)
}

// SequenceNameRe builds the pattern a sequence's file names must match, with the
// frame number as the only wildcard. It is a regular expression rather than a
// filepath.Glob pattern because escaping is not portable: on Windows a backslash
// separates directories, so a literal "*" in a file name could not be escaped.
func SequenceNameRe(base string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range sequenceNumberRe.FindAllStringSubmatchIndex(base, -1) {
		b.WriteString(regexp.QuoteMeta(base[last:loc[0]]))
		width := ""
		if loc[2] >= 0 {
			width = base[loc[2]:loc[3]]
		}
		if n, err := strconv.Atoi(width); err == nil && n > 0 {
			// The width is a minimum, not a maximum: ffmpeg keeps going past it
			// rather than repeating numbers, so at least this many digits.
			b.WriteString(`[0-9]{` + strconv.Itoa(n) + `,}`)
		} else {
			b.WriteString(`[0-9]+`)
		}
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(base[last:]))
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// OutputFiles lists the files on disk that path names, sorted: the file itself
// for an ordinary path, or every frame already written for a sequence. It is the
// single place the rest of the program asks "is the output there?", so a
// sequence behaves like a file everywhere else.
func OutputFiles(path string) []string {
	if path == "" {
		return nil
	}
	if !IsSequence(path) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return []string{path}
		}
		return nil
	}

	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	name := SequenceNameRe(base)
	var files []string
	for _, e := range entries {
		if e.IsDir() || !name.MatchString(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files
}

// outputWritten reports whether the output of a finished run is really on disk
// with something in it. An empty file counts as nothing: ffmpeg creates the
// output before it has written a single frame, so a zero-length file is evidence
// of a failure rather than of success (DECISIONS D9).
func outputWritten(path string) bool {
	for _, f := range OutputFiles(path) {
		if info, err := os.Stat(f); err == nil && !info.IsDir() && info.Size() > 0 {
			return true
		}
	}
	return false
}

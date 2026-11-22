package tasks

import (
	"fmt"
	"strings"
)

// ListSeparator separates the entries of a file-list answer.
//
// A newline is the one separator a path cannot contain *and* that the concat
// list can express: ffmpeg's demuxer reads one `file '...'` line per clip, so a
// path with a newline in it could not be written into a list at all
// (DECISIONS D41). Using it means a file-list answer needs no escaping.
const ListSeparator = "\n"

// MinConcatFiles is the fewest clips a concat step can join. Joining one file
// with itself is not a thing anybody means to ask for, and a step that can be
// left with an empty list would produce a command with no input.
const MinConcatFiles = 2

// FileList joins paths into a step answer.
func FileList(paths []string) string {
	return strings.Join(paths, ListSeparator)
}

// ParseFileList splits a step answer back into paths, dropping empty entries so
// that a trailing separator does not become a blank file name.
//
// Entries are *not* trimmed: a leading or trailing space is part of a file name,
// and a path that has been through the picker must come back unchanged.
func ParseFileList(answer string) []string {
	var paths []string
	for _, line := range strings.Split(answer, ListSeparator) {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// CheckConcatAnswer validates the clips chosen for a concat step.
//
// The wizard needs the answer to be usable before it moves on: the engine can
// build a one-line list, but only the UI knows that the user is still choosing,
// and a task that advanced with one clip would quietly produce a command that
// copies one file to another (DECISIONS D41).
func (s Step) CheckConcatAnswer(answer string) error {
	if strings.TrimSpace(answer) == "" {
		if s.RequiresValue() {
			return ErrValueRequired
		}
		return nil
	}
	paths := ParseFileList(answer)
	if len(paths) < MinConcatFiles {
		return fmt.Errorf("choose at least %d clips to join (%d chosen)", MinConcatFiles, len(paths))
	}
	for _, p := range paths {
		if strings.ContainsRune(p, '\r') {
			// A list split on CRLF leaves the carriage return on the end of the
			// name, and ffmpeg then reports an input it cannot open.
			return fmt.Errorf("%q has a carriage return in it", p)
		}
	}
	return nil
}

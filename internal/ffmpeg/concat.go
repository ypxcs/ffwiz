package ffmpeg

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// concatListExt is the extension of the generated file list. Nothing reads it
// back in, but a user who goes looking for the file ffmpeg is reading should
// find something that says what it is.
const concatListExt = ".txt"

// ConcatList renders paths as the file list the concat demuxer reads
// (plan.md §5.9).
//
// The format was measured against ffmpeg 8.1.1 rather than assumed
// (DECISIONS D41): the demuxer's parser takes `file '<path>'` and understands
// exactly one escape inside it - a backslash immediately before an apostrophe,
// which is how a quote in the middle of a name is written. Everything else is
// literal, so a backslash, a space, a comma or a colon needs nothing. The two
// plausible alternatives both fail: `file <path>` breaks on a space, an
// apostrophe and a backslash, and `file "<path>"` does not work at all because
// ffmpeg keeps the double quotes as part of the name.
//
// A path containing a newline cannot be expressed in this format at all, which
// is why a file-list answer cannot contain one.
func ConcatList(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(p, "'", `'\''`))
		b.WriteString("'\n")
	}
	return b.String()
}

// ConcatListPath names the list file that holds content, inside dir.
//
// The name comes from the content, so the same clips always produce the same
// path. That matters for two reasons: the preview is rebuilt on every keystroke
// and a name that changed each time would rewrite the file being shown, and a
// copied command stays valid while the session lives.
func ConcatListPath(dir, content string) string {
	sum := sha256.Sum256([]byte(content))
	return filepath.Join(dir, "concat-"+hex.EncodeToString(sum[:6])+concatListExt)
}

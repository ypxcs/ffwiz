package ffmpeg

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConcatListRendersOneLinePerClip pins the format of the file the concat
// demuxer reads. The format is not documented in one place and its parser is
// fussy, so the rules here were measured against ffmpeg 8.1.1 rather than
// assumed (DECISIONS D41):
//
//   - an entry is `file '<path>'`, not `file <path>` and not `file "<path>"`;
//   - a quote inside a name is written with a backslash in front of it, which is
//     the only escape the parser understands (`don't.mp4` -> `don\'t.mp4`);
//   - nothing else needs escaping: a space, a backslash and a comma are literal.
func TestConcatListRendersOneLinePerClip(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{
			name:  "one clip",
			paths: []string{"/films/a.mp4"},
			want:  "file '/films/a.mp4'\n",
		},
		{
			name:  "order is preserved",
			paths: []string{"/films/second.mp4", "/films/first.mp4"},
			want:  "file '/films/second.mp4'\nfile '/films/first.mp4'\n",
		},
		{
			// A space, a comma, a colon and a backslash are all literal to the
			// demuxer, so a path with them is written exactly as it is.
			name:  "awkward but unescaped characters are literal",
			paths: []string{"/films/part one, two:three\\four.mp4"},
			want:  "file '/films/part one, two:three\\four.mp4'\n",
		},
		{
			// An apostrophe is the one character that has to be escaped, and
			// the escaping is the shell's, not ffmpeg's: close the quote, emit
			// an escaped quote, reopen it.
			name:  "an apostrophe is closed and reopened",
			paths: []string{"/films/My Movie's clip.mp4"},
			want:  "file '/films/My Movie'\\''s clip.mp4'\n",
		},
		{
			name:  "every apostrophe is escaped",
			paths: []string{"a'b'c.mp4"},
			want:  "file 'a'\\''b'\\''c.mp4'\n",
		},
		{
			name:  "no clips is an empty file",
			paths: nil,
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConcatList(tc.paths); got != tc.want {
				t.Errorf("ConcatList(%q) =\n%q\nwant\n%q", tc.paths, got, tc.want)
			}
		})
	}
}

// TestConcatListPathIsStable: the list name comes from the list's content so the
// same clips always write the same file. The preview is rebuilt on every
// keystroke, and a name that moved each time would rewrite the file being shown.
func TestConcatListPathIsStable(t *testing.T) {
	dir := "/tmp/ffwiz"
	content := ConcatList([]string{"a.mp4", "b.mp4"})

	first := ConcatListPath(dir, content)
	if second := ConcatListPath(dir, content); first != second {
		t.Errorf("the same content gave two paths: %q and %q", first, second)
	}
	if got := filepath.Dir(first); got != dir {
		t.Errorf("the list is in %q, want it in %q", got, dir)
	}
	if !strings.HasSuffix(first, ".txt") {
		t.Errorf("%q does not look like a text file", first)
	}
	if !strings.HasPrefix(filepath.Base(first), "concat-") {
		t.Errorf("%q does not say what it is", first)
	}

	other := ConcatListPath(dir, ConcatList([]string{"a.mp4", "c.mp4"}))
	if other == first {
		t.Errorf("different clips share the path %q, so one would overwrite the other", first)
	}
}

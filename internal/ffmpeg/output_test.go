package ffmpeg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSequence(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"out.mp4", false},
		{"out.png", false},
		{"100%d.png", true},
		{"out.%d.png", true},
		{"out.%04d.png", true},
		{"out.%3d.jpg", true},
		{"frames_video.%04d.png", true},
		// A percent that is not a number placeholder is a literal.
		{"100%real.png", false},
		{"a%sd.png", false},
	}
	for _, tc := range tests {
		if got := IsSequence(tc.path); got != tc.want {
			t.Errorf("IsSequence(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestSequenceNameRe(t *testing.T) {
	tests := []struct {
		base  string
		match string
		no    string
	}{
		{"frames.%04d.png", "frames.0001.png", "frames.001.png"},
		{"frames.%d.png", "frames.17.png", "frames.png"},
		{"frames.%04d.png", "frames.10001.png", "frames.1.png"},
		// The literal part must not act as a wildcard, however glob-like it is.
		{"a[b]*c.%d.png", "a[b]*c.7.png", "axbyc.7.png"},
	}
	for _, tc := range tests {
		re := SequenceNameRe(tc.base)
		if !re.MatchString(tc.match) {
			t.Errorf("SequenceNameRe(%q) does not match %q", tc.base, tc.match)
		}
		if re.MatchString(tc.no) {
			t.Errorf("SequenceNameRe(%q) wrongly matches %q", tc.base, tc.no)
		}
	}
}

func TestOutputFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name string, data string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	one := mustWrite("one.mp4", "x")
	mustWrite("frames.0001.png", "a")
	mustWrite("frames.0002.png", "b")
	mustWrite("frames.0003.png", "c")
	// Not part of the sequence: wrong width, wrong stem, and a folder.
	mustWrite("frames.1.png", "d")
	mustWrite("other.0001.png", "e")
	if err := os.Mkdir(filepath.Join(dir, "frames.0004.png"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("a plain path is itself", func(t *testing.T) {
		got := OutputFiles(one)
		if len(got) != 1 || got[0] != one {
			t.Errorf("OutputFiles(%q) = %v, want just itself", one, got)
		}
	})

	t.Run("a missing path is nothing", func(t *testing.T) {
		if got := OutputFiles(filepath.Join(dir, "nope.mp4")); len(got) != 0 {
			t.Errorf("OutputFiles of a missing file = %v, want nothing", got)
		}
	})

	t.Run("an empty path is nothing", func(t *testing.T) {
		if got := OutputFiles(""); len(got) != 0 {
			t.Errorf("OutputFiles(\"\") = %v, want nothing", got)
		}
	})

	t.Run("only this sequence's frames, in order", func(t *testing.T) {
		got := OutputFiles(filepath.Join(dir, "frames.%04d.png"))
		var names []string
		for _, f := range got {
			names = append(names, filepath.Base(f))
		}
		want := "frames.0001.png frames.0002.png frames.0003.png"
		if strings.Join(names, " ") != want {
			t.Errorf("OutputFiles = %v, want %s", names, want)
		}
	})

	t.Run("a directory is not a frame", func(t *testing.T) {
		got := OutputFiles(filepath.Join(dir, "frames.0004.png"))
		if len(got) != 0 {
			t.Errorf("a folder is reported as output: %v", got)
		}
	})

	t.Run("a sequence that has not been written is nothing", func(t *testing.T) {
		if got := OutputFiles(filepath.Join(dir, "later.%04d.png")); len(got) != 0 {
			t.Errorf("OutputFiles = %v, want nothing", got)
		}
	})

	t.Run("a sequence in a missing folder is nothing", func(t *testing.T) {
		got := OutputFiles(filepath.Join(dir, "gone", "frames.%04d.png"))
		if len(got) != 0 {
			t.Errorf("OutputFiles = %v, want nothing", got)
		}
	})
}

func TestOutputWrittenIgnoresEmptyFrames(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "e.%04d.png")
	if err := os.WriteFile(filepath.Join(dir, "e.0001.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if outputWritten(empty) {
		t.Error("a sequence of empty files counts as written")
	}
	if err := os.WriteFile(filepath.Join(dir, "e.0002.png"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !outputWritten(empty) {
		t.Error("a sequence with one real frame does not count as written")
	}
}

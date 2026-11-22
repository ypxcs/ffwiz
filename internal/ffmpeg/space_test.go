//go:build unix

package ffmpeg

import "testing"

// TestFreeSpaceAnswersForATemporaryDirectory: the call has to reach the
// filesystem the directory is really on, and the answer has to be a number a
// caller can compare with an expected output size (plan.md §8).
func TestFreeSpaceAnswersForATemporaryDirectory(t *testing.T) {
	dir := t.TempDir()

	free, err := FreeSpace(dir)
	if err != nil {
		t.Fatalf("FreeSpace(%s): %v", dir, err)
	}
	// A machine running tests has some space in its temporary directory; zero
	// would mean the field was read from the wrong place.
	if free <= 0 {
		t.Errorf("FreeSpace(%s) = %d, want a positive number", dir, free)
	}
}

// TestFreeSpaceReportsAMissingDirectory: an error rather than a made-up number,
// so the caller can leave the warning out instead of printing a guess.
func TestFreeSpaceReportsAMissingDirectory(t *testing.T) {
	if _, err := FreeSpace("/no/such/directory/ffwiz"); err == nil {
		t.Error("FreeSpace on a missing directory succeeded, want an error")
	}
}

//go:build !unix

package ffmpeg

import "errors"

// FreeSpace is not implemented here. plan.md §8 calls the disk-space check "best
// effort", and a number that might be wrong is worse than an error the caller
// can treat as "unknown" - a warning built on a wrong figure teaches the user to
// ignore warnings.
func FreeSpace(dir string) (int64, error) {
	return 0, errors.ErrUnsupported
}

//go:build unix

package ffmpeg

import "syscall"

// FreeSpace reports how many bytes an ordinary process can still write on the
// filesystem holding dir. It is what plan.md §8's disk-space warning is built
// from, and it is deliberately the *available* figure rather than the free one:
// a filesystem keeps a slice for root, and warning on space the user cannot
// actually write to would be worse than saying nothing.
//
// dir does not have to exist for every platform to answer, but passing the
// folder the output goes in is the only way to ask about the right filesystem.
func FreeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	// The field types differ between platforms (Bsize is uint32 on Darwin and
	// int64 on Linux), so both sides are widened before the multiply.
	return int64(st.Bavail) * int64(st.Bsize), nil
}

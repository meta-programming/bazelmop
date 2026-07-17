//go:build darwin

package dedupe

import (
	"golang.org/x/sys/unix"
)

// cloneFile performs a Copy-on-Write reflink clone of src to dst on macOS using clonefile(2).
func cloneFile(src, dst string) error {
	// 0 specifies default clonefile flags (e.g. follow symlinks if any, copy-on-write clone)
	return unix.Clonefile(src, dst, 0)
}

//go:build darwin

package dedupe

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile performs a Copy-on-Write reflink clone of src to dst on macOS using clonefile(2).
//
// mode is the mode of the file dst is going to replace, and is applied after
// the clone. clonefile(2) copies the source's metadata rather than the
// target's, so without this the replaced file silently takes the permissions
// of whichever copy happened to be chosen as the source. That is usually the
// same and occasionally not, and the occasional case strips the executable bit
// from a binary that every workspace sharing it then cannot run.
func cloneFile(src, dst string, mode os.FileMode) error {
	// 0 specifies default clonefile flags (e.g. follow symlinks if any, copy-on-write clone)
	if err := unix.Clonefile(src, dst, 0); err != nil {
		return err
	}
	if err := os.Chmod(dst, mode.Perm()); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

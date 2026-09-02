//go:build linux

package dedupe

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile performs a Copy-on-Write reflink clone of src to dst on Linux,
// giving dst the mode of the file it is going to replace.
//
// mode matters and is not cosmetic. A reflink clone is a new inode, so unlike
// os.Link it inherits nothing from what it replaces; created with a fixed 0644
// it silently strips the executable bit from every deduplicated binary. That
// turns compilers, plugins and toolchain binaries into unrunnable files across
// every workspace sharing them, and the build error names a missing interpreter
// or permission rather than the deduplicator that caused it.
//
// The mode is applied with Fchmod on the open descriptor rather than a later
// os.Chmod on the path, so nothing can observe the file at the wrong mode and
// no second failure path can leave it there.
func cloneFile(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Open destination for writing, creating it if not exist, and truncating it.
	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// O_CREATE masks the requested permissions with the process umask, which
	// would drop the group and other execute bits a toolchain binary carries.
	// Fchmod is not masked, so it restores exactly what the target had.
	if err := unix.Fchmod(int(dstFile.Fd()), uint32(mode.Perm())); err != nil {
		_ = os.Remove(dst)
		return err
	}

	// Invoke the FICLONE ioctl system call
	err = unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

//go:build linux

package dedupe

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile performs a Copy-on-Write reflink clone of src to dst on Linux.
func cloneFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Open destination for writing, creating it if not exist, and truncating it.
	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// Invoke the FICLONE ioctl system call
	return unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
}

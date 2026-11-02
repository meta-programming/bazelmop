package bazelcas

import (
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveAll deletes path and everything under it, like [os.RemoveAll].
//
// Bazel marks many files and directories read-only. If the first attempt
// fails, RemoveAll makes the tree user-writable and tries again.
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		_ = os.Chmod(p, 0o700)
		return nil
	})
	return os.RemoveAll(path)
}

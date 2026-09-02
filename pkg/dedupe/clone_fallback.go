//go:build !linux && !darwin

package dedupe

import (
	"errors"
	"os"
)

// cloneFile returns an error for unsupported platforms.
func cloneFile(src, dst string, mode os.FileMode) error {
	return errors.New("reflink cloning is not supported on this operating system")
}

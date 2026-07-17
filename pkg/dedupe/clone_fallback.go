//go:build !linux && !darwin

package dedupe

import "errors"

// cloneFile returns an error for unsupported platforms.
func cloneFile(src, dst string) error {
	return errors.New("reflink cloning is not supported on this operating system")
}

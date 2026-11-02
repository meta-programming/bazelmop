//go:build !linux

package dedupe

// extentFingerprint is unavailable without FIEMAP, so every file is hashed.
func extentFingerprint(path string) (fp [extentFingerprintSize]byte, ok bool) {
	return fp, false
}

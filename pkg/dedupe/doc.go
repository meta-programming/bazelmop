// Package dedupe provides filesystem walk, byte-by-byte matching,
// and atomic file link replacement capabilities for Bazel caches.
//
// It is designed to run safely concurrent with active Bazel builds by
// executing all links atomically.
package dedupe

//go:build !linux

package dedupe

import "os"

// adviseSequential is a no-op where posix_fadvise is unavailable.
func adviseSequential(f *os.File) {}

// dropPageCache is a no-op where posix_fadvise is unavailable.
//
// Hashing still works; the process simply leaves its reads in the page cache
// the way it always did.
func dropPageCache(f *os.File) {}

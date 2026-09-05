//go:build linux

package dedupe

import (
	"os"

	"golang.org/x/sys/unix"
)

// adviseSequential tells the kernel the file is about to be read start to end.
//
// The readahead window is widened, which is what a hash of the whole file
// wants, and it costs nothing when the advice is ignored.
func adviseSequential(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_SEQUENTIAL)
}

// dropPageCache asks the kernel to evict this file's pages from the page cache.
//
// Without it a full pass over the cache reads every candidate file into the
// page cache and leaves it there. The pages are clean and reclaimable, so this
// is not a leak, but they are charged to the calling process's cgroup and the
// kernel must reclaim continuously to satisfy new allocations. That reclaim is
// exactly what PSI reports as memory pressure, and on a systemd-oomd system
// sustained pressure above the configured limit gets the whole user session
// killed. Dropping each file after hashing bounds the cache footprint to the
// files currently open rather than letting it grow to fill memory.
//
// A file another process has open stays cached, so this does not evict pages a
// running build is using.
//
// See [posix_fadvise] for the underlying call.
//
// [posix_fadvise]: https://man7.org/linux/man-pages/man2/posix_fadvise.2.html
func dropPageCache(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

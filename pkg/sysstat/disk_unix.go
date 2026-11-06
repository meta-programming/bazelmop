//go:build unix

package sysstat

import "golang.org/x/sys/unix"

// Disk is the size of a filesystem and the space left on it.
type Disk struct {
	Total, Free int64
}

// DiskUsage reports the filesystem holding path. Free is the space an
// unprivileged user can still write.
func DiskUsage(path string) (Disk, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Disk{}, err
	}
	bs := int64(st.Bsize)
	return Disk{Total: int64(st.Blocks) * bs, Free: int64(st.Bavail) * bs}, nil
}

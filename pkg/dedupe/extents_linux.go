//go:build linux

package dedupe

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fsIocFiemap is FS_IOC_FIEMAP, _IOWR('f', 11, struct fiemap).
const fsIocFiemap = 0xC020660B

// Extent flags from linux/fiemap.h.
const (
	fiemapExtentLast = 0x00000001
	// An extent with any of these flags does not describe settled bytes at a
	// fixed place on disk, so two files cannot be proven equal from it.
	fiemapExtentUnsettled = 0x00000002 | // UNKNOWN
		0x00000004 | // DELALLOC
		0x00000008 | // ENCODED
		0x00000080 | // DATA_ENCRYPTED
		0x00000100 | // NOT_ALIGNED
		0x00000200 | // DATA_INLINE
		0x00000400 | // DATA_TAIL
		0x00000800 // UNWRITTEN
)

const (
	fiemapHeaderSize = 32
	fiemapExtentSize = 56
	// Files with more extents than this are hashed instead.
	fiemapMaxExtents = 64
)

// extentFingerprint returns a digest of where path's bytes sit on disk.
//
// Two files of the same size on the same device with the same fingerprint
// read the same physical blocks, so they hold the same bytes. This is how a
// reflink clone is recognised without reading it: the clone is a new inode,
// but it shares the original's extents.
//
// ok is false when the layout cannot prove that: the filesystem does not
// support FIEMAP, an extent is not yet allocated or is stored inline, or the
// file has more than fiemapMaxExtents extents.
func extentFingerprint(path string) (fp [extentFingerprintSize]byte, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return fp, false
	}
	defer f.Close()

	// uint64 backing keeps the buffer aligned for the kernel's struct layout.
	buf := make([]uint64, (fiemapHeaderSize+fiemapMaxExtents*fiemapExtentSize)/8)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&buf[0])), len(buf)*8)
	binary.NativeEndian.PutUint64(raw[0:], 0)          // fm_start
	binary.NativeEndian.PutUint64(raw[8:], ^uint64(0)) // fm_length
	binary.NativeEndian.PutUint32(raw[24:], fiemapMaxExtents)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), fsIocFiemap, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return fp, false
	}

	mapped := int(binary.NativeEndian.Uint32(raw[20:]))
	if mapped == 0 || mapped > fiemapMaxExtents {
		return fp, false
	}

	h := sha256.New()
	var rec [24]byte
	for i := 0; i < mapped; i++ {
		ext := raw[fiemapHeaderSize+i*fiemapExtentSize:]
		flags := binary.NativeEndian.Uint32(ext[40:])
		if flags&fiemapExtentUnsettled != 0 {
			return fp, false
		}
		if i == mapped-1 && flags&fiemapExtentLast == 0 {
			return fp, false // more extents than fit in buf
		}
		copy(rec[:], ext[:24]) // fe_logical, fe_physical, fe_length
		h.Write(rec[:])
	}
	copy(fp[:], h.Sum(nil))
	return fp, true
}

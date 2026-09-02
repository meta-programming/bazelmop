package dedupe

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// reflinkTestDir returns a directory on a filesystem that can reflink.
//
// t.TempDir() is usually tmpfs or ext4, where FICLONE returns ENOTSUP and
// these tests can only skip. Point BAZELMOP_REFLINK_TESTDIR at a Btrfs, XFS or
// APFS path to actually run them; that is how the mode regression they guard
// was reproduced and then verified fixed.
func reflinkTestDir(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("BAZELMOP_REFLINK_TESTDIR"); root != "" {
		dir, err := os.MkdirTemp(root, "bazelmop-clone-*")
		if err != nil {
			t.Fatalf("creating a test directory under %s: %v", root, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	return t.TempDir()
}

// A deduplicated file must keep the permissions of the file it replaced.
//
// This is the regression guard for a reflink clone created with a fixed 0644.
// A clone is a new inode and inherits nothing from what it replaces, so the
// executable bit vanished from every binary that got deduplicated -- compilers,
// protoc plugins, toolchain binaries -- and the resulting build failures named
// a permission problem rather than the deduplicator.
func TestCloneFilePreservesTheReplacedFilesMode(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("reflink cloning is not supported on this platform")
	}
	dir := reflinkTestDir(t)

	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("identical contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []os.FileMode{0o755, 0o644, 0o700, 0o555} {
		dst := filepath.Join(dir, "dst")
		_ = os.Remove(dst)

		if err := cloneFile(src, dst, mode); err != nil {
			// A tmpfs or ext4 TempDir cannot reflink; the mode contract is
			// still what is under test, so skip rather than fail on the
			// filesystem the test happens to run on.
			t.Skipf("cloneFile: %v", err)
		}
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatalf("stat %s: %v", dst, err)
		}
		if got := info.Mode().Perm(); got != mode.Perm() {
			t.Errorf("clone made with mode %v has mode %v", mode.Perm(), got)
		}
	}
}

// The mode must survive the process umask, which O_CREATE applies and an
// explicit chmod does not.
func TestCloneFileModeIsNotMaskedByUmask(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("umask handling is exercised on linux")
	}
	old := umask(0o077)
	defer umask(old)

	dir := reflinkTestDir(t)
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("identical contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	if err := cloneFile(src, dst, 0o755); err != nil {
		t.Skipf("cloneFile: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// 0755 under a 0077 umask becomes 0700 if the umask was allowed to apply.
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %v, want 0755; the umask was applied to the clone", got)
	}
}

package dedupe

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)


func TestAtomicLinkSafetyGate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dedup-test-safety-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sourceDir := filepath.Join(tmpDir, "source_dir")
	targetWriteableDir := filepath.Join(tmpDir, "target_writeable_dir")
	targetReadOnlyDir := filepath.Join(tmpDir, "target_readonly_dir")

	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("failed to create source dir: %v", err)
	}
	if err := os.MkdirAll(targetWriteableDir, 0755); err != nil {
		t.Fatalf("failed to create target writeable dir: %v", err)
	}
	if err := os.MkdirAll(targetReadOnlyDir, 0755); err != nil {
		t.Fatalf("failed to create target readonly dir: %v", err)
	}

	source := filepath.Join(sourceDir, "source")
	targetWriteable := filepath.Join(targetWriteableDir, "target_writeable")
	targetReadOnly := filepath.Join(targetReadOnlyDir, "target_readonly")

	// Write files
	if err := os.WriteFile(source, []byte("shared content"), 0444); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}
	if err := os.WriteFile(targetWriteable, []byte("shared content"), 0644); err != nil {
		t.Fatalf("failed to write targetWriteable: %v", err)
	}
	if err := os.WriteFile(targetReadOnly, []byte("shared content"), 0444); err != nil {
		t.Fatalf("failed to write targetReadOnly: %v", err)
	}

	// Make directories read-only to match Bazel behavior
	if err := os.Chmod(sourceDir, 0555); err != nil {
		t.Fatalf("failed to chmod sourceDir: %v", err)
	}
	if err := os.Chmod(targetWriteableDir, 0555); err != nil {
		t.Fatalf("failed to chmod targetWriteableDir: %v", err)
	}
	if err := os.Chmod(targetReadOnlyDir, 0555); err != nil {
		t.Fatalf("failed to chmod targetReadOnlyDir: %v", err)
	}

	// Defer restoring write permissions on directories so os.RemoveAll can clean up tmpDir
	defer func() {
		_ = os.Chmod(sourceDir, 0755)
		_ = os.Chmod(targetWriteableDir, 0755)
		_ = os.Chmod(targetReadOnlyDir, 0755)
	}()

	d := NewDeduplicator(Config{DryRun: false, PreferReflink: false})

	// 1. Linking to writeable file in read-only directory should succeed by converting target to read-only
	err = d.atomicLink(source, targetWriteable)
	if err != nil {
		t.Errorf("expected successful hard link to writeable target file in read-only dir, got error: %v", err)
	}

	// Verify targetWriteableDir permission is restored to read-only (0555 or mode without owner write bit)
	infoDirWriteable, err := os.Stat(targetWriteableDir)
	if err != nil {
		t.Fatalf("failed to stat targetWriteableDir: %v", err)
	}
	if infoDirWriteable.Mode()&0200 != 0 {
		t.Errorf("expected targetWriteableDir permissions to be restored to read-only, got mode %v", infoDirWriteable.Mode())
	}

	// Verify targetWriteable file itself is now read-only
	infoWriteable, err := os.Stat(targetWriteable)
	if err != nil {
		t.Fatalf("failed to stat targetWriteable: %v", err)
	}
	if infoWriteable.Mode()&0222 != 0 {
		t.Errorf("expected targetWriteable file to be read-only, got mode %v", infoWriteable.Mode())
	}

	// 2. Linking to read-only file in read-only directory should succeed
	err = d.atomicLink(source, targetReadOnly)
	if err != nil {
		t.Errorf("expected successful hard link to read-only file in read-only dir, got error: %v", err)
	}

	// Verify targetReadOnlyDir permission is restored to read-only
	infoDirReadOnly, err := os.Stat(targetReadOnlyDir)
	if err != nil {
		t.Fatalf("failed to stat targetReadOnlyDir: %v", err)
	}
	if infoDirReadOnly.Mode()&0200 != 0 {
		t.Errorf("expected targetReadOnlyDir permissions to be restored to read-only, got mode %v", infoDirReadOnly.Mode())
	}

	// Verify they all now share the same inode
	infoSrc, err := os.Stat(source)
	if err != nil {
		t.Fatalf("failed to stat source: %v", err)
	}
	infoReadOnly, err := os.Stat(targetReadOnly)
	if err != nil {
		t.Fatalf("failed to stat targetReadOnly: %v", err)
	}

	statSrc := infoSrc.Sys().(*syscall.Stat_t)
	statWriteable := infoWriteable.Sys().(*syscall.Stat_t)
	statReadOnly := infoReadOnly.Sys().(*syscall.Stat_t)

	if statSrc.Ino != statWriteable.Ino {
		t.Errorf("expected source and targetWriteable to share inode, got %d and %d", statSrc.Ino, statWriteable.Ino)
	}
	if statSrc.Ino != statReadOnly.Ino {
		t.Errorf("expected source and targetReadOnly to share inode, got %d and %d", statSrc.Ino, statReadOnly.Ino)
	}
}

func TestDeduplicateDryRun(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dedup-test-dryrun-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create files
	pathA := filepath.Join(tmpDir, "fileA")
	pathB := filepath.Join(tmpDir, "fileB")

	if err := os.WriteFile(pathA, []byte("identical"), 0444); err != nil {
		t.Fatalf("failed to write fileA: %v", err)
	}
	if err := os.WriteFile(pathB, []byte("identical"), 0444); err != nil {
		t.Fatalf("failed to write fileB: %v", err)
	}

	// Get inodes
	infoA, _ := os.Stat(pathA)
	infoB, _ := os.Stat(pathB)
	statA := infoA.Sys().(*syscall.Stat_t)
	statB := infoB.Sys().(*syscall.Stat_t)

	entries := []FileEntry{
		{Path: pathA, Size: infoA.Size(), Inode: statA.Ino, Dev: statA.Dev},
		{Path: pathB, Size: infoB.Size(), Inode: statB.Ino, Dev: statB.Dev},
	}

	// Perform dry run
	d := NewDeduplicator(Config{
		DryRun:        true,
		MinReportSize: 0,
	})

	ctx := context.Background()
	_, err = d.Deduplicate(ctx, entries)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}

	// In dry-run, they should still have different inodes
	infoA2, _ := os.Stat(pathA)
	infoB2, _ := os.Stat(pathB)
	statA2 := infoA2.Sys().(*syscall.Stat_t)
	statB2 := infoB2.Sys().(*syscall.Stat_t)

	if statA2.Ino != statA.Ino || statB2.Ino != statB.Ino {
		t.Errorf("inodes modified in dry run")
	}
}

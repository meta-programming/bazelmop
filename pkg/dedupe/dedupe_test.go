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

	source := filepath.Join(tmpDir, "source")
	targetWriteable := filepath.Join(tmpDir, "target_writeable")
	targetReadOnly := filepath.Join(tmpDir, "target_readonly")

	if err := os.WriteFile(source, []byte("shared content"), 0444); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}
	// Writeable file
	if err := os.WriteFile(targetWriteable, []byte("shared content"), 0644); err != nil {
		t.Fatalf("failed to write targetWriteable: %v", err)
	}
	// Read-only file
	if err := os.WriteFile(targetReadOnly, []byte("shared content"), 0444); err != nil {
		t.Fatalf("failed to write targetReadOnly: %v", err)
	}

	d := NewDeduplicator(Config{DryRun: false, PreferReflink: false})

	// 1. Linking to writeable file should succeed by automatically converting it to read-only first
	err = d.atomicLink(source, targetWriteable)
	if err != nil {
		t.Errorf("expected successful hard link to writeable target file (by converting to read-only), got error: %v", err)
	}

	// Verify targetWriteable is now read-only
	infoWriteable, err := os.Stat(targetWriteable)
	if err != nil {
		t.Fatalf("failed to stat targetWriteable: %v", err)
	}
	if infoWriteable.Mode()&0222 != 0 {
		t.Errorf("expected targetWriteable to be read-only after link, got mode %v", infoWriteable.Mode())
	}

	// 2. Linking to read-only file should succeed
	err = d.atomicLink(source, targetReadOnly)
	if err != nil {
		t.Errorf("expected successful hard link to read-only file, got error: %v", err)
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

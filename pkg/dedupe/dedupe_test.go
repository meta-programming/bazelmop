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

	// 1. Linking to writeable file should fail due to safety gate (when using hard links)
	err = d.atomicLink(source, targetWriteable)
	if err == nil {
		t.Errorf("expected safety gate failure for writeable target file, got nil")
	}

	// 2. Linking to read-only file should succeed
	err = d.atomicLink(source, targetReadOnly)
	if err != nil {
		t.Errorf("expected successful hard link to read-only file, got error: %v", err)
	}

	// Verify they now share the same inode
	infoSrc, err := os.Stat(source)
	if err != nil {
		t.Fatalf("failed to stat source: %v", err)
	}
	infoTarget, err := os.Stat(targetReadOnly)
	if err != nil {
		t.Fatalf("failed to stat target: %v", err)
	}

	statSrc := infoSrc.Sys().(*syscall.Stat_t)
	statTarget := infoTarget.Sys().(*syscall.Stat_t)

	if statSrc.Ino != statTarget.Ino {
		t.Errorf("expected source and target to share inode, got %d and %d", statSrc.Ino, statTarget.Ino)
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
	err = d.Deduplicate(ctx, entries)
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

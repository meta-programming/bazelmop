package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRemoveAll(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "read-only-tree")
	subDir := filepath.Join(targetDir, "subdir")
	file := filepath.Join(subDir, "readonly.txt")

	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(file, []byte("bazel artifact"), 0400); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Make directories read-only (like Bazel output artifacts / execroot trees)
	if err := os.Chmod(file, 0400); err != nil {
		t.Fatalf("failed to chmod file: %v", err)
	}
	if err := os.Chmod(subDir, 0500); err != nil {
		t.Fatalf("failed to chmod subdir: %v", err)
	}
	if err := os.Chmod(targetDir, 0500); err != nil {
		t.Fatalf("failed to chmod targetDir: %v", err)
	}

	// Plain RemoveAll on targetDir would fail with permission denied on Linux
	if err := safeRemoveAll(targetDir); err != nil {
		t.Fatalf("safeRemoveAll failed: %v", err)
	}

	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Fatalf("targetDir still exists after safeRemoveAll")
	}
}

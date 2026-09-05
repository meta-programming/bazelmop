package dedupe

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestComputeSHA256MatchesReference guards the read path against the page cache
// advice added around it. The advisory calls must not alter what is read, at
// any size, including a file that ends exactly on a read boundary.
func TestComputeSHA256MatchesReference(t *testing.T) {
	dir := t.TempDir()

	sizes := map[string]int{
		"empty":      0,
		"tiny":       11,
		"page":       4096,
		"multiblock": 32<<10*3 + 7,
		"large":      1 << 20,
	}

	for name, size := range sizes {
		t.Run(name, func(t *testing.T) {
			content := make([]byte, size)
			if _, err := rand.New(rand.NewSource(int64(size))).Read(content); err != nil {
				t.Fatalf("failed to generate content: %v", err)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatalf("failed to write file: %v", err)
			}

			got, err := computeSHA256(path)
			if err != nil {
				t.Fatalf("computeSHA256 failed: %v", err)
			}

			sum := sha256.Sum256(content)
			want := hex.EncodeToString(sum[:])
			if got != want {
				t.Errorf("digest mismatch for %d bytes:\n got %s\nwant %s", size, got, want)
			}
		})
	}
}

// TestComputeSHA256Repeatable checks that dropping a file's pages does not
// disturb a later read of the same file.
func TestComputeSHA256Repeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeated")
	if err := os.WriteFile(path, []byte("content read more than once"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	first, err := computeSHA256(path)
	if err != nil {
		t.Fatalf("first computeSHA256 failed: %v", err)
	}
	for i := range 3 {
		again, err := computeSHA256(path)
		if err != nil {
			t.Fatalf("repeat %d failed: %v", i, err)
		}
		if again != first {
			t.Fatalf("repeat %d returned %s, want %s", i, again, first)
		}
	}
}

func TestComputeSHA256MissingFile(t *testing.T) {
	if _, err := computeSHA256(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

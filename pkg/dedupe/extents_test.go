package dedupe

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestSelectHashCandidatesSkipsClones(t *testing.T) {
	clone := [extentFingerprintSize]byte{7}
	entries := []FileEntry{
		// Three reflink clones: distinct inodes, one set of extents.
		{Path: "a", Size: 10, Dev: 1, Inode: 1, Extents: clone},
		{Path: "b", Size: 10, Dev: 1, Inode: 2, Extents: clone},
		{Path: "c", Size: 10, Dev: 1, Inode: 3, Extents: clone},
		// The same three plus one file whose extents are unknown.
		{Path: "d", Size: 20, Dev: 1, Inode: 4, Extents: [extentFingerprintSize]byte{8}},
		{Path: "e", Size: 20, Dev: 1, Inode: 5, Extents: [extentFingerprintSize]byte{8}},
		{Path: "f", Size: 20, Dev: 1, Inode: 6},
	}

	var got []string
	for _, idx := range selectHashCandidates(entries, groupBySize(entries)) {
		got = append(got, entries[idx].Path)
	}

	// a, b and c are one file, so nothing in their group is read. In the
	// second group d and e are one file and f another: one path each.
	sort.Strings(got)
	if len(got) != 2 || (got[0] != "d" && got[0] != "e") || got[1] != "f" {
		t.Errorf("selectHashCandidates chose %v, want one of d/e and f", got)
	}
}

func TestSameFile(t *testing.T) {
	x := [extentFingerprintSize]byte{1}
	cases := []struct {
		name string
		a, b FileEntry
		want bool
	}{
		{"same inode", FileEntry{Dev: 1, Inode: 5}, FileEntry{Dev: 1, Inode: 5}, true},
		{"same extents", FileEntry{Dev: 1, Inode: 5, Extents: x}, FileEntry{Dev: 1, Inode: 6, Extents: x}, true},
		{"one extents unknown", FileEntry{Dev: 1, Inode: 5, Extents: x}, FileEntry{Dev: 1, Inode: 6}, false},
		{"other device", FileEntry{Dev: 1, Inode: 5, Extents: x}, FileEntry{Dev: 2, Inode: 5, Extents: x}, false},
	}
	for _, c := range cases {
		if got := sameFile(&c.a, &c.b); got != c.want {
			t.Errorf("%s: sameFile = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestExtentFingerprintRecognisesClone needs a filesystem with reflinks,
// such as XFS or Btrfs. Set TMPDIR to one to run it.
func TestExtentFingerprintRecognisesClone(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("bazelmop "), 100000)
	orig := filepath.Join(dir, "orig")
	copied := filepath.Join(dir, "copy")
	clone := filepath.Join(dir, "clone")
	for _, p := range []string{orig, copied} {
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Flush so the extents are allocated rather than delayed.
	for _, p := range []string{orig, copied} {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := cloneFile(orig, clone, 0o644); err != nil {
		t.Skipf("%s does not support reflinks: %v", dir, err)
	}

	fpOrig, ok := extentFingerprint(orig)
	if !ok {
		t.Skipf("%s does not report extents", dir)
	}
	fpClone, ok := extentFingerprint(clone)
	if !ok || fpClone != fpOrig {
		t.Errorf("clone fingerprint = %x (ok %v), want %x", fpClone, ok, fpOrig)
	}
	if fpCopy, ok := extentFingerprint(copied); ok && fpCopy == fpOrig {
		t.Errorf("an independent copy has the original's fingerprint %x", fpOrig)
	}
}

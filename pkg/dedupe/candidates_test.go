package dedupe

import (
	"sort"
	"testing"
)

func groupBySize(entries []FileEntry) map[SizeDev][]int {
	groups := make(map[SizeDev][]int)
	for i, e := range entries {
		key := SizeDev{Size: e.Size, Dev: e.Dev}
		groups[key] = append(groups[key], i)
	}
	return groups
}

func TestSelectHashCandidates(t *testing.T) {
	entries := []FileEntry{
		// Two hard links to inode 1 and one separate copy: hash inode 1 once, and inode 2.
		{Path: "a1", Size: 10, Dev: 1, Inode: 1},
		{Path: "b", Size: 10, Dev: 1, Inode: 2},
		{Path: "a2", Size: 10, Dev: 1, Inode: 1},
		// Three paths to one inode: already deduplicated, nothing to hash.
		{Path: "c1", Size: 20, Dev: 1, Inode: 3},
		{Path: "c2", Size: 20, Dev: 1, Inode: 3},
		{Path: "c3", Size: 20, Dev: 1, Inode: 3},
		// Inode 4 is known from the repository cache through one of its paths.
		{Path: "d1", Size: 30, Dev: 1, Inode: 4},
		{Path: "d2", Size: 30, Dev: 1, Inode: 4, Hash: "cas"},
		{Path: "e", Size: 30, Dev: 1, Inode: 5},
		// Unique size: nothing to compare against.
		{Path: "f", Size: 40, Dev: 1, Inode: 6},
		// Empty files are never read.
		{Path: "g", Size: 0, Dev: 1, Inode: 7},
		{Path: "h", Size: 0, Dev: 1, Inode: 8},
	}

	var got []string
	for _, idx := range selectHashCandidates(entries, groupBySize(entries)) {
		got = append(got, entries[idx].Path)
	}
	sort.Strings(got)

	// For inode 1 either path may be chosen.
	if len(got) != 3 || (got[0] != "a1" && got[0] != "a2") || got[1] != "b" || got[2] != "e" {
		t.Errorf("selectHashCandidates chose %v, want one of a1/a2, then b and e", got)
	}
}

func TestShareInodeHashes(t *testing.T) {
	entries := []FileEntry{
		{Path: "a1", Size: 10, Dev: 1, Inode: 1, Hash: "x"},
		{Path: "a2", Size: 10, Dev: 1, Inode: 1},
		{Path: "b", Size: 10, Dev: 1, Inode: 2},
		// Same inode number on another device is a different file.
		{Path: "z", Size: 10, Dev: 2, Inode: 1},
		{Path: "z2", Size: 10, Dev: 2, Inode: 9},
	}

	shareInodeHashes(entries, groupBySize(entries))

	want := map[string]string{"a1": "x", "a2": "x", "b": "", "z": "", "z2": ""}
	for _, e := range entries {
		if e.Hash != want[e.Path] {
			t.Errorf("%s has hash %q, want %q", e.Path, e.Hash, want[e.Path])
		}
	}
}

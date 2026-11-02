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
		{Path: "d2", Size: 30, Dev: 1, Inode: 4, Hash: [32]byte{1}},
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
		{Path: "a1", Size: 10, Dev: 1, Inode: 1, Hash: [32]byte{2}},
		{Path: "a2", Size: 10, Dev: 1, Inode: 1},
		{Path: "b", Size: 10, Dev: 1, Inode: 2},
		// Same inode number on another device is a different file.
		{Path: "z", Size: 10, Dev: 2, Inode: 1},
		{Path: "z2", Size: 10, Dev: 2, Inode: 9},
	}

	shareFileHashes(entries, groupBySize(entries))

	want := map[string][32]byte{"a1": {2}, "a2": {2}}
	for _, e := range entries {
		if e.Hash != want[e.Path] {
			t.Errorf("%s has hash %x, want %x", e.Path, e.Hash, want[e.Path])
		}
	}
}

func TestExtractWorkspace(t *testing.T) {
	ws := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		path, user, workspace string
	}{
		{"/root/_bazel_red/" + ws + "/external/x", "red", ws},
		{"/root/_bazel_red/" + ws, "red", ws},
		{"/root/_bazel_red", "unknown", "unknown"},
		{"/root/_bazel_red/short/external/x", "unknown", "unknown"},
		{"/root/_bazel_red/short/_bazel_bob/" + ws + "/x", "bob", ws},
		// Flat output user root, as with --output_user_root=/media/x/bazel.
		{"/media/x/bazel/" + ws + "/execroot/_main/bazel-out/k8/bin/y", "unknown", ws},
		{"/root/other/ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ/x", "unknown", "unknown"},
	}
	for _, c := range cases {
		user, workspace := extractWorkspace(c.path)
		if user != c.user || workspace != c.workspace {
			t.Errorf("extractWorkspace(%q) = %q, %q; want %q, %q", c.path, user, workspace, c.user, c.workspace)
		}
	}
}

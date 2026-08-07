package bazelcas

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestResolveWorkspacePath(t *testing.T) {
	sys := fstest.MapFS{
		"_bazel_red/0123456789abcdef0123456789abcdef/DO_NOT_BUILD_HERE": &fstest.MapFile{
			Data: []byte("/home/user/workspace-a\n"),
		},
		"_bazel_red/1111111111abcdef0123456789abcdef/server/cmdline": &fstest.MapFile{
			Data: []byte("bazel --workspace_directory=/home/user/workspace-b --output_base=/foo\n"),
		},
		"_bazel_red/2222222222abcdef0123456789abcdef/server/cmdline": &fstest.MapFile{
			Data: []byte("invalid cmdline without flag"),
		},
	}

	wsA := ResolveWorkspacePath(sys, "_bazel_red/0123456789abcdef0123456789abcdef")
	if wsA != "/home/user/workspace-a" {
		t.Errorf("expected /home/user/workspace-a, got %q", wsA)
	}

	wsB := ResolveWorkspacePath(sys, "_bazel_red/1111111111abcdef0123456789abcdef")
	if wsB != "/home/user/workspace-b" {
		t.Errorf("expected /home/user/workspace-b, got %q", wsB)
	}

	wsC := ResolveWorkspacePath(sys, "_bazel_red/2222222222abcdef0123456789abcdef")
	if wsC != "" {
		t.Errorf("expected empty path for missing workspace, got %q", wsC)
	}
}

func TestDetermineWorkspaceStatus(t *testing.T) {
	// Create a temporary directory for an active workspace
	tmpDir, err := os.MkdirTemp("", "bazelmop-test-ws-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sys := fstest.MapFS{}

	statusActive := DetermineWorkspaceStatus(sys, tmpDir)
	if statusActive != StatusActive {
		t.Errorf("expected StatusActive for existing dir %s, got %s", tmpDir, statusActive)
	}

	nonExistent := filepath.Join(tmpDir, "does-not-exist-dir")
	statusOrphaned := DetermineWorkspaceStatus(sys, nonExistent)
	if statusOrphaned != StatusOrphaned {
		t.Errorf("expected StatusOrphaned for missing path, got %s", statusOrphaned)
	}

	statusEmpty := DetermineWorkspaceStatus(sys, "")
	if statusEmpty != StatusOrphaned {
		t.Errorf("expected StatusOrphaned for empty path, got %s", statusEmpty)
	}
}

func TestSortInstances(t *testing.T) {
	now := time.Now()
	inst1 := &Instance{ID: "inst1", SizeBytes: 100, LastModified: now.Add(-10 * time.Minute)}
	inst2 := &Instance{ID: "inst2", SizeBytes: 500, LastModified: now.Add(-60 * time.Minute)}
	inst3 := &Instance{ID: "inst3", SizeBytes: 200, LastModified: now.Add(-30 * time.Minute)}

	instances := []*Instance{inst1, inst2, inst3}

	// Sort by LRU (oldest mtime first)
	SortInstances(instances, "lru")
	if instances[0].ID != "inst2" || instances[1].ID != "inst3" || instances[2].ID != "inst1" {
		t.Errorf("LRU sort failed: got order [%s, %s, %s]", instances[0].ID, instances[1].ID, instances[2].ID)
	}

	// Sort by Size (largest first)
	SortInstances(instances, "size")
	if instances[0].ID != "inst2" || instances[1].ID != "inst3" || instances[2].ID != "inst1" {
		t.Errorf("Size sort failed: got order [%s, %s, %s]", instances[0].ID, instances[1].ID, instances[2].ID)
	}

	// Sort by ID
	SortInstances(instances, "id")
	if instances[0].ID != "inst1" || instances[1].ID != "inst2" || instances[2].ID != "inst3" {
		t.Errorf("ID sort failed: got order [%s, %s, %s]", instances[0].ID, instances[1].ID, instances[2].ID)
	}
}

func TestTTLAndTargetFreeFiltering(t *testing.T) {
	now := time.Now()
	instOldOrphan := &Instance{
		ID:            "old-orphan",
		Type:          TypeOutputBase,
		Status:        StatusOrphaned,
		SizeBytes:     10 * 1024 * 1024 * 1024, // 10 GB
		LastModified:  now.Add(-40 * 24 * time.Hour),
		WorkspacePath: "/missing/ws1",
	}
	instOldActive := &Instance{
		ID:            "old-active",
		Type:          TypeOutputBase,
		Status:        StatusActive,
		SizeBytes:     15 * 1024 * 1024 * 1024, // 15 GB
		LastModified:  now.Add(-20 * 24 * time.Hour),
		WorkspacePath: "/active/ws2",
	}
	instNewOrphan := &Instance{
		ID:            "new-orphan",
		Type:          TypeOutputBase,
		Status:        StatusOrphaned,
		SizeBytes:     5 * 1024 * 1024 * 1024, // 5 GB
		LastModified:  now.Add(-2 * 24 * time.Hour),
		WorkspacePath: "/missing/ws3",
	}

	instances := []*Instance{instOldOrphan, instOldActive, instNewOrphan}

	// Filter TTL=14d
	ttlCandidates := FilterInstances(instances, GCOptions{TTL: 14 * 24 * time.Hour}, now)
	if len(ttlCandidates) != 2 {
		t.Fatalf("expected 2 TTL candidates, got %d", len(ttlCandidates))
	}
	if ttlCandidates[0].ID != "old-orphan" || ttlCandidates[1].ID != "old-active" {
		t.Errorf("unexpected TTL candidate order: %s, %s", ttlCandidates[0].ID, ttlCandidates[1].ID)
	}

	// Filter Orphaned only
	orphCandidates := FilterInstances(instances, GCOptions{OrphanedOnly: true}, now)
	if len(orphCandidates) != 2 {
		t.Fatalf("expected 2 Orphaned candidates, got %d", len(orphCandidates))
	}

	// Filter Orphaned + TTL=14d
	orphTTLCandidates := FilterInstances(instances, GCOptions{OrphanedOnly: true, TTL: 14 * 24 * time.Hour}, now)
	if len(orphTTLCandidates) != 1 || orphTTLCandidates[0].ID != "old-orphan" {
		t.Fatalf("expected 1 orphan+TTL candidate (old-orphan), got %d", len(orphTTLCandidates))
	}

	// Target-Free = 12 GB
	targetFreeCandidates := FilterInstances(instances, GCOptions{TargetFreeBytes: 12 * 1024 * 1024 * 1024}, now)
	// Should pick oldest first (old-orphan 10GB), total 10GB < 12GB, so picks second oldest (old-active 15GB), total 25GB >= 12GB.
	if len(targetFreeCandidates) != 2 {
		t.Fatalf("expected 2 target-free candidates, got %d", len(targetFreeCandidates))
	}
}

func TestParseTTLAndSize(t *testing.T) {
	ttl30d, err := ParseTTL("30d")
	if err != nil || ttl30d != 30*24*time.Hour {
		t.Errorf("ParseTTL(30d) failed: got %v, err: %v", ttl30d, err)
	}

	ttl2w, err := ParseTTL("2w")
	if err != nil || ttl2w != 14*24*time.Hour {
		t.Errorf("ParseTTL(2w) failed: got %v, err: %v", ttl2w, err)
	}

	ttl1h, err := ParseTTL("1h")
	if err != nil || ttl1h != time.Hour {
		t.Errorf("ParseTTL(1h) failed: got %v, err: %v", ttl1h, err)
	}

	sz20g, err := ParseSize("20GB")
	if err != nil || sz20g != 20*1024*1024*1024 {
		t.Errorf("ParseSize(20GB) failed: got %d, err: %v", sz20g, err)
	}

	sz500m, err := ParseSize("500MB")
	if err != nil || sz500m != 500*1024*1024 {
		t.Errorf("ParseSize(500MB) failed: got %d, err: %v", sz500m, err)
	}

	szFmt := FormatSize(15*1024*1024*1024 + 400*1024*1024)
	if szFmt != "15.4 GB" {
		t.Errorf("FormatSize failed: expected 15.4 GB, got %s", szFmt)
	}
}

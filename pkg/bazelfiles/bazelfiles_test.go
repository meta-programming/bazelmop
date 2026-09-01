package bazelfiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

func TestPathMethods(t *testing.T) {
	buildPath := BuildOutputPath("/cache/_bazel_red/0123456789abcdef0123456789abcdef/execroot/_main/bazel-out/k8-fastbuild/bin/main.a")
	if buildPath.Config() != "k8-fastbuild" {
		t.Errorf("expected k8-fastbuild, got %s", buildPath.Config())
	}
	expectedWS := bazelcas.WorkspaceCASPath("/cache/_bazel_red/0123456789abcdef0123456789abcdef")
	if buildPath.Workspace() != expectedWS {
		t.Errorf("expected workspace %s, got %s", expectedWS, buildPath.Workspace())
	}

	depPath := ExternalDepPath("/cache/_bazel_red/0123456789abcdef0123456789abcdef/external/rules_go/README.md")
	if depPath.Repository() != "rules_go" {
		t.Errorf("expected rules_go, got %s", depPath.Repository())
	}
	if depPath.Workspace() != expectedWS {
		t.Errorf("expected workspace %s, got %s", expectedWS, depPath.Workspace())
	}

	casPath := RepoCacheCASPath("/cache/_bazel_red/cache/repos/v1/content_addressable/sha256/1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef/file")
	expectedUser := bazelcas.UserCASPath("/cache/_bazel_red")
	if casPath.User() != expectedUser {
		t.Errorf("expected user %s, got %s", expectedUser, casPath.User())
	}
	expectedHash := "1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef"
	if casPath.Hash() != expectedHash {
		t.Errorf("expected hash %s, got %s", expectedHash, casPath.Hash())
	}
}

func TestWalk(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bazelfiles-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	userDir := filepath.Join(tmpDir, "_bazel_red")
	wsDir := filepath.Join(userDir, "0123456789abcdef0123456789abcdef")
	execDir := filepath.Join(wsDir, "execroot", "_main")
	bazelOutDir := filepath.Join(execDir, "bazel-out", "k8-fastbuild")
	externalDir := filepath.Join(wsDir, "external", "rules_go")
	repoCasDir := filepath.Join(userDir, "cache", "repos", "v1", "content_addressable", "sha256", "1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef")

	// Create directories
	dirs := []string{bazelOutDir, externalDir, repoCasDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	// Create files
	if err := os.WriteFile(filepath.Join(bazelOutDir, "main.a"), []byte("bin"), 0644); err != nil {
		t.Fatalf("failed to write build output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "README.md"), []byte("readme"), 0644); err != nil {
		t.Fatalf("failed to write external dep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoCasDir, "file"), []byte("archive"), 0644); err != nil {
		t.Fatalf("failed to write CAS file: %v", err)
	}

	ctx := context.Background()
	root := bazelcas.RootCASPath(tmpDir)

	// Test case 1: Walk all
	discovered, err := Walk(ctx, root, WithScanExternal(true), WithScanBazelOut(true))
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}

	if len(discovered.BuildOutputs) != 1 {
		t.Errorf("expected 1 build output, got %d", len(discovered.BuildOutputs))
	}
	if len(discovered.ExternalDeps) != 1 {
		t.Errorf("expected 1 external dep, got %d", len(discovered.ExternalDeps))
	}
	if len(discovered.RepoCacheCAS) != 1 {
		t.Errorf("expected 1 repo cache file, got %d", len(discovered.RepoCacheCAS))
	}

	// Test case 2: Walk bazel-out only
	discovered2, err := Walk(ctx, root, WithScanExternal(false), WithScanBazelOut(true))
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}
	if len(discovered2.BuildOutputs) != 1 {
		t.Errorf("expected 1 build output, got %d", len(discovered2.BuildOutputs))
	}
	if len(discovered2.ExternalDeps) != 0 {
		t.Errorf("expected 0 external deps, got %d", len(discovered2.ExternalDeps))
	}
	if len(discovered2.RepoCacheCAS) != 0 {
		t.Errorf("expected 0 repo cache files, got %d", len(discovered2.RepoCacheCAS))
	}
}

// A root named by --output_user_root is already the per-user directory, so its
// output bases sit directly beneath it with no _bazel_<user> level.
//
// This is not a rare configuration: any machine that moves its Bazel cache off
// the home partition has it. Before this was handled, Walk skipped every entry
// on such a root and returned no files at all, so `report` and `clean` said
// nothing was found while `output-bases` listed the same cache correctly.
func TestWalkFindsOutputBasesDirectlyUnderTheRoot(t *testing.T) {
	tmpDir := t.TempDir()

	wsDir := filepath.Join(tmpDir, "0123456789abcdef0123456789abcdef")
	bazelOutDir := filepath.Join(wsDir, "execroot", "_main", "bazel-out", "k8-fastbuild")
	externalDir := filepath.Join(wsDir, "external", "rules_go")
	repoCasDir := filepath.Join(tmpDir, "cache", "repos", "v1", "content_addressable", "sha256",
		"1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef")

	for _, dir := range []string{bazelOutDir, externalDir, repoCasDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(bazelOutDir, "main.a"):    "bin",
		filepath.Join(externalDir, "README.md"): "doc",
		filepath.Join(repoCasDir, "file"):       "archive",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}

	got, err := Walk(context.Background(), bazelcas.RootCASPath(tmpDir))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got.BuildOutputs) != 1 {
		t.Errorf("build outputs = %d, want 1: %v", len(got.BuildOutputs), got.BuildOutputs)
	}
	if len(got.ExternalDeps) != 1 {
		t.Errorf("external deps = %d, want 1: %v", len(got.ExternalDeps), got.ExternalDeps)
	}
	if len(got.RepoCacheCAS) != 1 {
		t.Errorf("repo cache entries = %d, want 1: %v", len(got.RepoCacheCAS), got.RepoCacheCAS)
	}
}

// A root that is neither layout must stay empty rather than being walked as
// though every directory in it were an output base.
func TestWalkIgnoresARootThatHoldsNeitherLayout(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "not-a-cache", "external", "rules_go"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "not-a-cache", "external", "rules_go", "x"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := Walk(context.Background(), bazelcas.RootCASPath(tmpDir))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if n := len(got.BuildOutputs) + len(got.ExternalDeps) + len(got.RepoCacheCAS); n != 0 {
		t.Errorf("found %d files under a root that is not a Bazel cache", n)
	}
}

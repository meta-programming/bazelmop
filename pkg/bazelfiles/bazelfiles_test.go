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

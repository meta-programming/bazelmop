package bazelcas

import (
	"testing"
	"testing/fstest"
)

func TestPathTypes(t *testing.T) {
	root := RootCASPath("/cache")
	if root.String() != "/cache" {
		t.Errorf("expected /cache, got %s", root.String())
	}

	user := root.User("red")
	if user.String() != "/cache/_bazel_red" {
		t.Errorf("expected /cache/_bazel_red, got %s", user.String())
	}

	if user.Parent() != root {
		t.Errorf("expected parent to be %s, got %s", root, user.Parent())
	}

	repo := user.RepositoryCache()
	expectedRepo := "/cache/_bazel_red/cache/repos/v1/content_addressable"
	if repo != RepoCASPath(expectedRepo) {
		t.Errorf("expected repo cache %s, got %s", expectedRepo, repo)
	}

	md5Str := "0123456789abcdef0123456789abcdef"
	ws := user.Workspace(md5Str)
	expectedWS := "/cache/_bazel_red/" + md5Str
	if ws.String() != expectedWS {
		t.Errorf("expected workspace %s, got %s", expectedWS, ws.String())
	}

	if ws.Parent() != user {
		t.Errorf("expected parent to be %s, got %s", user, ws.Parent())
	}

	if ws.ExecRoot() != expectedWS+"/execroot" {
		t.Errorf("expected execroot, got %s", ws.ExecRoot())
	}

	if ws.BazelOut() != expectedWS+"/bazel-out" {
		t.Errorf("expected bazel-out, got %s", ws.BazelOut())
	}

	if ws.External() != expectedWS+"/external" {
		t.Errorf("expected external, got %s", ws.External())
	}

	hashDir := repo.HashDir("sha256", "abcdef")
	if string(hashDir) != expectedRepo+"/sha256/abcdef" {
		t.Errorf("expected hash dir, got %s", hashDir)
	}

	if hashDir.File() != expectedRepo+"/sha256/abcdef/file" {
		t.Errorf("expected file path, got %s", hashDir.File())
	}

	if hashDir.TempFile("xyz") != expectedRepo+"/sha256/abcdef/tmp-xyz" {
		t.Errorf("expected temp file path, got %s", hashDir.TempFile("xyz"))
	}
}

func TestWalk(t *testing.T) {
	// Create mock files using fstest.MapFS
	sys := fstest.MapFS{
		"_bazel_red/cache/repos/v1/content_addressable/sha256/1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef/file": &fstest.MapFile{
			Data: []byte("content"),
		},
		// A workspace
		"_bazel_red/0123456789abcdef0123456789abcdef/execroot/workspace_name/README.md": &fstest.MapFile{
			Data: []byte("workspace readme"),
		},
		"_bazel_red/0123456789abcdef0123456789abcdef/bazel-out/k8-fastbuild/bin/main.a": &fstest.MapFile{
			Data: []byte("bin"),
		},
		"_bazel_red/0123456789abcdef0123456789abcdef/external/rules_go/README.md": &fstest.MapFile{
			Data: []byte("rules_go"),
		},
		// A non-workspace (ignored folder)
		"_bazel_red/some_other_folder/execroot/readme": &fstest.MapFile{
			Data: []byte("other"),
		},
	}

	tree, err := Walk(sys)
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}

	users := tree.Users()
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}

	user, ok := users["red"]
	if !ok {
		t.Fatal("user red not found")
	}

	if user.Username() != "red" {
		t.Errorf("expected username red, got %s", user.Username())
	}

	repo := user.RepositoryCache()
	if repo == nil {
		t.Fatal("expected repository cache, got nil")
	}

	if len(repo.Hashes()) != 1 {
		t.Errorf("expected 1 hash in repo cache, got %d", len(repo.Hashes()))
	} else if repo.Hashes()[0] != "1b4f4ef8bc3a4d8c0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("unexpected hash: %s", repo.Hashes()[0])
	}

	workspaces := user.Workspaces()
	if len(workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(workspaces))
	}

	ws, ok := workspaces["0123456789abcdef0123456789abcdef"]
	if !ok {
		t.Fatal("workspace not found")
	}

	if ws.MD5() != "0123456789abcdef0123456789abcdef" {
		t.Errorf("expected workspace MD5, got %s", ws.MD5())
	}

	if len(ws.ExternalRepos()) != 1 || ws.ExternalRepos()[0] != "rules_go" {
		t.Errorf("expected rules_go external repo, got %v", ws.ExternalRepos())
	}
}

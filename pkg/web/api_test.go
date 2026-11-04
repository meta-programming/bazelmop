package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	bazelmopv1 "github.com/meta-programming/bazelmop/gen/bazelmop/v1"
	"github.com/meta-programming/bazelmop/gen/bazelmop/v1/bazelmopv1connect"
	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

func newTestAPI(t *testing.T) (*Server, bazelmopv1connect.BazelmopServiceClient) {
	t.Helper()
	s := NewServer("localhost", "0")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, bazelmopv1connect.NewBazelmopServiceClient(ts.Client(), ts.URL, connect.WithProtoJSON())
}

func TestGetStatus(t *testing.T) {
	s, client := newTestAPI(t)
	s.UpdateStatus("Hashing files: 1/2")
	s.UpdateReport("# Report", time.Now().Add(time.Hour))

	res, err := client.GetStatus(context.Background(), connect.NewRequest(&bazelmopv1.GetStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Msg.GetStatus()
	if st.GetActivity() != "Idle" || st.GetReportMarkdown() != "# Report" || st.GetNextRunTime() == nil {
		t.Errorf("GetStatus = %v", st)
	}
}

func TestWatchStatusSendsChanges(t *testing.T) {
	s, client := newTestAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.WatchStatus(ctx, connect.NewRequest(&bazelmopv1.WatchStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if !stream.Receive() {
		t.Fatalf("no initial status: %v", stream.Err())
	}
	if got := stream.Msg().GetStatus().GetActivity(); got != "Idle" {
		t.Errorf("initial activity = %q, want Idle", got)
	}

	s.UpdateStatus("Scanning directories...")
	if !stream.Receive() {
		t.Fatalf("no status after change: %v", stream.Err())
	}
	if got := stream.Msg().GetStatus().GetActivity(); got != "Scanning directories..." {
		t.Errorf("activity after change = %q", got)
	}
}

func TestListAndDeleteOutputBases(t *testing.T) {
	s, client := newTestAPI(t)
	dir := filepath.Join(t.TempDir(), "0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(dir, "ro"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Read-only like a Bazel output tree, so a plain os.RemoveAll fails.
	if err := os.Chmod(filepath.Join(dir, "ro"), 0o500); err != nil {
		t.Fatal(err)
	}
	s.UpdateInstances([]*bazelcas.Instance{
		{ID: "0123456789abcdef0123456789abcdef", Type: bazelcas.TypeOutputBase, Path: dir, WorkspacePath: "/ws/a", Status: bazelcas.StatusOrphaned, SizeBytes: 42},
		{ID: "fedcba9876543210fedcba9876543210", Type: bazelcas.TypeOutputBase, Path: "/nowhere", WorkspacePath: "N/A", Status: bazelcas.StatusOrphaned},
		{ID: "repo-cache", Type: bazelcas.TypeRepoCache, Path: filepath.Join(t.TempDir(), "rc"), WorkspacePath: "N/A", Status: bazelcas.StatusActive},
	})

	list, err := client.ListOutputBases(context.Background(), connect.NewRequest(&bazelmopv1.ListOutputBasesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	obs := list.Msg.GetOutputBases()
	if len(obs) != 3 || obs[0].GetSizeBytes() != 42 || obs[0].GetWorkspaceExists() || obs[1].GetWorkspacePath() != "" || obs[2].GetWorkspacePath() != "" {
		t.Fatalf("ListOutputBases = %v", obs)
	}

	del, err := client.DeleteOutputBases(context.Background(), connect.NewRequest(&bazelmopv1.DeleteOutputBasesRequest{
		Ids: []string{"0123456789abcdef0123456789abcdef", "missing"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := del.Msg.GetDeletedIds(); len(got) != 1 || got[0] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("deleted %v", got)
	}
	if f := del.Msg.GetFailures(); len(f) != 1 || f[0].GetId() != "missing" {
		t.Errorf("failures %v", f)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("output base still on disk: %v", err)
	}
}

func TestConnectJSONFromPlainFetch(t *testing.T) {
	s := NewServer("localhost", "0")
	s.UpdateStatus("Idle")
	req := httptest.NewRequest("POST", bazelmopv1connect.BazelmopServiceGetStatusProcedure, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"activity":"Idle"`) {
		t.Errorf("POST GetStatus = %d %s", w.Code, w.Body.String())
	}
}

func TestMutationsAreLocalOnly(t *testing.T) {
	s := NewServer("localhost", "0")
	cases := []struct {
		name, remote, host string
		want               int
	}{
		{"loopback", "127.0.0.1:5000", "localhost:8092", http.StatusOK},
		{"ipv6 loopback", "[::1]:5000", "[::1]:8092", http.StatusOK},
		{"other machine", "192.168.1.20:5000", "turbine1:8092", http.StatusForbidden},
		{"rebound name", "127.0.0.1:5000", "evil.example:8092", http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", bazelmopv1connect.BazelmopServiceDeleteOutputBasesProcedure, strings.NewReader(`{"ids":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = c.remote
		req.Host = c.host
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

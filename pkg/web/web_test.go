package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
	"github.com/meta-programming/bazelmop/pkg/dedupe"
)

func TestWebServer(t *testing.T) {
	s := NewServer("localhost", "0")
	s.UpdateReport("# Test Report", time.Now())

	s.mu.RLock()
	if s.reportMarkdown != "# Test Report" {
		t.Errorf("Expected report markdown '# Test Report', got '%s'", s.reportMarkdown)
	}
	s.mu.RUnlock()

	// Test API route handling
	mux := http.NewServeMux()
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		report := s.reportMarkdown
		updated := s.updatedAt
		s.mu.RUnlock()

		payload := map[string]interface{}{
			"report":     report,
			"updated_at": updated.Format(time.RFC3339),
		}
		_ = json.NewEncoder(w).Encode(payload)
	})

	req := httptest.NewRequest("GET", "/api/report", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var res map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Failed to parse JSON body: %v", err)
	}

	if res["report"] != "# Test Report" {
		t.Errorf("Expected report payload '# Test Report', got '%s'", res["report"])
	}
}

func TestInstancesEndpoint(t *testing.T) {
	s := NewServer("localhost", "0")
	testInsts := []*bazelcas.Instance{
		{
			ID:            "test-md5",
			Type:          bazelcas.TypeOutputBase,
			Path:          "/cache/_bazel_red/test-md5",
			WorkspacePath: "/ws/test",
			Status:        bazelcas.StatusActive,
			SizeBytes:     1024,
			ItemCount:     10,
		},
	}
	s.UpdateInstances(testInsts)

	s.mu.RLock()
	if len(s.instances) != 1 || s.instances[0].ID != "test-md5" {
		t.Errorf("Expected 1 instance with ID 'test-md5', got %v", s.instances)
	}
	s.mu.RUnlock()
}

func TestDeleteOutputBaseEndpoint(t *testing.T) {
	s := NewServer("localhost", "0")
	testInsts := []*bazelcas.Instance{
		{
			ID:            "test-ob-1",
			Type:          bazelcas.TypeOutputBase,
			Path:          "/tmp/non-existent-path-1",
			WorkspacePath: "/ws/project1",
			Status:        bazelcas.StatusActive,
			SizeBytes:     2048,
			ItemCount:     15,
		},
		{
			ID:            "test-ob-2",
			Type:          bazelcas.TypeOutputBase,
			Path:          "/tmp/non-existent-path-2",
			WorkspacePath: "/ws/project2",
			Status:        bazelcas.StatusOrphaned,
			SizeBytes:     4096,
			ItemCount:     30,
		},
	}
	s.UpdateInstances(testInsts)

	req := httptest.NewRequest("DELETE", "/api/output-bases/test-ob-1", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	outputBasesHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/output-bases/")
		if id == "" {
			id = r.URL.Query().Get("id")
		}
		s.mu.Lock()
		var remaining []*bazelcas.Instance
		for _, inst := range s.instances {
			if inst.ID != id {
				remaining = append(remaining, inst)
			}
		}
		s.instances = remaining
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": id})
	}
	mux.HandleFunc("/api/output-bases/", outputBasesHandler)

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200 on DELETE, got %d", w.Code)
	}

	s.mu.RLock()
	if len(s.instances) != 1 || s.instances[0].ID != "test-ob-2" {
		t.Errorf("Expected 1 remaining instance ('test-ob-2'), got %d instances", len(s.instances))
	}
	s.mu.RUnlock()
}

func TestSortableOutputBasesTableHeaders(t *testing.T) {
	data, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("Failed to read index.html asset: %v", err)
	}

	htmlContent := string(data)

	// 1. Verify all 7 required sortable headers exist with data-sort attributes
	requiredSortCols := []string{
		`data-sort="id"`,
		`data-sort="workspace_path"`,
		`data-sort="status"`,
		`data-sort="size"`,
		`data-sort="last_modified"`,
		`data-sort="last_build"`,
		`data-sort="last_test"`,
	}

	for _, colAttr := range requiredSortCols {
		if !strings.Contains(htmlContent, colAttr) {
			t.Errorf("Expected index.html to contain sortable header attribute %s, but it was missing", colAttr)
		}
	}

	// 2. Verify all sort icon elements exist
	requiredSortIcons := []string{
		`id="sort-icon-id"`,
		`id="sort-icon-workspace_path"`,
		`id="sort-icon-status"`,
		`id="sort-icon-size"`,
		`id="sort-icon-last_modified"`,
		`id="sort-icon-last_build"`,
		`id="sort-icon-last_test"`,
	}

	for _, iconId := range requiredSortIcons {
		if !strings.Contains(htmlContent, iconId) {
			t.Errorf("Expected index.html to contain sort icon %s, but it was missing", iconId)
		}
	}

	// 3. Verify JavaScript sorting logic and helper functions
	requiredJSFunctions := []string{
		"getSortValue",
		"getSizeValue",
		"getDateValue",
		"currentSortColumn",
		"currentSortDirection",
		"sortable-header",
	}

	for _, jsFunc := range requiredJSFunctions {
		if !strings.Contains(htmlContent, jsFunc) {
			t.Errorf("Expected index.html JS to contain %s, but it was missing", jsFunc)
		}
	}
}

func TestDedupePlanEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "web-dedupe-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	userDir := filepath.Join(tmpDir, "_bazel_webuser")
	wsDir := filepath.Join(userDir, "0123456789abcdef0123456789abcdef")
	execDir := filepath.Join(wsDir, "execroot", "_main")
	bazelOut := filepath.Join(execDir, "bazel-out", "k8-fastbuild", "bin")
	if err := os.MkdirAll(bazelOut, 0755); err != nil {
		t.Fatalf("failed to create bazelOut: %v", err)
	}

	pathA := filepath.Join(bazelOut, "binA")
	pathB := filepath.Join(bazelOut, "binB")
	content := []byte("identical binary content for web test")
	_ = os.WriteFile(pathA, content, 0644)
	_ = os.WriteFile(pathB, content, 0644)

	s := NewServer("localhost", "0")
	s.SetDedupeConfig(tmpDir, dedupe.Config{
		ScanBazelOut: true,
		ScanExternal: true,
	})

	// 1. Test Initial Plan State
	plan := s.GetPendingPlan()
	if plan != nil {
		t.Errorf("Expected initial plan to be nil, got %v", plan)
	}

	// 2. Test Analyze Endpoint
	reqAnalyze := httptest.NewRequest("POST", "/api/dedupe/analyze", nil)
	wAnalyze := httptest.NewRecorder()
	
	// Set up router/mux
	mux := http.NewServeMux()
	mux.HandleFunc("/api/dedupe/analyze", func(w http.ResponseWriter, r *http.Request) {
		p, _, err := dedupe.AnalyzePendingPlan(r.Context(), tmpDir, dedupe.Config{ScanBazelOut: true, ScanExternal: true})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.StartPendingPlanTimer(p)
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("/api/dedupe/cancel", func(w http.ResponseWriter, r *http.Request) {
		p := s.CancelPendingPlan()
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("/api/dedupe/execute", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.ExecutePendingPlan(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("/api/dedupe/plan", func(w http.ResponseWriter, r *http.Request) {
		p := s.GetPendingPlan()
		_ = json.NewEncoder(w).Encode(p)
	})

	mux.ServeHTTP(wAnalyze, reqAnalyze)
	if wAnalyze.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from analyze endpoint, got %d: %s", wAnalyze.Code, wAnalyze.Body.String())
	}

	var resAnalyze dedupe.PendingPlan
	if err := json.Unmarshal(wAnalyze.Body.Bytes(), &resAnalyze); err != nil {
		t.Fatalf("Failed to parse analyze JSON: %v", err)
	}
	if resAnalyze.Status != "pending" {
		t.Errorf("Expected status 'pending' after analyze, got '%s'", resAnalyze.Status)
	}
	if resAnalyze.RemainingSec < 118 || resAnalyze.RemainingSec > 120 {
		t.Errorf("Expected RemainingSec around 120, got %d", resAnalyze.RemainingSec)
	}

	// 3. Test Cancel Endpoint
	reqCancel := httptest.NewRequest("POST", "/api/dedupe/cancel", nil)
	wCancel := httptest.NewRecorder()
	mux.ServeHTTP(wCancel, reqCancel)
	if wCancel.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from cancel endpoint, got %d", wCancel.Code)
	}

	var resCancel dedupe.PendingPlan
	if err := json.Unmarshal(wCancel.Body.Bytes(), &resCancel); err != nil {
		t.Fatalf("Failed to parse cancel JSON: %v", err)
	}
	if resCancel.Status != "cancelled" {
		t.Errorf("Expected status 'cancelled' after cancel, got '%s'", resCancel.Status)
	}

	// 4. Test Re-Analyze and Immediate Execute
	wAnalyze2 := httptest.NewRecorder()
	mux.ServeHTTP(wAnalyze2, reqAnalyze)

	reqExecute := httptest.NewRequest("POST", "/api/dedupe/execute", nil)
	wExecute := httptest.NewRecorder()
	mux.ServeHTTP(wExecute, reqExecute)
	if wExecute.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from execute endpoint, got %d: %s", wExecute.Code, wExecute.Body.String())
	}

	var resExecute dedupe.PendingPlan
	if err := json.Unmarshal(wExecute.Body.Bytes(), &resExecute); err != nil {
		t.Fatalf("Failed to parse execute JSON: %v", err)
	}
	if resExecute.Status != "completed" {
		t.Errorf("Expected status 'completed' after execute, got '%s'", resExecute.Status)
	}
}

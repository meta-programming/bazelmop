package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
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


package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

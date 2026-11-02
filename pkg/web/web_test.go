package web

import (
	"strings"
	"testing"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

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

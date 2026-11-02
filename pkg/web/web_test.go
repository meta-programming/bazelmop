package web

import (
	"regexp"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	bazelmopv1 "github.com/meta-programming/bazelmop/gen/bazelmop/v1"
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

// TestPageCallsRealMethods checks that every API method the dashboard
// page calls exists in the service, so a rename in the proto cannot leave
// the page calling a method that is gone.
func TestPageCallsRealMethods(t *testing.T) {
	data, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	methods := bazelmopv1.File_bazelmop_v1_bazelmop_proto.Services().ByName("BazelmopService").Methods()

	calls := regexp.MustCompile(`\b(?:call|watch)\("([A-Za-z]+)"`).FindAllStringSubmatch(string(data), -1)
	if len(calls) == 0 {
		t.Fatal("found no API calls in index.html")
	}
	for _, c := range calls {
		if methods.ByName(protoreflect.Name(c[1])) == nil {
			t.Errorf("index.html calls %s, which BazelmopService does not define", c[1])
		}
	}
}

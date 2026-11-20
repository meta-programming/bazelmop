package bazelcas

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestServerRunning(t *testing.T) {
	write := func(t *testing.T, pid string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "server"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "server", "server.pid.txt"), []byte(pid), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	if !ServerRunning(write(t, strconv.Itoa(os.Getpid())+"\n")) {
		t.Error("ServerRunning(own pid) = false, want true")
	}
	// PIDs are capped well below this on Linux.
	if ServerRunning(write(t, "2147483646")) {
		t.Error("ServerRunning(unused pid) = true, want false")
	}
	if ServerRunning(write(t, "not a pid")) {
		t.Error("ServerRunning(garbage) = true, want false")
	}
	if ServerRunning(t.TempDir()) {
		t.Error("ServerRunning(no pid file) = true, want false")
	}
}

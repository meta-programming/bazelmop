package bazelcas

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ServerRunning reports whether the Bazel server that owns the output base
// at path is still running, from the PID in server/server.pid.txt.
//
// A PID that has been reused by another process also reads as running,
// which errs on the side of keeping the output base.
func ServerRunning(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, "server", "server.pid.txt"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

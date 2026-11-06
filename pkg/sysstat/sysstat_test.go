package sysstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMemory(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"memory.current": "4096\n",
		"memory.high":    "max\n",
		"memory.max":     "6442450944\n",
		"memory.stat":    "anon 1000\nfile 200\nkernel 2800\nslab 2700\n",
		"memory.events":  "low 0\nhigh 566\nmax 0\noom 0\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m, err := ReadMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{Current: 4096, High: 0, Max: 6442450944, Anon: 1000, File: 200, Kernel: 2800, HighEvents: 566}
	if m != want {
		t.Errorf("ReadMemory = %+v, want %+v", m, want)
	}
}

func TestSelfMemoryAndDisk(t *testing.T) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("no cgroup v2 hierarchy")
	}
	if m, err := SelfMemory(); err != nil || m.Current <= 0 {
		t.Errorf("SelfMemory = %+v, %v", m, err)
	}
	if d, err := DiskUsage(t.TempDir()); err != nil || d.Total <= 0 || d.Free > d.Total {
		t.Errorf("DiskUsage = %+v, %v", d, err)
	}
}

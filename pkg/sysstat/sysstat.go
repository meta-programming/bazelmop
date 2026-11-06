// Package sysstat reads the memory charged to this process's cgroup and the
// space left on a filesystem.
package sysstat

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Memory is the memory charged to a cgroup v2 group.
type Memory struct {
	// Current is everything charged to the group, including kernel memory
	// such as inode and directory caches.
	Current int64
	// High and Max are the group's memory.high and memory.max limits. Zero
	// means no limit.
	High, Max int64
	Anon      int64 // process memory: heap, stacks
	File      int64 // page cache
	Kernel    int64 // kernel memory, mostly filesystem caches
	// HighEvents counts how often the group went over High and the kernel
	// reclaimed memory from it.
	HighEvents int64
}

// cgroupRoot is where the cgroup v2 hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// SelfMemory reads the memory of the cgroup this process runs in.
func SelfMemory() (Memory, error) {
	dir, err := selfCgroupDir()
	if err != nil {
		return Memory{}, err
	}
	return ReadMemory(dir)
}

// ReadMemory reads the memory files of the cgroup directory dir.
func ReadMemory(dir string) (Memory, error) {
	var m Memory
	var err error
	if m.Current, err = readInt(filepath.Join(dir, "memory.current")); err != nil {
		return Memory{}, err
	}
	m.High, _ = readInt(filepath.Join(dir, "memory.high"))
	m.Max, _ = readInt(filepath.Join(dir, "memory.max"))

	stat, err := readKeyed(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return Memory{}, err
	}
	m.Anon, m.File, m.Kernel = stat["anon"], stat["file"], stat["kernel"]

	if events, err := readKeyed(filepath.Join(dir, "memory.events")); err == nil {
		m.HighEvents = events["high"]
	}
	return m, nil
}

// selfCgroupDir returns this process's cgroup v2 directory.
func selfCgroupDir() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(cgroupRoot, rel), nil
		}
	}
	return "", errors.New("no cgroup v2 entry in /proc/self/cgroup")
}

// readInt reads a file holding one integer, or "max" for no limit (zero).
func readInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// readKeyed reads a file of "key value" lines.
func readKeyed(path string) (map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make(map[string]int64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out[k] = n
		}
	}
	return out, sc.Err()
}

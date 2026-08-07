package bazelcas

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InstanceType represents the category of a Bazel cache instance.
type InstanceType string

const (
	// TypeOutputBase identifies an output base workspace cache directory.
	TypeOutputBase InstanceType = "Output Base"
	// TypeRepoCache identifies a content-addressable repository cache directory.
	TypeRepoCache InstanceType = "Repo Cache"
)

// InstanceStatus represents whether the workspace associated with an output base exists.
type InstanceStatus string

const (
	// StatusActive indicates the associated workspace directory exists on disk.
	StatusActive InstanceStatus = "Active"
	// StatusOrphaned indicates the associated workspace directory is missing or deleted.
	StatusOrphaned InstanceStatus = "Orphaned"
)

// Instance represents a discovered Bazel cache instance (Output Base or Repo Cache).
type Instance struct {
	// ID is the unique identifier for the instance (MD5 hash for Output Base, directory name for Repo Cache).
	ID string `json:"id"`
	// Type specifies whether the instance is an Output Base or Repo Cache.
	Type InstanceType `json:"type"`
	// Path is the absolute filesystem path to the instance directory.
	Path string `json:"path"`
	// WorkspacePath is the resolved workspace directory on disk, or "N/A" for Repo Cache.
	WorkspacePath string `json:"workspace_path"`
	// Status indicates if the workspace path exists on disk (Active vs Orphaned).
	Status InstanceStatus `json:"status"`
	// SizeBytes is the total disk usage of the instance in bytes.
	SizeBytes int64 `json:"size_bytes"`
	// ItemCount is the total number of files contained within the instance.
	ItemCount int64 `json:"item_count"`
	// LastModified is the maximum mtime timestamp across all files in the instance.
	LastModified time.Time `json:"last_modified"`
	// LastBuildTime is the maximum mtime timestamp of build profile, action cache, or server start files.
	LastBuildTime time.Time `json:"last_build_time"`
	// LastTestTime is the maximum mtime timestamp of test log and test xml result files.
	LastTestTime time.Time `json:"last_test_time"`
}

// GCOptions defines filtering criteria for garbage collection candidate selection.
type GCOptions struct {
	// TTL is the time-to-live threshold. Instances older than TTL are selected.
	TTL time.Duration
	// TargetFreeBytes is the target disk space to reclaim in bytes.
	TargetFreeBytes int64
	// OrphanedOnly filters only orphaned instances if true.
	OrphanedOnly bool
}

// ResolveWorkspacePath attempts to read the original workspace path for an Output Base.
// It reads from `<outputBasePath>/DO_NOT_BUILD_HERE` first, and falls back to
// searching for `--workspace_directory=` in `<outputBasePath>/server/cmdline`.
func ResolveWorkspacePath(sys fs.FS, outputBaseRelPath string) string {
	// 1. Try reading DO_NOT_BUILD_HERE
	doNotBuildFile := filepath.Join(outputBaseRelPath, "DO_NOT_BUILD_HERE")
	if data, err := fs.ReadFile(sys, doNotBuildFile); err == nil {
		pathStr := strings.TrimSpace(string(data))
		if pathStr != "" {
			return pathStr
		}
	}

	// 2. Fallback: try reading server/cmdline
	cmdlineFile := filepath.Join(outputBaseRelPath, "server", "cmdline")
	if f, err := sys.Open(cmdlineFile); err == nil {
		defer f.Close()
		data, err := io.ReadAll(f)
		if err == nil {
			cmdStr := string(data)
			if idx := strings.Index(cmdStr, "--workspace_directory="); idx != -1 {
				sub := cmdStr[idx+len("--workspace_directory="):]
				// Truncate at space, NUL byte, or newline
				endIdx := strings.IndexAny(sub, " \x00\r\n")
				if endIdx != -1 {
					sub = sub[:endIdx]
				}
				sub = strings.TrimSpace(sub)
				if sub != "" {
					return sub
				}
			}
		}
	}

	return ""
}

// DetermineWorkspaceStatus checks if a given workspace path exists on disk.
// If workspacePath is empty, or if stats fail, returns [StatusOrphaned].
func DetermineWorkspaceStatus(sys fs.FS, workspacePath string) InstanceStatus {
	if workspacePath == "" || workspacePath == "N/A" {
		return StatusOrphaned
	}

	// Check on host filesystem via os.Stat first if path is absolute
	if filepath.IsAbs(workspacePath) {
		if _, err := os.Stat(workspacePath); err == nil {
			return StatusActive
		}
	}

	// Fallback check within sys for virtual testing filesystems
	relPath := strings.TrimPrefix(workspacePath, "/")
	if _, err := fs.Stat(sys, relPath); err == nil {
		return StatusActive
	}

	return StatusOrphaned
}

// DiscoverInstances walks the Bazel root cache directory and returns all discovered
// [Instance] entries representing Output Bases and Repository Caches.
func DiscoverInstances(sys fs.FS, rootPath string) ([]*Instance, error) {
	entries, err := fs.ReadDir(sys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read root cache directory: %w", err)
	}

	type scanJob struct {
		relDir   string
		id       string
		instType InstanceType
		wsPath   string
		status   InstanceStatus
	}

	var jobs []scanJob

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "_bazel_") {
			continue
		}

		userRelDir := entry.Name()

		// 1. Discover Repo Cache under _bazel_<user>/cache/repos/v1/content_addressable
		repoCacheRelDir := filepath.Join(userRelDir, "cache", "repos", "v1", "content_addressable")
		if repoInfo, err := fs.Stat(sys, repoCacheRelDir); err == nil && repoInfo.IsDir() {
			jobs = append(jobs, scanJob{
				relDir:   repoCacheRelDir,
				id:       "repo-cache",
				instType: TypeRepoCache,
				wsPath:   "N/A",
				status:   StatusActive,
			})
		}

		// 2. Discover Output Base instances (32-character MD5 subdirectories)
		userEntries, err := fs.ReadDir(sys, userRelDir)
		if err != nil {
			continue
		}

		for _, userSub := range userEntries {
			if !userSub.IsDir() || !isMD5(userSub.Name()) {
				continue
			}

			md5Str := userSub.Name()
			outputBaseRelDir := filepath.Join(userRelDir, md5Str)

			wsPath := ResolveWorkspacePath(sys, outputBaseRelDir)
			status := StatusOrphaned
			if wsPath != "" {
				status = DetermineWorkspaceStatus(sys, wsPath)
			} else {
				wsPath = "N/A"
			}

			jobs = append(jobs, scanJob{
				relDir:   outputBaseRelDir,
				id:       md5Str,
				instType: TypeOutputBase,
				wsPath:   wsPath,
				status:   status,
			})
		}
	}

	if len(jobs) == 0 {
		return nil, nil
	}

	// Concurrent scanning using worker pool
	numWorkers := 16
	if len(jobs) < numWorkers {
		numWorkers = len(jobs)
	}

	jobChan := make(chan scanJob, len(jobs))
	for _, j := range jobs {
		jobChan <- j
	}
	close(jobChan)

	type scanResult struct {
		inst *Instance
	}
	resChan := make(chan scanResult, len(jobs))

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobChan {
				inst := scanInstance(sys, rootPath, j.relDir, j.id, j.instType, j.wsPath, j.status)
				resChan <- scanResult{inst: inst}
			}
		}()
	}

	wg.Wait()
	close(resChan)

	var instances []*Instance
	for res := range resChan {
		instances = append(instances, res.inst)
	}

	return instances, nil
}

// scanInstance calculates disk size, item count, and max mtime for a directory.
func scanInstance(sys fs.FS, rootPath, relDir, id string, instType InstanceType, wsPath string, status InstanceStatus) *Instance {
	absPath := filepath.Join(rootPath, relDir)
	inst := &Instance{
		ID:            id,
		Type:          instType,
		Path:          absPath,
		WorkspacePath: wsPath,
		Status:        status,
	}

	if instType == TypeOutputBase {
		inst.LastBuildTime = resolveLastBuildTime(sys, relDir)
		inst.LastTestTime = resolveLastTestTime(sys, relDir)
	}

	_ = fs.WalkDir(sys, relDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// Skip execroot directory to avoid walking massive symlink trees and double-counting files
		if d.IsDir() && d.Name() == "execroot" && path != relDir {
			return fs.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if info.ModTime().After(inst.LastModified) {
			inst.LastModified = info.ModTime()
		}

		if !d.IsDir() {
			inst.ItemCount++
			inst.SizeBytes += info.Size()
		}

		return nil
	})

	return inst
}

// resolveLastBuildTime calculates the maximum mtime of command.profile.gz, action_cache,
// lock, server/server.starttime, and command-*.profile.gz inside an output base.
func resolveLastBuildTime(sys fs.FS, relDir string) time.Time {
	var maxTime time.Time

	fixedPaths := []string{
		filepath.Join(relDir, "command.profile.gz"),
		filepath.Join(relDir, "action_cache"),
		filepath.Join(relDir, "lock"),
		filepath.Join(relDir, "server", "server.starttime"),
	}

	for _, p := range fixedPaths {
		if info, err := fs.Stat(sys, p); err == nil {
			if info.ModTime().After(maxTime) {
				maxTime = info.ModTime()
			}
		}
	}

	if entries, err := fs.ReadDir(sys, relDir); err == nil {
		for _, entry := range entries {
			if matched, _ := filepath.Match("command-*.profile.gz", entry.Name()); matched {
				if info, err := entry.Info(); err == nil {
					if info.ModTime().After(maxTime) {
						maxTime = info.ModTime()
					}
				}
			}
		}
	}

	return maxTime
}

// resolveLastTestTime scans execroot/*/bazel-out/*/testlogs (or bazel-out/*/testlogs)
// for the maximum mtime across test.log and test.xml files.
func resolveLastTestTime(sys fs.FS, relDir string) time.Time {
	var maxTime time.Time
	seenDirs := make(map[string]bool)

	var testlogDirs []string
	addDir := func(p string) {
		if seenDirs[p] {
			return
		}
		if info, err := fs.Stat(sys, p); err == nil && info.IsDir() {
			seenDirs[p] = true
			testlogDirs = append(testlogDirs, p)
		}
	}

	// 1. execroot/*/bazel-out/*/testlogs (and execroot/*/testlogs)
	execrootPath := filepath.Join(relDir, "execroot")
	if wsEntries, err := fs.ReadDir(sys, execrootPath); err == nil {
		for _, ws := range wsEntries {
			if !ws.IsDir() {
				continue
			}
			wsPath := filepath.Join(execrootPath, ws.Name())
			bazelOut := filepath.Join(wsPath, "bazel-out")
			if cfgEntries, err := fs.ReadDir(sys, bazelOut); err == nil {
				for _, cfg := range cfgEntries {
					if !cfg.IsDir() {
						continue
					}
					addDir(filepath.Join(bazelOut, cfg.Name(), "testlogs"))
				}
			}
			addDir(filepath.Join(wsPath, "testlogs"))
		}
	}

	// 2. bazel-out/*/testlogs (and bazel-out/testlogs)
	bazelOutPath := filepath.Join(relDir, "bazel-out")
	if cfgEntries, err := fs.ReadDir(sys, bazelOutPath); err == nil {
		for _, cfg := range cfgEntries {
			if !cfg.IsDir() {
				continue
			}
			addDir(filepath.Join(bazelOutPath, cfg.Name(), "testlogs"))
		}
	}
	addDir(filepath.Join(bazelOutPath, "testlogs"))

	for _, dir := range testlogDirs {
		_ = fs.WalkDir(sys, dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && (d.Name() == "test.log" || d.Name() == "test.xml") {
				if info, err := d.Info(); err == nil {
					if info.ModTime().After(maxTime) {
						maxTime = info.ModTime()
					}
				}
			}
			return nil
		})
	}

	return maxTime
}

// FormatRelativeTime formats a time.Time value into a relative time string (e.g. "3h ago", "2d ago", or "Never").
func FormatRelativeTime(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	diff := now.Sub(t)
	if diff < 0 {
		return "Just now"
	}
	secs := int64(diff.Seconds())
	if secs < 60 {
		return "Just now"
	}
	mins := secs / 60
	if mins < 60 {
		return fmt.Sprintf("%dm ago", mins)
	}
	hours := mins / 60
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	if days < 30 {
		return fmt.Sprintf("%dd ago", days)
	}
	months := days / 30
	if months < 12 {
		return fmt.Sprintf("%dmo ago", months)
	}
	years := days / 365
	return fmt.Sprintf("%dy ago", years)
}

// SortInstances sorts instances in-place by the specified field ("lru", "size", "id").
func SortInstances(instances []*Instance, sortBy string) {
	sort.Slice(instances, func(i, j int) bool {
		switch strings.ToLower(sortBy) {
		case "size":
			return instances[i].SizeBytes > instances[j].SizeBytes
		case "id":
			return instances[i].ID < instances[j].ID
		case "lru":
			fallthrough
		default:
			// Oldest last modified first (LRU candidate)
			if instances[i].LastModified.Equal(instances[j].LastModified) {
				return instances[i].ID < instances[j].ID
			}
			return instances[i].LastModified.Before(instances[j].LastModified)
		}
	})
}

// FilterInstances selects candidate instances matching the specified [GCOptions].
func FilterInstances(instances []*Instance, opts GCOptions, now time.Time) []*Instance {
	var candidates []*Instance

	// 1. Initial pass: filter by TTL and Orphaned criteria
	for _, inst := range instances {
		if opts.OrphanedOnly && inst.Status != StatusOrphaned {
			continue
		}

		if opts.TTL > 0 {
			age := now.Sub(inst.LastModified)
			if age < opts.TTL {
				continue
			}
		}

		candidates = append(candidates, inst)
	}

	// 2. Sort candidates by LRU (oldest mtime first)
	SortInstances(candidates, "lru")

	// 3. If TargetFreeBytes is specified, limit candidate set to meet target size
	if opts.TargetFreeBytes > 0 && len(candidates) > 0 {
		var accumulatedBytes int64
		var targetSet []*Instance

		for _, cand := range candidates {
			targetSet = append(targetSet, cand)
			accumulatedBytes += cand.SizeBytes
			if accumulatedBytes >= opts.TargetFreeBytes {
				break
			}
		}

		return targetSet
	}

	return candidates
}

// ParseTTL parses duration strings supporting 'd' (days) and 'w' (weeks) suffixes.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	unit := s[len(s)-1]
	valStr := s[:len(s)-1]

	switch unit {
	case 'd', 'D':
		days, err := strconv.Atoi(valStr)
		if err != nil {
			return 0, fmt.Errorf("invalid day value in TTL %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	case 'w', 'W':
		weeks, err := strconv.Atoi(valStr)
		if err != nil {
			return 0, fmt.Errorf("invalid week value in TTL %q: %w", s, err)
		}
		return time.Duration(weeks) * 7 * 24 * time.Hour, nil
	default:
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid TTL duration format %q: %w", s, err)
		}
		return d, nil
	}
}

// ParseSize parses human-readable disk size strings (e.g. 20GB, 500MB, 100KB, 1024B).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}

	var multiplier int64 = 1
	if strings.HasSuffix(s, "GB") || strings.HasSuffix(s, "G") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "G")
	} else if strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "M") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "M")
	} else if strings.HasSuffix(s, "KB") || strings.HasSuffix(s, "K") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "K")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format %q: %w", s, err)
	}

	return int64(val * float64(multiplier)), nil
}

// FormatSize formats a byte count into a clean human-readable size string.
func FormatSize(bytes int64) string {
	const (
		_KB = 1024
		_MB = 1024 * _KB
		_GB = 1024 * _MB
	)

	switch {
	case bytes >= _GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(_GB))
	case bytes >= _MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(_MB))
	case bytes >= _KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(_KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ReadPrompt confirms a yes/no response from an interactive reader.
func ReadPrompt(r io.Reader, prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	scanner := bufio.NewScanner(r)
	if scanner.Scan() {
		text := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return text == "y" || text == "yes"
	}
	return false
}

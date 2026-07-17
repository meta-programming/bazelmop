package dedupe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// FileEntry represents filesystem metadata for a scanned file.
type FileEntry struct {
	Path  string
	Size  int64
	Inode uint64
	Dev   uint64
	Hash  string // Resolved from CAS or calculated selectively for reporting/matching
}

// Config holds runtime options for the deduplication execution.
type Config struct {
	DryRun        bool
	PreferReflink bool
	MinReportSize int64
	Verbose       bool
	ScanExternal  bool // Whether to scan and deduplicate external/ (extracted third-party repos)
	ScanBazelOut  bool // Whether to scan and deduplicate bazel-out/ (locally built targets)
}

// Deduplicator manages the walking, matching, and linking process.
type Deduplicator struct {
	config Config
	casMap map[uint64]string // Inode -> SHA-256 hash resolved from repository cache
}

// NewDeduplicator instantiates a new deduplicator runner.
func NewDeduplicator(cfg Config) *Deduplicator {
	return &Deduplicator{
		config: cfg,
		casMap: make(map[uint64]string),
	}
}

// scanCAS walks the user repository cache CAS and indexes Inode -> Hash.
func (d *Deduplicator) scanCAS(root string) error {
	userEntries, err := os.ReadDir(root)
	if err != nil {
		// Cache root might not exist if no builds were run
		return nil
	}

	for _, userEntry := range userEntries {
		if !userEntry.IsDir() || !strings.HasPrefix(userEntry.Name(), "_bazel_") {
			continue
		}

		userPath := filepath.Join(root, userEntry.Name())
		repoCASPath := filepath.Join(userPath, "cache", "repos", "v1", "content_addressable", "sha256")

		shaEntries, err := os.ReadDir(repoCASPath)
		if err != nil {
			continue
		}

		for _, shaEntry := range shaEntries {
			if !shaEntry.IsDir() || len(shaEntry.Name()) != 64 {
				continue
			}

			filePath := filepath.Join(repoCASPath, shaEntry.Name(), "file")
			info, err := os.Lstat(filePath)
			if err != nil {
				continue
			}

			if info.Mode().IsRegular() {
				if stat, ok := info.Sys().(*syscall.Stat_t); ok {
					d.casMap[stat.Ino] = shaEntry.Name()
				}
			}
		}
	}

	if d.config.Verbose && len(d.casMap) > 0 {
		fmt.Printf("Indexed %d CAS entries from repository cache\n", len(d.casMap))
	}
	return nil
}

// Scan walks the workspaces and builds the list of file entries.
func (d *Deduplicator) Scan(ctx context.Context, root string) ([]FileEntry, error) {
	// First index the CAS
	if err := d.scanCAS(root); err != nil {
		return nil, fmt.Errorf("failed to scan CAS: %w", err)
	}

	var entries []FileEntry

	userEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, userEntry := range userEntries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !userEntry.IsDir() || !strings.HasPrefix(userEntry.Name(), "_bazel_") {
			continue
		}

		userPath := filepath.Join(root, userEntry.Name())

		// Read workspace MD5 base directories
		workspaceEntries, err := os.ReadDir(userPath)
		if err != nil {
			continue
		}

		for _, wsEntry := range workspaceEntries {
			if !wsEntry.IsDir() || len(wsEntry.Name()) != 32 {
				continue
			}

			wsPath := filepath.Join(userPath, wsEntry.Name())

			// 1. Scan external/ if configured
			if d.config.ScanExternal {
				targetDir := filepath.Join(wsPath, "external")
				_ = d.scanDirectory(ctx, targetDir, &entries)
			}

			// 2. Scan bazel-out/ (located in execroot/[workspace]/bazel-out)
			if d.config.ScanBazelOut {
				execrootPath := filepath.Join(wsPath, "execroot")
				execrootEntries, err := os.ReadDir(execrootPath)
				if err == nil {
					for _, execEntry := range execrootEntries {
						if execEntry.IsDir() {
							targetDir := filepath.Join(execrootPath, execEntry.Name(), "bazel-out")
							_ = d.scanDirectory(ctx, targetDir, &entries)
						}
					}
				}
			}
		}
	}

	return entries, nil
}

// scanDirectory recursively walks a target directory and appends file entries.
func (d *Deduplicator) scanDirectory(ctx context.Context, targetDir string, entries *[]FileEntry) error {
	return filepath.WalkDir(targetDir, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip walk errors
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if de.IsDir() {
			return nil
		}

		info, err := de.Info()
		if err != nil {
			return nil
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}

		// Build entry
		entry := FileEntry{
			Path:  path,
			Size:  info.Size(),
			Inode: stat.Ino,
			Dev:   stat.Dev,
		}

		// Fast CAS lookup
		if hash, ok := d.casMap[stat.Ino]; ok {
			entry.Hash = hash
		}

		*entries = append(*entries, entry)
		return nil
	})
}

// SizeDev represents the key for grouping files by size and device.
type SizeDev struct {
	Size int64
	Dev  uint64
}

// HashKey represents the key for grouping identical files.
type HashKey struct {
	Size int64
	Dev  uint64
	Hash string
}

// EquivalenceClass represents a group of identical files.
type EquivalenceClass struct {
	Representative FileEntry
	Duplicates     []FileEntry
}

// computeSHA256 hashes a file.
func computeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Deduplicate executes matching and link replacement.
func (d *Deduplicator) Deduplicate(ctx context.Context, entries []FileEntry) error {
	// 1. Group by (Size, Dev)
	sizeGroups := make(map[SizeDev][]int) // value holds indices in the entries array
	for i, entry := range entries {
		key := SizeDev{Size: entry.Size, Dev: entry.Dev}
		sizeGroups[key] = append(sizeGroups[key], i)
	}

	// 2. Find which files are in groups of size >= 2 and do NOT have their hash pre-resolved
	var indicesToHash []int
	for _, indices := range sizeGroups {
		if len(indices) < 2 {
			continue
		}
		for _, idx := range indices {
			if entries[idx].Hash == "" && entries[idx].Size > 0 {
				indicesToHash = append(indicesToHash, idx)
			}
		}
	}

	fmt.Printf("Calculating SHA-256 hashes for %d unlinked candidate files (skipping %d empty and %d unique-sized files)...\n",
		len(indicesToHash), len(entries)-len(indicesToHash)-len(d.casMap), len(entries)-len(indicesToHash))

	if len(indicesToHash) == 0 {
		return d.processMatches(ctx, entries)
	}

	// Concurrent worker pool for hashing
	numWorkers := runtime.NumCPU() * 2
	if numWorkers < 8 {
		numWorkers = 8
	}

	fmt.Printf("Using %d parallel workers to accelerate disk I/O & hashing...\n", numWorkers)

	jobs := make(chan int, len(indicesToHash))
	for _, idx := range indicesToHash {
		jobs <- idx
	}
	close(jobs)

	var hashedCount uint64
	var wg sync.WaitGroup
	lastLogTime := time.Now()
	var logMutex sync.Mutex

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				hash, err := computeSHA256(entries[idx].Path)
				if err != nil {
					if d.config.Verbose {
						fmt.Printf("Warning: failed to hash %s: %v\n", entries[idx].Path, err)
					}
					continue
				}
				entries[idx].Hash = hash

				currentHashed := atomic.AddUint64(&hashedCount, 1)

				// Log progress safely
				logMutex.Lock()
				if time.Since(lastLogTime) >= 5*time.Second {
					lastLogTime = time.Now()
					percent := float64(currentHashed) * 100.0 / float64(len(indicesToHash))
					fmt.Printf("Progress: Hashed %d/%d files (%.1f%%)...\n", currentHashed, len(indicesToHash), percent)
				}
				logMutex.Unlock()
			}
		}()
	}

	wg.Wait()

	// Handle cancellations
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// For empty files in candidate groups, assign "empty" hash
	for _, indices := range sizeGroups {
		if len(indices) < 2 {
			continue
		}
		for _, idx := range indices {
			if entries[idx].Hash == "" && entries[idx].Size == 0 {
				entries[idx].Hash = "empty"
			}
		}
	}

	return d.processMatches(ctx, entries)
}

// processMatches groups identical files and executes link swaps.
func (d *Deduplicator) processMatches(ctx context.Context, entries []FileEntry) error {
	// Group by (Size, Dev, Hash)
	groups := make(map[HashKey][]FileEntry)
	for _, entry := range entries {
		if entry.Hash == "" {
			continue // Skip files that failed to hash or are unique-sized
		}
		key := HashKey{Size: entry.Size, Dev: entry.Dev, Hash: entry.Hash}
		groups[key] = append(groups[key], entry)
	}

	// Track matches to link
	var matchesToLink []EquivalenceClass

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		// Partition the group based on inodes.
		representative := group[0]
		var duplicates []FileEntry

		for _, file := range group[1:] {
			if file.Inode != representative.Inode {
				duplicates = append(duplicates, file)
			}
		}

		if len(duplicates) > 0 {
			matchesToLink = append(matchesToLink, EquivalenceClass{
				Representative: representative,
				Duplicates:     duplicates,
			})
		}
	}

	// Calculate space statistics
	var totalApparentSize int64
	var totalDiskFootprint int64
	var totalReclaimable int64

	inodeSizes := make(map[uint64]int64)
	for _, entry := range entries {
		totalApparentSize += entry.Size
		inodeSizes[entry.Inode] = entry.Size
	}

	for _, sz := range inodeSizes {
		totalDiskFootprint += sz
	}

	for _, match := range matchesToLink {
		totalReclaimable += int64(len(match.Duplicates)) * match.Representative.Size
	}

	// Generate and print reports
	d.printGlobalMetrics(len(entries), totalApparentSize, totalDiskFootprint, totalReclaimable)
	d.printWorkspaceBreakdown(entries, matchesToLink)
	d.printLargeDuplicates(matchesToLink)

	if d.config.DryRun {
		fmt.Println("\n[Dry Run] No files were modified.")
		return nil
	}

	// Execute linking
	fmt.Printf("\nPerforming deduplication linking for %d groups...\n", len(matchesToLink))
	for _, match := range matchesToLink {
		source := match.Representative.Path
		for _, dup := range match.Duplicates {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Perform link atomically
			err := d.atomicLink(source, dup.Path)
			if err != nil {
				if d.config.Verbose {
					fmt.Printf("Warning: failed to link %s to %s: %v\n", dup.Path, source, err)
				}
			}
		}
	}

	fmt.Println("Deduplication completed successfully!")
	return nil
}

// extractWorkspace parses the username and workspace MD5 hash from the file path.
func extractWorkspace(path string) (string, string) {
	// Expected format: .../_bazel_[username]/[workspace_md5]/...
	parts := strings.Split(path, string(os.PathSeparator))
	for i, part := range parts {
		if strings.HasPrefix(part, "_bazel_") && i+1 < len(parts) {
			username := strings.TrimPrefix(part, "_bazel_")
			workspace := parts[i+1]
			if len(workspace) == 32 {
				return username, workspace
			}
		}
	}
	return "unknown", "unknown"
}

func (d *Deduplicator) printGlobalMetrics(scannedCount int, apparent, footprint, reclaimable int64) {
	fmt.Println("\n=== Global Space Metrics ===")
	fmt.Printf("Total Scanned Files: %d\n", scannedCount)
	fmt.Printf("Total Apparent Size: %s\n", formatSize(apparent))
	fmt.Printf("Current Disk Footprint: %s\n", formatSize(footprint))
	fmt.Printf("Reclaimable Space: %s\n", formatSize(reclaimable))
}

func (d *Deduplicator) printWorkspaceBreakdown(entries []FileEntry, matches []EquivalenceClass) {
	fmt.Println("\n=== Workspace Breakdown Table ===")
	fmt.Printf("%-32s | %-15s | %-15s | %-15s\n", "Workspace Path / MD5", "Apparent Size", "Disk Footprint", "Reclaimable")
	fmt.Println(strings.Repeat("-", 85))

	// Group entries by workspace MD5
	wsApparent := make(map[string]int64)
	wsInodes := make(map[string]map[uint64]int64)

	for _, entry := range entries {
		_, ws := extractWorkspace(entry.Path)
		wsApparent[ws] += entry.Size
		if wsInodes[ws] == nil {
			wsInodes[ws] = make(map[uint64]int64)
		}
		wsInodes[ws][entry.Inode] = entry.Size
	}

	// Calculate reclaimable per workspace
	wsReclaimable := make(map[string]int64)
	for _, match := range matches {
		for _, dup := range match.Duplicates {
			_, ws := extractWorkspace(dup.Path)
			wsReclaimable[ws] += match.Representative.Size
		}
	}

	for ws, apparent := range wsApparent {
		var footprint int64
		for _, sz := range wsInodes[ws] {
			footprint += sz
		}
		reclaimable := wsReclaimable[ws]
		fmt.Printf("%-32s | %-15s | %-15s | %-15s\n", ws, formatSize(apparent), formatSize(footprint), formatSize(reclaimable))
	}
}

func (d *Deduplicator) printLargeDuplicates(matches []EquivalenceClass) {
	fmt.Println("\n=== Large Duplicates ===")
	count := 0
	for _, match := range matches {
		totalSize := match.Representative.Size * int64(len(match.Duplicates)+1)
		if totalSize < d.config.MinReportSize {
			continue
		}

		count++
		fmt.Printf("Large Duplicate Group: sha256:%s (Size: %s, %d instances)\n",
			match.Representative.Hash, formatSize(match.Representative.Size), len(match.Duplicates)+1)

		// Print representative
		fmt.Printf("  - [Inode: %d] %s (Representative)\n", match.Representative.Inode, match.Representative.Path)
		// Print duplicates
		for _, dup := range match.Duplicates {
			fmt.Printf("  - [Inode: %d] %s\n", dup.Inode, dup.Path)
		}
	}

	if count == 0 {
		fmt.Println("No duplicate groups exceed the minimum report size threshold.")
	}
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// atomicLink replaces target with a link to source atomically.
func (d *Deduplicator) atomicLink(source, target string) error {
	// Check if target is read-only if we are using hard links and not reflinks
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}

	tempPath := target + ".tmp-dedup"

	// Attempt reflink if preferred
	linked := false
	if d.config.PreferReflink {
		err := cloneFile(source, tempPath)
		if err == nil {
			linked = true
		} else if d.config.Verbose {
			fmt.Printf("Reflink failed for %s -> %s: %v. Falling back to hard link...\n", source, tempPath, err)
		}
	}

	if !linked {
		// Hard link safety gate
		if info.Mode()&0222 != 0 {
			return fmt.Errorf("safety gate: file %s is writeable, skipping hard link", target)
		}

		err = os.Link(source, tempPath)
		if err != nil {
			return fmt.Errorf("failed to create hard link: %w", err)
		}
	}

	// Rename temp to target atomically
	err = os.Rename(tempPath, target)
	if err != nil {
		// Cleanup temp
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to atomically replace target: %w", err)
	}

	return nil
}

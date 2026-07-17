package dedupe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
	"github.com/meta-programming/bazelmop/pkg/bazelfiles"
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
	OnProgress    func(status string) // Callback for real-time progress updates
}

// Deduplicator manages the walking, matching, and linking process.
type Deduplicator struct {
	config Config
	casMap map[uint64]string // Inode -> SHA-256 hash resolved from repository cache
}

func (d *Deduplicator) updateProgress(status string) {
	if d.config.OnProgress != nil {
		d.config.OnProgress(status)
	}
}

// NewDeduplicator instantiates a new deduplicator runner.
func NewDeduplicator(cfg Config) *Deduplicator {
	return &Deduplicator{
		config: cfg,
		casMap: make(map[uint64]string),
	}
}

// Scan walks the workspaces and builds the list of file entries using pkg/bazelfiles.
func (d *Deduplicator) Scan(ctx context.Context, root string) ([]FileEntry, error) {
	d.updateProgress("Scanning directories...")
	discovered, err := bazelfiles.Walk(ctx, bazelcas.RootCASPath(root),
		bazelfiles.WithScanExternal(d.config.ScanExternal),
		bazelfiles.WithScanBazelOut(d.config.ScanBazelOut),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to walk directories: %w", err)
	}

	// Index the RepoCache CAS files to d.casMap
	for _, path := range discovered.RepoCacheCAS {
		info, err := os.Lstat(path.String())
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				d.casMap[stat.Ino] = path.Hash()
			}
		}
	}

	if d.config.Verbose && len(d.casMap) > 0 {
		fmt.Printf("Indexed %d CAS entries from repository cache\n", len(d.casMap))
	}

	var entries []FileEntry

	// Process external dependencies
	for _, path := range discovered.ExternalDeps {
		info, err := os.Lstat(path.String())
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				entry := FileEntry{
					Path:  path.String(),
					Size:  info.Size(),
					Inode: stat.Ino,
					Dev:   stat.Dev,
				}
				if hash, ok := d.casMap[stat.Ino]; ok {
					entry.Hash = hash
				}
				entries = append(entries, entry)
			}
		}
	}

	// Process build outputs
	for _, path := range discovered.BuildOutputs {
		info, err := os.Lstat(path.String())
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				entry := FileEntry{
					Path:  path.String(),
					Size:  info.Size(),
					Inode: stat.Ino,
					Dev:   stat.Dev,
				}
				if hash, ok := d.casMap[stat.Ino]; ok {
					entry.Hash = hash
				}
				entries = append(entries, entry)
			}
		}
	}

	return entries, nil
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
func (d *Deduplicator) Deduplicate(ctx context.Context, entries []FileEntry) (string, error) {
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
	d.updateProgress(fmt.Sprintf("Hashing files: %d Group Candidates...", len(indicesToHash)))

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
					d.updateProgress(fmt.Sprintf("Hashing files: %d/%d (%.1f%%)...", currentHashed, len(indicesToHash), percent))
				}
				logMutex.Unlock()
			}
		}()
	}

	wg.Wait()

	// Handle cancellations
	select {
	case <-ctx.Done():
		return "", ctx.Err()
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
func (d *Deduplicator) processMatches(ctx context.Context, entries []FileEntry) (string, error) {
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

	// Generate report
	report := d.generateMarkdownReport(entries, matchesToLink)
	fmt.Println(report)

	if d.config.DryRun {
		fmt.Println("\n[Dry Run] No files were modified.")
		d.updateProgress("Idle")
		return report, nil
	}

	// Execute linking
	d.updateProgress("Performing deduplication linking...")
	fmt.Printf("\nPerforming deduplication linking for %d groups...\n", len(matchesToLink))
	for _, match := range matchesToLink {
		source := match.Representative.Path
		for _, dup := range match.Duplicates {
			select {
			case <-ctx.Done():
				d.updateProgress("Idle")
				return report, ctx.Err()
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
	d.updateProgress("Idle")
	return report, nil
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

func truncateMD5(s string) string {
	if len(s) == 32 {
		return s[:8] + "..."
	}
	return s
}

func (d *Deduplicator) generateMarkdownReport(entries []FileEntry, matches []EquivalenceClass) string {
	var sb strings.Builder

	var countBuildOut, countExternal int
	var sizeBuildOut, sizeExternal int64
	var reclaimBuildOut, reclaimExternal int64
	var dupsBuildOut, dupsExternal int

	// Track files per category
	for _, entry := range entries {
		if strings.Contains(entry.Path, "/bazel-out/") {
			countBuildOut++
			sizeBuildOut += entry.Size
		} else if strings.Contains(entry.Path, "/external/") {
			countExternal++
			sizeExternal += entry.Size
		}
	}

	// Track reclaimable and duplicates per category
	for _, match := range matches {
		for _, dup := range match.Duplicates {
			if strings.Contains(dup.Path, "/bazel-out/") {
				reclaimBuildOut += match.Representative.Size
				dupsBuildOut++
			} else if strings.Contains(dup.Path, "/external/") {
				reclaimExternal += match.Representative.Size
				dupsExternal++
			}
		}
	}

	// Compute totals
	totalScanned := int64(len(entries))
	var totalApparent int64
	for _, entry := range entries {
		totalApparent += entry.Size
	}
	totalReclaimable := reclaimBuildOut + reclaimExternal
	totalDups := dupsBuildOut + dupsExternal

	pctBuildOut := 0.0
	if sizeBuildOut > 0 {
		pctBuildOut = float64(reclaimBuildOut) * 100.0 / float64(sizeBuildOut)
	}
	pctExternal := 0.0
	if sizeExternal > 0 {
		pctExternal = float64(reclaimExternal) * 100.0 / float64(sizeExternal)
	}
	pctTotal := 0.0
	if totalApparent > 0 {
		pctTotal = float64(totalReclaimable) * 100.0 / float64(totalApparent)
	}

	sb.WriteString("# Bazel Cache Deduplication Report\n\n")
	sb.WriteString("## Executive Summary\n\n")
	sb.WriteString("| Category | Files Scanned | Apparent Size | Reclaimable Space | Duplicates | % Reclaimed |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- | :--- | :--- |\n")
	fmt.Fprintf(&sb, "| **1. Locally Compiled Build Outputs (`bazel-out/`)** | %s | %s | %s | %s | %.1f%% |\n",
		formatNumber(int64(countBuildOut)), formatSize(sizeBuildOut), formatSize(reclaimBuildOut), formatNumber(int64(dupsBuildOut)), pctBuildOut)
	fmt.Fprintf(&sb, "| **2. Extracted Repository Sources (`external/`)** | %s | %s | %s | %s | %.1f%% |\n",
		formatNumber(int64(countExternal)), formatSize(sizeExternal), formatSize(reclaimExternal), formatNumber(int64(dupsExternal)), pctExternal)
	fmt.Fprintf(&sb, "| **Total Cache Profile** | **%s** | **%s** | **%s** | **%s** | **%.1f%%** |\n",
		formatNumber(totalScanned), formatSize(totalApparent), formatSize(totalReclaimable), formatNumber(int64(totalDups)), pctTotal)

	// Structure to hold metrics per workspace
	type WSMetrics struct {
		Apparent    int64
		Reclaimable int64
		DupCount    int
	}
	
	wsBuildOut := make(map[string]*WSMetrics)
	wsExternal := make(map[string]*WSMetrics)

	for _, entry := range entries {
		_, ws := extractWorkspace(entry.Path)
		if strings.Contains(entry.Path, "/bazel-out/") {
			if wsBuildOut[ws] == nil {
				wsBuildOut[ws] = &WSMetrics{}
			}
			wsBuildOut[ws].Apparent += entry.Size
		} else if strings.Contains(entry.Path, "/external/") {
			if wsExternal[ws] == nil {
				wsExternal[ws] = &WSMetrics{}
			}
			wsExternal[ws].Apparent += entry.Size
		}
	}

	for _, match := range matches {
		for _, dup := range match.Duplicates {
			_, ws := extractWorkspace(dup.Path)
			if strings.Contains(dup.Path, "/bazel-out/") {
				if wsBuildOut[ws] == nil {
					wsBuildOut[ws] = &WSMetrics{}
				}
				wsBuildOut[ws].Reclaimable += match.Representative.Size
				wsBuildOut[ws].DupCount++
			} else if strings.Contains(dup.Path, "/external/") {
				if wsExternal[ws] == nil {
					wsExternal[ws] = &WSMetrics{}
				}
				wsExternal[ws].Reclaimable += match.Representative.Size
				wsExternal[ws].DupCount++
			}
		}
	}

	type WSRow struct {
		MD5         string
		Apparent    int64
		Reclaimable int64
		DupCount    int
	}

	sb.WriteString("\n## Detailed Category Views\n")

	if d.config.ScanBazelOut && len(wsBuildOut) > 0 {
		var buildOutRows []WSRow
		for ws, metrics := range wsBuildOut {
			buildOutRows = append(buildOutRows, WSRow{
				MD5:         ws,
				Apparent:    metrics.Apparent,
				Reclaimable: metrics.Reclaimable,
				DupCount:    metrics.DupCount,
			})
		}
		sort.Slice(buildOutRows, func(i, j int) bool {
			return buildOutRows[i].Reclaimable > buildOutRows[j].Reclaimable
		})

		sb.WriteString("\n### 1. Locally Compiled Build Outputs (`bazel-out/`)\n")
		sb.WriteString("*Local compilation artifacts, binaries, and libraries built inside target configurations.*\n\n")
		sb.WriteString("| Workspace MD5 | Apparent Size | Reclaimable Space | Duplicates | % Reclaimed |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
		for _, row := range buildOutRows {
			pct := 0.0
			if row.Apparent > 0 {
				pct = float64(row.Reclaimable) * 100.0 / float64(row.Apparent)
			}
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s | %.1f%% |\n",
				truncateMD5(row.MD5), formatSize(row.Apparent), formatSize(row.Reclaimable), formatNumber(int64(row.DupCount)), pct)
		}
	}

	if d.config.ScanExternal && len(wsExternal) > 0 {
		var externalRows []WSRow
		for ws, metrics := range wsExternal {
			externalRows = append(externalRows, WSRow{
				MD5:         ws,
				Apparent:    metrics.Apparent,
				Reclaimable: metrics.Reclaimable,
				DupCount:    metrics.DupCount,
			})
		}
		sort.Slice(externalRows, func(i, j int) bool {
			return externalRows[i].Reclaimable > externalRows[j].Reclaimable
		})

		sb.WriteString("\n### 2. Extracted Repository Sources (`external/`)\n")
		sb.WriteString("*Decompressed third-party repository trees, SDKs, and toolchains downloaded from repository rules.*\n\n")
		sb.WriteString("| Workspace MD5 | Apparent Size | Reclaimable Space | Duplicates | % Reclaimed |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
		for _, row := range externalRows {
			pct := 0.0
			if row.Apparent > 0 {
				pct = float64(row.Reclaimable) * 100.0 / float64(row.Apparent)
			}
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s | %.1f%% |\n",
				truncateMD5(row.MD5), formatSize(row.Apparent), formatSize(row.Reclaimable), formatNumber(int64(row.DupCount)), pct)
		}
	}

	return sb.String()
}


func (d *Deduplicator) printLargeDuplicates(matches []EquivalenceClass) {
	fmt.Println("\n=== Large Duplicates ===")
	
	var filtered []EquivalenceClass
	for _, match := range matches {
		totalSize := match.Representative.Size * int64(len(match.Duplicates)+1)
		if totalSize >= d.config.MinReportSize {
			filtered = append(filtered, match)
		}
	}

	if len(filtered) == 0 {
		fmt.Println("No duplicate groups exceed the minimum report size threshold.")
		return
	}

	// Sort by total duplicate reclaimable size descending
	sort.Slice(filtered, func(i, j int) bool {
		sizeI := filtered[i].Representative.Size * int64(len(filtered[i].Duplicates))
		sizeJ := filtered[j].Representative.Size * int64(len(filtered[j].Duplicates))
		return sizeI > sizeJ
	})

	limit := 20
	if len(filtered) < limit {
		limit = len(filtered)
	}

	for i := 0; i < limit; i++ {
		match := filtered[i]
		fmt.Printf("Large Duplicate Group: sha256:%s (Size: %s, %d instances)\n",
			match.Representative.Hash, formatSize(match.Representative.Size), len(match.Duplicates)+1)

		// Print representative
		fmt.Printf("  - [Inode: %d] %s (Representative)\n", match.Representative.Inode, match.Representative.Path)
		// Print duplicates
		for _, dup := range match.Duplicates {
			fmt.Printf("  - [Inode: %d] %s\n", dup.Inode, dup.Path)
		}
	}

	if len(filtered) > limit {
		fmt.Printf("\n... and %d more large duplicate groups truncated.\n", len(filtered)-limit)
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

func formatNumber(n int64) string {
	in := strconv.FormatInt(n, 10)
	var out []rune
	for i, r := range in {
		if i > 0 && (len(in)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, r)
	}
	return string(out)
}

// atomicLink replaces target with a link to source atomically.
func (d *Deduplicator) atomicLink(source, target string) error {
	// Check if target is read-only if we are using hard links and not reflinks
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	tempPath := target + ".tmp-dedup"

	// 1. Temporarily make parent directory writable if it is read-only
	parentDir := filepath.Dir(target)
	pInfo, err := os.Stat(parentDir)
	if err == nil && pInfo.Mode()&0200 == 0 {
		if errChmod := os.Chmod(parentDir, pInfo.Mode()|0200); errChmod == nil {
			defer func() {
				_ = os.Chmod(parentDir, pInfo.Mode())
			}()
		}
	}

	// 2. Clean up any stale temp files from previous interrupted runs (now safe to remove as parent is writable)
	_ = os.Remove(tempPath)

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
		// Hard link safety gate: if the file has write permissions, we make the source
		// read-only first. This makes hard-linking safe by preventing future write propagations.
		if info.Mode()&0222 != 0 {
			err = os.Chmod(source, info.Mode() &^ 0222)
			if err != nil {
				return fmt.Errorf("failed to make source read-only for hardlink safety: %w", err)
			}
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

// Package main provides the Cobra command-line interface and daemon execution
// logic for the bazelmop Cross-Workspace Bazel Cache Deduplicator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
	"github.com/meta-programming/bazelmop/pkg/dedupe"
	"github.com/meta-programming/bazelmop/pkg/web"
)

var (
	rootPath        string
	scanExternal    bool
	scanBazelOut    bool
	minReportSizeMB int64
	preferReflink   bool
	verbose         bool
	dryRun          bool
	daemonInterval  time.Duration

	webEnabled bool
	webHost    string
	webPort    string

	// Subcommand flags
	listFormat     string
	listOrphaned   bool
	gcTTL          string
	gcTargetFree   string
	gcOrphaned     bool
	gcExecute      bool
	gcInteractive  bool
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "bazelmop",
		Short: "bazelmop deduplicates files across Bazel workspaces",
		Long:  `bazelmop is a command-line utility to deduplicate external repository sources and compiled build outputs across multiple Bazel workspaces.`,
	}

	// Persistent flags (available to all subcommands)
	rootCmd.PersistentFlags().StringVar(&rootPath, "root", "", "Bazel cache root directory (default: $HOME/.cache/bazel)")
	rootCmd.PersistentFlags().BoolVar(&scanExternal, "scan-external", true, "Scan and deduplicate external repository dependencies (in external/)")
	rootCmd.PersistentFlags().BoolVar(&scanBazelOut, "scan-bazel-out", true, "Scan and deduplicate locally built targets (in bazel-out/)")
	rootCmd.PersistentFlags().Int64Var(&minReportSizeMB, "min-report-size", 10, "Minimum total size of duplicate group in MB to report hashes (default: 10)")
	rootCmd.PersistentFlags().BoolVar(&preferReflink, "prefer-reflink", true, "Attempt copy-on-write clone (reflink) first, falling back to hard link")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "Print verbose execution details")

	var cleanCmd = &cobra.Command{
		Use:   "clean",
		Short: "Deduplicate files in the Bazel cache and reclaim space",
		Run: func(cmd *cobra.Command, args []string) {
			resolveRootPath()
			runDedupe(dryRun)
		},
	}
	cleanCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate deduplication and report savings without modifying files")

	var reportCmd = &cobra.Command{
		Use:   "report",
		Short: "Report potential space savings without modifying any files (alias for clean --dry-run)",
		Run: func(cmd *cobra.Command, args []string) {
			resolveRootPath()
			runDedupe(true)
		},
	}

	var daemonCmd = &cobra.Command{
		Use:   "daemon",
		Short: "Run bazelmop clean periodically in the background",
		Run: func(cmd *cobra.Command, args []string) {
			resolveRootPath()
			runDaemon()
		},
	}
	daemonCmd.Flags().DurationVar(&daemonInterval, "interval", 24*time.Hour, "Daemon check interval (e.g., 1h, 30m)")
	daemonCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate deduplication and report savings without modifying files")
	daemonCmd.Flags().BoolVar(&webEnabled, "web", false, "Enable the web report dashboard server")
	daemonCmd.Flags().StringVar(&webHost, "web-host", "localhost", "Binding address for the web dashboard")
	daemonCmd.Flags().StringVar(&webPort, "web-port", "8080", "Port for the web dashboard server")

	var outputBasesCmd = &cobra.Command{
		Use:     "output-bases",
		Aliases: []string{"instances", "list"},
		Short:   "List discovered Bazel cache output bases and repository caches",
		Long:    `Discovers and displays all active and orphaned Bazel workspace output bases and content-addressable repository caches.`,
		Run: func(cmd *cobra.Command, args []string) {
			runOutputBasesList(listFormat, listOrphaned)
		},
	}
	outputBasesCmd.Flags().StringVar(&listFormat, "format", "table", "Output format: table or json")
	outputBasesCmd.Flags().BoolVar(&listOrphaned, "orphaned", false, "Filter output bases to show only orphaned instances (missing workspace path)")

	var outputBasesPruneCmd = &cobra.Command{
		Use:     "prune",
		Aliases: []string{"gc"},
		Short:   "Prune and garbage collect inactive or stale Bazel cache output bases",
		Long:    `Analyzes or deletes Bazel output bases and repo caches based on age (TTL), target free space, or orphaned status.`,
		Run: func(cmd *cobra.Command, args []string) {
			runOutputBasesPrune(gcTTL, gcTargetFree, gcOrphaned, gcExecute, gcInteractive)
		},
	}
	outputBasesPruneCmd.Flags().StringVar(&gcTTL, "ttl", "", "Time-to-live threshold for cache instances (e.g. 30d, 14d, 24h)")
	outputBasesPruneCmd.Flags().StringVar(&gcTargetFree, "target-free", "", "Target disk space to reclaim (e.g. 20GB, 500MB)")
	outputBasesPruneCmd.Flags().BoolVar(&gcOrphaned, "orphaned", false, "Filter candidates to only include orphaned output bases")
	outputBasesPruneCmd.Flags().BoolVar(&gcExecute, "execute", false, "Execute filesystem deletion of candidate instances")
	outputBasesPruneCmd.Flags().BoolVar(&gcInteractive, "interactive", false, "Prompt for confirmation before deleting each candidate instance")

	var gcCmd = &cobra.Command{
		Use:    "gc",
		Short:  "Garbage collect output bases (alias for output-bases prune)",
		Hidden: false,
		Run: func(cmd *cobra.Command, args []string) {
			runOutputBasesPrune(gcTTL, gcTargetFree, gcOrphaned, gcExecute, gcInteractive)
		},
	}
	gcCmd.Flags().StringVar(&gcTTL, "ttl", "", "Time-to-live threshold for cache instances (e.g. 30d, 14d, 24h)")
	gcCmd.Flags().StringVar(&gcTargetFree, "target-free", "", "Target disk space to reclaim (e.g. 20GB, 500MB)")
	gcCmd.Flags().BoolVar(&gcOrphaned, "orphaned", false, "Filter candidates to only include orphaned output bases")
	gcCmd.Flags().BoolVar(&gcExecute, "execute", false, "Execute filesystem deletion of candidate instances")
	gcCmd.Flags().BoolVar(&gcInteractive, "interactive", false, "Prompt for confirmation before deleting each candidate instance")

	outputBasesCmd.AddCommand(outputBasesPruneCmd)

	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(reportCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(outputBasesCmd)
	rootCmd.AddCommand(gcCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func resolveRootPath() {
	if rootPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Error: failed to resolve user home directory: %v", err)
		}
		rootPath = filepath.Join(home, ".cache", "bazel")
	} else if strings.HasPrefix(rootPath, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			rootPath = filepath.Join(home, rootPath[1:])
		}
	}
	rootPath = filepath.Clean(rootPath)
}

func runDedupe(isDryRun bool) {
	config := dedupe.Config{
		DryRun:        isDryRun,
		PreferReflink: preferReflink,
		MinReportSize: minReportSizeMB * 1024 * 1024,
		Verbose:       verbose,
		ScanExternal:  scanExternal,
		ScanBazelOut:  scanBazelOut,
	}

	fmt.Println("=========================================================")
	fmt.Println("        Cross-Workspace Bazel Cache Deduplicator        ")
	fmt.Println("=========================================================")
	fmt.Printf("Bazel Root:     %s\n", rootPath)
	fmt.Printf("Dry Run Mode:   %v\n", config.DryRun)
	fmt.Printf("Reflink Pref:   %v\n", config.PreferReflink)
	fmt.Printf("Scan external:  %v\n", config.ScanExternal)
	fmt.Printf("Scan bazel-out: %v\n", config.ScanBazelOut)
	fmt.Printf("Min Report Sz:  %d MB\n", minReportSizeMB)
	fmt.Println("=========================================================")

	// Setup context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT/SIGTERM gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived shutdown signal. Stopping gracefully...")
		cancel()
	}()

	start := time.Now()
	fmt.Printf("\n[%s] Scanning directories...\n", start.Format(time.RFC3339))
	d := dedupe.NewDeduplicator(config)

	entries, err := d.Scan(ctx, rootPath)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Fatalf("Error: Scan failed: %v", err)
	}

	fmt.Printf("Scan completed in %v. Found %d candidate files.\n", time.Since(start), len(entries))
	if len(entries) == 0 {
		fmt.Println("No files found to process.")
		return
	}

	fmt.Println("Matching and calculating savings...")
	matchStart := time.Now()
	_, err = d.Deduplicate(ctx, entries)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Fatalf("Error: Deduplication failed: %v", err)
	}
	fmt.Printf("Deduplication phase took %v.\n", time.Since(matchStart))
}

func runDaemon() {
	var webSrv *web.Server

	config := dedupe.Config{
		DryRun:        dryRun,
		PreferReflink: preferReflink,
		MinReportSize: minReportSizeMB * 1024 * 1024,
		Verbose:       verbose,
		ScanExternal:  scanExternal,
		ScanBazelOut:  scanBazelOut,
		OnProgress: func(status string) {
			if webEnabled && webSrv != nil {
				webSrv.UpdateStatus(status)
			}
		},
	}

	fmt.Println("=========================================================")
	fmt.Println("        Cross-Workspace Bazel Cache Deduplicator (Daemon)")
	fmt.Println("=========================================================")
	fmt.Printf("Bazel Root:     %s\n", rootPath)
	fmt.Printf("Check Interval: %v\n", daemonInterval)
	fmt.Printf("Reflink Pref:   %v\n", config.PreferReflink)
	fmt.Printf("Scan external:  %v\n", config.ScanExternal)
	fmt.Printf("Scan bazel-out: %v\n", config.ScanBazelOut)
	fmt.Printf("Min Report Sz:  %d MB\n", minReportSizeMB)
	if webEnabled {
		fmt.Printf("Web Dashboard:  http://%s:%s\n", webHost, webPort)
	}
	fmt.Println("=========================================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if webEnabled {
		webSrv = web.NewServer(webHost, webPort)
		go func() {
			if err := webSrv.Start(ctx); err != nil {
				log.Printf("Web server error: %v", err)
			}
		}()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived shutdown signal. Stopping daemon gracefully...")
		cancel()
	}()

	runner := func(nextScan time.Time) {
		start := time.Now()
		fmt.Printf("\n[%s] Starting scheduled scan...\n", start.Format(time.RFC3339))
		d := dedupe.NewDeduplicator(config)

		entries, err := d.Scan(ctx, rootPath)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error: Scan failed: %v", err)
			return
		}

		fmt.Printf("Scan completed in %v. Found %d candidate files.\n", time.Since(start), len(entries))
		if len(entries) == 0 {
			// Even if 0 files, update next scan timer on dashboard
			if webEnabled && webSrv != nil {
				webSrv.UpdateNextScan(nextScan)
			}
			return
		}

		report, err := d.Deduplicate(ctx, entries)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error: Deduplication failed: %v", err)
			return
		}
		if webEnabled && webSrv != nil {
			webSrv.UpdateReport(report, nextScan)
			sys := os.DirFS(rootPath)
			if insts, err := bazelcas.DiscoverInstances(sys, rootPath); err == nil {
				webSrv.UpdateInstances(insts)
			}
		}
		fmt.Printf("Completed scheduled deduplication.\n")
	}

	nextScan := time.Now().Add(daemonInterval)
	runner(nextScan) // Run first check immediately

	ticker := time.NewTicker(daemonInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextScan = time.Now().Add(daemonInterval)
			runner(nextScan)
		}
	}
}

func runOutputBasesList(format string, orphanedOnly bool) {
	resolveRootPath()
	sys := os.DirFS(rootPath)

	instances, err := bazelcas.DiscoverInstances(sys, rootPath)
	if err != nil {
		log.Fatalf("Error discovering Bazel cache instances: %v", err)
	}

	var filtered []*bazelcas.Instance
	for _, inst := range instances {
		if orphanedOnly && inst.Status != bazelcas.StatusOrphaned {
			continue
		}
		filtered = append(filtered, inst)
	}

	bazelcas.SortInstances(filtered, "lru")

	now := time.Now()

	if strings.ToLower(format) == "json" {
		type instanceJSON struct {
			*bazelcas.Instance
			LastBuild string `json:"last_build"`
			LastTest  string `json:"last_test"`
		}
		jsonList := make([]instanceJSON, len(filtered))
		for i, inst := range filtered {
			lastBuild := bazelcas.FormatRelativeTime(inst.LastBuildTime, now)
			lastTest := bazelcas.FormatRelativeTime(inst.LastTestTime, now)
			if inst.Type == bazelcas.TypeRepoCache {
				lastBuild = "N/A"
				lastTest = "N/A"
			}
			jsonList[i] = instanceJSON{
				Instance:  inst,
				LastBuild: lastBuild,
				LastTest:  lastTest,
			}
		}
		data, err := json.MarshalIndent(jsonList, "", "  ")
		if err != nil {
			log.Fatalf("Error marshaling instances to JSON: %v", err)
		}
		fmt.Println(string(data))
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tSIZE\tLAST MODIFIED\tLAST BUILD\tLAST TEST\tSTATUS\tWORKSPACE PATH")
	for _, inst := range filtered {
		lastMod := inst.LastModified.Format("2006-01-02 15:04:05")
		if inst.LastModified.IsZero() {
			lastMod = "Unknown"
		}
		lastBuild := bazelcas.FormatRelativeTime(inst.LastBuildTime, now)
		lastTest := bazelcas.FormatRelativeTime(inst.LastTestTime, now)
		if inst.Type == bazelcas.TypeRepoCache {
			lastBuild = "N/A"
			lastTest = "N/A"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			inst.ID,
			inst.Type,
			bazelcas.FormatSize(inst.SizeBytes),
			lastMod,
			lastBuild,
			lastTest,
			inst.Status,
			inst.WorkspacePath,
		)
	}
	w.Flush()
}

func runOutputBasesPrune(ttlStr, targetFreeStr string, orphanedOnly, execute, interactive bool) {
	resolveRootPath()
	sys := os.DirFS(rootPath)

	ttlDuration, err := bazelcas.ParseTTL(ttlStr)
	if err != nil {
		log.Fatalf("Invalid TTL specification: %v", err)
	}

	targetFreeBytes, err := bazelcas.ParseSize(targetFreeStr)
	if err != nil {
		log.Fatalf("Invalid target free size specification: %v", err)
	}

	instances, err := bazelcas.DiscoverInstances(sys, rootPath)
	if err != nil {
		log.Fatalf("Error discovering Bazel cache instances: %v", err)
	}

	opts := bazelcas.GCOptions{
		TTL:             ttlDuration,
		TargetFreeBytes: targetFreeBytes,
		OrphanedOnly:    orphanedOnly,
	}

	candidates := bazelcas.FilterInstances(instances, opts, time.Now())
	if len(candidates) == 0 {
		fmt.Println("No Bazel cache instances match the garbage collection criteria.")
		return
	}

	var totalReclaimable int64
	for _, cand := range candidates {
		totalReclaimable += cand.SizeBytes
	}

	if !execute && !interactive {
		fmt.Println("================================================================================")
		fmt.Println("            Bazel Cache Instance Garbage Collection Analysis Report             ")
		fmt.Println("================================================================================")
		fmt.Printf("Cache Root:        %s\n", rootPath)
		if ttlStr != "" {
			fmt.Printf("TTL Threshold:     %s (%v)\n", ttlStr, ttlDuration)
		}
		if targetFreeStr != "" {
			fmt.Printf("Target Free Size:  %s (%s)\n", targetFreeStr, bazelcas.FormatSize(targetFreeBytes))
		}
		fmt.Printf("Orphaned Filter:   %v\n", orphanedOnly)
		fmt.Printf("Candidate Count:   %d instances\n", len(candidates))
		fmt.Printf("Reclaimable Space: %s\n", bazelcas.FormatSize(totalReclaimable))
		fmt.Println("--------------------------------------------------------------------------------")

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTYPE\tSIZE\tLAST MODIFIED\tLAST BUILD\tLAST TEST\tSTATUS\tWORKSPACE PATH")
		now := time.Now()
		for _, cand := range candidates {
			lastMod := cand.LastModified.Format("2006-01-02 15:04:05")
			if cand.LastModified.IsZero() {
				lastMod = "Unknown"
			}
			lastBuild := bazelcas.FormatRelativeTime(cand.LastBuildTime, now)
			lastTest := bazelcas.FormatRelativeTime(cand.LastTestTime, now)
			if cand.Type == bazelcas.TypeRepoCache {
				lastBuild = "N/A"
				lastTest = "N/A"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				cand.ID,
				cand.Type,
				bazelcas.FormatSize(cand.SizeBytes),
				lastMod,
				lastBuild,
				lastTest,
				cand.Status,
				cand.WorkspacePath,
			)
		}
		w.Flush()

		fmt.Println("\nImpact & Consequences of Deletion:")
		for _, cand := range candidates {
			if cand.Type == bazelcas.TypeOutputBase {
				fmt.Printf(" - [Output Base %s]: Cold rebuild required on next build in workspace %s (output base will be recreated)\n", cand.ID, cand.WorkspacePath)
			} else {
				fmt.Printf(" - [Repo Cache %s]: External repository dependencies will be re-downloaded on next build\n", cand.ID)
			}
		}

		fmt.Println("\nTo perform garbage collection automatically, run:")
		cmdSuggest := "  bazelmop output-bases prune"
		if ttlStr != "" {
			cmdSuggest += " --ttl " + ttlStr
		}
		if targetFreeStr != "" {
			cmdSuggest += " --target-free " + targetFreeStr
		}
		if orphanedOnly {
			cmdSuggest += " --orphaned"
		}
		cmdSuggest += " --execute"
		fmt.Println(cmdSuggest)

		fmt.Println("\nOr remove candidate directories manually:")
		for _, cand := range candidates {
			fmt.Printf("  rm -rf %s\n", cand.Path)
		}
		fmt.Println("================================================================================")
		return
	}

	fmt.Println("================================================================================")
	fmt.Println("            Executing Bazel Cache Instance Garbage Collection                   ")
	fmt.Println("================================================================================")

	var deletedCount int
	var deletedBytes int64

	for _, cand := range candidates {
		shouldDelete := execute
		if interactive {
			prompt := fmt.Sprintf("Delete %s instance %s (%s, %s)?", cand.Type, cand.ID, bazelcas.FormatSize(cand.SizeBytes), cand.Path)
			shouldDelete = bazelcas.ReadPrompt(os.Stdin, prompt)
		}

		if shouldDelete {
			fmt.Printf("Deleting %s (%s)... ", cand.Path, bazelcas.FormatSize(cand.SizeBytes))
			err := os.RemoveAll(cand.Path)
			if err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("SUCCESS")
				deletedCount++
				deletedBytes += cand.SizeBytes
			}
		} else {
			fmt.Printf("Skipped %s\n", cand.ID)
		}
	}

	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("Garbage Collection Complete: Removed %d instances, reclaimed %s of disk space.\n", deletedCount, bazelcas.FormatSize(deletedBytes))
	fmt.Println("================================================================================")
}

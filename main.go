// Package main provides the Cobra command-line interface and daemon execution
// logic for the bazelmop Cross-Workspace Bazel Cache Deduplicator.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	daemonCmd.Flags().DurationVar(&daemonInterval, "interval", 1*time.Hour, "Daemon check interval (e.g., 1h, 30m)")
	daemonCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate deduplication and report savings without modifying files")
	daemonCmd.Flags().BoolVar(&webEnabled, "web", false, "Enable the web report dashboard server")
	daemonCmd.Flags().StringVar(&webHost, "web-host", "localhost", "Binding address for the web dashboard")
	daemonCmd.Flags().StringVar(&webPort, "web-port", "8080", "Port for the web dashboard server")

	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(reportCmd)
	rootCmd.AddCommand(daemonCmd)

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
	config := dedupe.Config{
		DryRun:        dryRun,
		PreferReflink: preferReflink,
		MinReportSize: minReportSizeMB * 1024 * 1024,
		Verbose:       verbose,
		ScanExternal:  scanExternal,
		ScanBazelOut:  scanBazelOut,
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

	var webSrv *web.Server
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

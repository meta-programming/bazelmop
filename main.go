// Package main provides the command-line interface and daemon execution
// logic for the Cross-Workspace Bazel Cache Deduplicator.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"bazel-cache-share/pkg/dedupe"
)

func main() {
	// Parse CLI flags
	rootPath := flag.String("root", "", "Bazel cache root directory (default: $HOME/.cache/bazel)")
	dryRun := flag.Bool("dry-run", true, "Simulate deduplication and report savings without modifying files")
	daemon := flag.Bool("daemon", false, "Run periodically as a background daemon")
	interval := flag.Duration("interval", 1*time.Hour, "Daemon check interval (e.g., 1h, 30m)")
	minReportSizeMB := flag.Int64("min-report-size", 10, "Minimum total size of duplicate group in MB to report hashes (default: 10)")
	preferReflink := flag.Bool("prefer-reflink", true, "Attempt copy-on-write clone (reflink) first, falling back to hard link")
	verbose := flag.Bool("verbose", false, "Print verbose execution details")
	scanExternal := flag.Bool("external", true, "Scan and deduplicate external repository dependencies (in external/)")
	scanBazelOut := flag.Bool("bazel-out", true, "Scan and deduplicate locally built targets (in bazel-out/)")

	flag.Parse()

	// Resolve the Bazel cache root
	if *rootPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Error: failed to resolve user home directory: %v", err)
		}
		*rootPath = filepath.Join(home, ".cache", "bazel")
	} else if strings.HasPrefix(*rootPath, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			*rootPath = filepath.Join(home, (*rootPath)[1:])
		}
	}

	// Clean the resolved path
	*rootPath = filepath.Clean(*rootPath)

	// Verify the root directory exists
	info, err := os.Stat(*rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("Error: Bazel root directory %q does not exist. Ensure Bazel has run at least once.", *rootPath)
		}
		log.Fatalf("Error: failed to inspect Bazel root directory: %v", err)
	}
	if !info.IsDir() {
		log.Fatalf("Error: Bazel root path %q is not a directory.", *rootPath)
	}

	config := dedupe.Config{
		DryRun:        *dryRun,
		PreferReflink: *preferReflink,
		MinReportSize: *minReportSizeMB * 1024 * 1024,
		Verbose:       *verbose,
		ScanExternal:  *scanExternal,
		ScanBazelOut:  *scanBazelOut,
	}

	fmt.Println("=========================================================")
	fmt.Println("        Cross-Workspace Bazel Cache Deduplicator        ")
	fmt.Println("=========================================================")
	fmt.Printf("Bazel Root:     %s\n", *rootPath)
	fmt.Printf("Dry Run Mode:   %v\n", config.DryRun)
	fmt.Printf("Reflink Pref:   %v\n", config.PreferReflink)
	fmt.Printf("Scan external:  %v\n", config.ScanExternal)
	fmt.Printf("Scan bazel-out: %v\n", config.ScanBazelOut)
	fmt.Printf("Min Report Sz:  %d MB\n", *minReportSizeMB)
	fmt.Println("=========================================================")

	// Context for cancellation
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

	runner := func() {
		start := time.Now()
		fmt.Printf("\n[%s] Scanning directories...\n", start.Format(time.RFC3339))
		d := dedupe.NewDeduplicator(config)

		entries, err := d.Scan(ctx, *rootPath)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error: Scan failed: %v", err)
			return
		}

		fmt.Printf("Scan completed in %v. Found %d candidate files.\n", time.Since(start), len(entries))
		if len(entries) == 0 {
			fmt.Println("No files found to process.")
			return
		}

		fmt.Println("Matching and calculating savings...")
		matchStart := time.Now()
		err = d.Deduplicate(ctx, entries)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error: Deduplication failed: %v", err)
			return
		}
		fmt.Printf("Deduplication phase took %v.\n", time.Since(matchStart))
	}

	if !*daemon {
		runner()
		return
	}

	// Daemon execution loop
	fmt.Printf("Starting in Daemon Mode. Check interval: %v\n", *interval)
	runner() // Run first check immediately

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runner()
		}
	}
}

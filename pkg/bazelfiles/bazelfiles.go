package bazelfiles

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

// BuildOutputPath represents a strongly-typed path to a compiled build output
// located inside `bazel-out/` (e.g., .../bazel-out/k8-fastbuild/bin/main.a).
type BuildOutputPath string

// String returns the raw string representation.
func (p BuildOutputPath) String() string {
	return string(p)
}

// Workspace resolves the owning workspace output base path.
func (p BuildOutputPath) Workspace() bazelcas.WorkspaceCASPath {
	user, ws := parseWorkspaceContext(string(p))
	if user == "" || ws == "" {
		return ""
	}
	// Reconstruct the workspace path dynamically
	idx := strings.Index(string(p), "/_bazel_"+user+"/"+ws)
	if idx == -1 {
		return ""
	}
	return bazelcas.WorkspaceCASPath(string(p)[:idx] + "/_bazel_" + user + "/" + ws)
}

// Config resolves the compilation configuration directory (e.g., "k8-fastbuild").
func (p BuildOutputPath) Config() string {
	parts := strings.Split(string(p), string(os.PathSeparator))
	for i, part := range parts {
		if part == "bazel-out" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// ExternalDepPath represents a strongly-typed path to an extracted external
// dependency located inside `external/` (e.g., .../external/rules_go/README.md).
type ExternalDepPath string

// String returns the raw string representation.
func (p ExternalDepPath) String() string {
	return string(p)
}

// Workspace resolves the owning workspace output base path.
func (p ExternalDepPath) Workspace() bazelcas.WorkspaceCASPath {
	user, ws := parseWorkspaceContext(string(p))
	if user == "" || ws == "" {
		return ""
	}
	idx := strings.Index(string(p), "/_bazel_"+user+"/"+ws)
	if idx == -1 {
		return ""
	}
	return bazelcas.WorkspaceCASPath(string(p)[:idx] + "/_bazel_" + user + "/" + ws)
}

// Repository resolves the external repository name (e.g., "rules_go").
func (p ExternalDepPath) Repository() string {
	parts := strings.Split(string(p), string(os.PathSeparator))
	for i, part := range parts {
		if part == "external" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// RepoCacheCASPath represents a strongly-typed path to a downloaded central
// cache archive located in the repository CAS (e.g., .../sha256/[hash]/file).
type RepoCacheCASPath string

// String returns the raw string representation.
func (p RepoCacheCASPath) String() string {
	return string(p)
}

// User resolves the system user directory.
func (p RepoCacheCASPath) User() bazelcas.UserCASPath {
	parts := strings.Split(string(p), string(os.PathSeparator))
	for _, part := range parts {
		if strings.HasPrefix(part, "_bazel_") {
			idx := strings.Index(string(p), "/"+part)
			if idx != -1 {
				return bazelcas.UserCASPath(string(p)[:idx] + "/" + part)
			}
		}
	}
	return ""
}

// Hash resolves the SHA-256 content address hash of the archive.
func (p RepoCacheCASPath) Hash() string {
	parts := strings.Split(string(p), string(os.PathSeparator))
	for i, part := range parts {
		if part == "sha256" && i+1 < len(parts) {
			if len(parts[i+1]) == 64 {
				return parts[i+1]
			}
		}
	}
	return ""
}

// parseWorkspaceContext extracts the system username and workspace MD5 hash from a path.
func parseWorkspaceContext(path string) (string, string) {
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
	return "", ""
}

// DiscoveredFiles groups the strongly-typed file paths discovered during a walk.
type DiscoveredFiles struct {
	BuildOutputs []BuildOutputPath
	ExternalDeps []ExternalDepPath
	RepoCacheCAS []RepoCacheCASPath
}

// WalkOption configures the behavior of the Walk operation.
type WalkOption func(*walkOptions)

// Private configuration structure holding the evaluated options.
type walkOptions struct {
	scanExternal bool
	scanBazelOut bool
}

// WithScanExternal configures whether the walk traverses external dependencies.
func WithScanExternal(enable bool) WalkOption {
	return func(o *walkOptions) {
		o.scanExternal = enable
	}
}

// WithScanBazelOut configures whether the walk traverses locally built targets.
func WithScanBazelOut(enable bool) WalkOption {
	return func(o *walkOptions) {
		o.scanBazelOut = enable
	}
}

// Walk recursively walks the Bazel root cache directory and maps discovered
// files into a strongly-typed DiscoveredFiles container, applying any WalkOption modifiers.
func Walk(ctx context.Context, root bazelcas.RootCASPath, options ...WalkOption) (*DiscoveredFiles, error) {
	opts := &walkOptions{
		scanExternal: true,
		scanBazelOut: true,
	}
	for _, opt := range options {
		opt(opts)
	}

	discovered := &DiscoveredFiles{}
	rootStr := root.String()

	userEntries, err := os.ReadDir(rootStr)
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

		userPath := filepath.Join(rootStr, userEntry.Name())

		// Read repository cache CAS files if configured
		if opts.scanExternal {
			repoCASPath := filepath.Join(userPath, "cache", "repos", "v1", "content_addressable", "sha256")
			shaEntries, err := os.ReadDir(repoCASPath)
			if err == nil {
				for _, shaEntry := range shaEntries {
					if shaEntry.IsDir() && len(shaEntry.Name()) == 64 {
						filePath := filepath.Join(repoCASPath, shaEntry.Name(), "file")
						info, err := os.Lstat(filePath)
						if err == nil && info.Mode().IsRegular() {
							discovered.RepoCacheCAS = append(discovered.RepoCacheCAS, RepoCacheCASPath(filePath))
						}
					}
				}
			}
		}

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

			// 1. Walk external/ dependencies
			if opts.scanExternal {
				targetDir := filepath.Join(wsPath, "external")
				_ = filepath.WalkDir(targetDir, func(path string, de fs.DirEntry, err error) error {
					if err != nil {
						return nil
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
					if err == nil && info.Mode().IsRegular() {
						discovered.ExternalDeps = append(discovered.ExternalDeps, ExternalDepPath(path))
					}
					return nil
				})
			}

			// 2. Walk bazel-out/ compiled outputs
			if opts.scanBazelOut {
				execrootPath := filepath.Join(wsPath, "execroot")
				execrootEntries, err := os.ReadDir(execrootPath)
				if err == nil {
					for _, execEntry := range execrootEntries {
						fullPath := filepath.Join(execrootPath, execEntry.Name())
						stat, err := os.Stat(fullPath)
						if err == nil && stat.IsDir() {
							targetDir := filepath.Join(fullPath, "bazel-out")
							_ = filepath.WalkDir(targetDir, func(path string, de fs.DirEntry, err error) error {
								if err != nil {
									return nil
								}
								select {
								case <-ctx.Done():
									return ctx.Err()
								default:
								}
								if de.IsDir() {
									if (de.Name() == "external" || de.Name() == "node_modules") && !opts.scanExternal {
										return filepath.SkipDir
									}
									return nil
								}
								info, err := de.Info()
								if err == nil && info.Mode().IsRegular() {
									discovered.BuildOutputs = append(discovered.BuildOutputs, BuildOutputPath(path))
								}
								return nil
							})
						}
					}
				}
			}
		}
	}

	return discovered, nil
}

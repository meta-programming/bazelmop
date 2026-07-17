# Design Document: Cross-Workspace Bazel Cache Deduplicator

This document outlines the detailed architecture, requirements, and design for a Go-based Bazel cache deduplication tool.

## 1. Introduction

Bazel isolates build outputs per workspace by naming the output directories based on the MD5 hash of the workspace path. This provides hermeticity but can lead to gigabytes of duplicate files (especially downloaded external repositories and common compiled targets) on the same machine.

The deduplicator scans a Bazel root directory, identifies identical files across different workspaces, and replaces them with links (reflinks or hard links) to recover disk space.

---

## 2. Requirements

### Requirements Table

Below is the mapping of all requirements to their priority, design implementation, and verification methods.

| Req ID | Priority | Requirement Description | Implementation Detail | Verification Method |
| :--- | :--- | :--- | :--- | :--- |
| **req-dedup** | P0 | **Deduplication**: Scan Bazel output directories, identify identical files, and replace them with links. | Core scanner and matcher finds identical files and replaces duplicates using links. | Unit test & manual check with `df -h` / `du`. |
| **req-safety** | P0 | **Safety Gates**: Skip non-read-only files (if using hard links) and prevent crossing filesystem boundaries. | Checks file mode permissions (`mode & 0222 == 0`) for hard links, and verifies device ID matching (`sys.Dev`) to avoid cross-filesystem link errors. | Unit tests checking permission filters and boundary checks. |
| **req-atomic** | P0 | **Atomic Operations**: Perform link replacements atomically to prevent concurrent build failures. | Creates a temporary link to the source file (e.g. `path + ".tmp-dedup"`) in the target directory, then calls `rename(2)` to overwrite target atomically. | Unit tests simulating concurrent file access during deduplication. |
| **req-dryrun** | P0 | **Dry Run**: Simulate deduplication and output savings report without modifying any files. | Implements a `--dry-run` flag that runs the scanner, groups duplicates, and generates the report without calling link/rename. | CLI execution with `--dry-run` and verification that no file inodes changed. |
| **req-report** | P0 | **Detailed Reporting**: Output structured global space metrics and a per-workspace breakdown. | Computes `Total Scanned Files`, `Apparent Size`, `Disk Footprint`, `Reclaimable Space`, and breaks down space savings per workspace directory. | Unit tests verifying metric arithmetic; CLI output inspection. |
| **req-perf-hash** | P0 | **Fast Matching (Low I/O)**: Group files by size/inodes first, comparing byte-by-byte rather than hashing. | Sizes are grouped. Files with identical sizes are matched byte-by-byte (exiting early on first mismatch). No SHA-256 is computed except for large reporting. | Benchmarks and profiling under large workspaces. |
| **req-daemon** | P1 | **Daemon Mode**: Run periodically in the background using a timer. | CLI implements background loop via a time ticker. | Manual validation using background runtime logs. |
| **req-perf-mem** | P1 | **Low Memory Footprint**: Avoid holding open descriptors and large in-memory maps. | Files are walked sequentially, and descriptors are closed immediately after byte-by-byte comparison blocks. | Go memory profiling (`pprof`). |
| **req-alerts** | P2 | **Large Duplicate Alerts**: List duplicate groups exceeding a size threshold (e.g. 10 MB) with hashes. | Groups exceeding `--min-report-size` are compiled, their representative file hashed, and detailed in the report. | CLI execution and checking the output report matches expected groups. |

---

## 3. Background

### 3.1. Bazel Output Directory Layout and Caching
To maintain hermeticity and support fast incremental builds, Bazel isolates build outputs inside a dedicated user root[^1]. Bazel organizes output bases under a hashed directory structure representing the MD5 hash of the workspace path[^1]:
`$HOME/.cache/bazel/_bazel_[username]/[workspace_md5]/`

Within each workspace directory, Bazel maintains:
- `execroot/`: The working directory for build actions, containing source symlinks and build inputs[^1].
- `bazel-out/`: The directory containing compiled libraries, binaries, and intermediate artifacts[^1].
- `external/`: Cloned repository rule dependencies.

> [!NOTE]
> The architectural rationale behind isolation using MD5 hashed workspace paths is detailed in **Appendix A.3**.

#### Repository Cache (CAS) Location & Multi-CAS Scenarios
By default, Bazel maintains a content-addressable repository cache under the user's output root directory[^2]:
- **Linux**: `~/.cache/bazel/_bazel_$USER/cache/repos/v1/content_addressable/`
- **macOS**: `~/Library/Caches/Bazel/_bazel_$USER/cache/repos/v1/content_addressable/`
- **Windows**: `%USERPROFILE%\_bazel_$USER\cache\repos\v1\content_addressable\`

While this cache is shared across workspaces by default, it is **not globally unified** across all contexts. A single machine can have multiple independent CAS directories due to:
1. **User Isolation**: The default output user root is scoped to `$USER`. Different system users have completely isolated CAS directories.
2. **Flag Overrides**: Users and build scripts can override the repository cache location per build or per project using the `--repository_cache` command-line flag or `.bazelrc` configurations (e.g., `--repository_cache=/path/to/custom/cache`)[^2].
3. **Output User Root Overrides**: Specifying `--output_user_root` changes the parent directory of the default repository cache, creating an independent cache tree.

#### Inode Sharing Behavior Across Workspaces
- **If `--experimental_repository_cache_hardlinks` is enabled**: Files in the `external/` directories of different workspaces that refer to the same dependency version will be hard-linked to the **same** shared repository cache CAS file. Thus, they **will** share the same inode across workspaces (and are already deduplicated). However, if workspaces specify custom repository caches or use different dependency versions, they will not share inodes.
- **If `--experimental_repository_cache_hardlinks` is disabled (Default)**: Bazel copies files from the repository cache to `external/`. In this case, files **do not** share inodes across workspaces, leading to duplicate disk footprint.
- **Compiled Build Outputs (`bazel-out/`)**: Bazel never hardlinks compiled target outputs across workspaces (even if retrieved from a shared local `--disk_cache`, they are copied). Therefore, duplicate build outputs **never** share inodes across workspaces by default.

#### CAS Directory Layout Example
Inside the `cache/repos/v1/content_addressable/` directory, files are indexed cryptographically under the hash algorithm directory:

```
[output_user_root]/cache/repos/v1/content_addressable/
└── sha256/
    ├── 1b4f4ef8bc3a4d8c.../
    │   └── file         <-- The actual cached dependency file content
    ├── a29c8e10df32ef74.../
    │   └── file
    └── f8309aef80bc41df.../
        └── file
```

By default, Bazel sets file permissions on all output artifacts to read-only (`0555` or `0444`)[^3] to enforce build hermeticity. When rebuilding a target, Bazel does not write to existing files in-place; instead, it deletes (unlinks) the existing file and creates a new one in its place[^3]. This behavior makes Bazel output files safe for link-based deduplication, as updates unlink the old link, preserving other workspaces.

### 3.2. Hard Links and Reference Counting in POSIX Filesystems
In POSIX-compliant filesystems (such as ext4[^4]), a file is divided into two concepts: directory entries (filenames mapped to inode numbers) and inodes (which store metadata and pointers to the physical data blocks on disk)[^4].

When a hard link is created via the `link(2)` system call[^5], a new directory entry is established pointing to an existing inode, and the inode's link count (`nlink`) is incremented.
- **Safety and Deletion**: When a program calls `unlink(2)` (or `rm`)[^6], the OS removes the specific directory entry and decrements the inode's link count. The actual data blocks are only marked as free when the link count reaches `0` and there are no active file descriptors pointing to the inode. Therefore, deleting a hard link in one workspace has no effect on other workspaces sharing that inode.
- **In-Place Modification Risks**: If a process opens a hard-linked file in write-only (`O_WRONLY`) or read-write (`O_RDWR`) mode and modifies its content, the modifications are written to the shared data blocks, corrupting all other hard links pointing to the same inode. To prevent this, files must be marked read-only to prevent accidental writes, and build tools must unlink existing files before writing new ones.

### 3.3. Copy-on-Write (CoW) Reflinks
Reflinks (available on Copy-on-Write filesystems such as XFS[^7] and Btrfs[^8]) allow multiple files to share the same physical storage blocks on disk while maintaining independent inodes.
- Reflinks are created on Linux using the `ioctl(2)` system call with the `FICLONE` or `FICLONERANGE` command[^9].
- Unlike hard links, since reflinks have distinct inodes, modifying one file triggers an automatic Copy-on-Write operation by the operating system kernel. The kernel allocates new disk blocks for the modified data, leaving the unmodified blocks of the clone intact. This completely eliminates any risk of in-place cache corruption.

---

## 4. High-Level Design and Justification

### 4.1. Core Workflow
The tool will execute in four sequential phases:
```
+--------------+     +-----------------+     +-----------------------+     +--------------------+
|  1. Scanner  | --> |  2. CAS Mapper  | --> |  3. Size/Byte Matcher | --> |  4. Atomic Linker  |
+--------------+     +-----------------+     +-----------------------+     +--------------------+
```
1. **Scanner**: Recursively crawls all files under the specified Bazel root. It filters out directories, symlinks, sockets, and non-regular files to guarantee walk safety.
2. **CAS Mapper**: Reads the repository cache files. It maps each file's inode to its content hash (resolved from the path name).
3. **Matcher**: Filters out files that are already linked. It groups remaining files by size and performs fast byte-by-byte comparisons to group identical files without hashing.
4. **Linker**: Replaces duplicate files with copy-on-write reflinks (or hard links on unsupported systems) using atomic POSIX actions.

### 4.2. Architecture Justifications
- **Programming Language (Go)**: Go provides cross-platform compilation with zero runtime dependencies. It compiles to a single, static binary, which is ideal for a system developer utility. It also exposes standard library bindings (`syscall` / `golang.org/x/sys`) to directly invoke filesystem-level ioctls (`FICLONE`) without Cgo.
- **Deduplication Strategy (Reflinks Over Hard Links)**: Reflinking is our preferred approach. By using copy-on-write clones, we achieve the exact same disk space savings as hard links while keeping file inodes isolated. If a user modifies a file in a workspace, the filesystem automatically copies the block, preventing other workspaces or the central CAS from being corrupted.
- **Zero-Hashing Optimization**: Computing SHA-256 hashes on thousands of files is highly CPU and disk I/O intensive. By comparing files byte-by-byte only when they have identical sizes, we exit early on the first differing byte (frequently the first block). We only perform SHA-256 hashing to identify groups in the report, saving substantial CPU cycles.
- **Atomic Renaming**: Unlinking and linking directly causes file-missing race windows. The atomic `link + rename` sequence ensures that files are always available to active Bazel builds.
- **Strongly-Typed Path Handling**: Navigation of Bazel output and CAS structures will be encapsulated in a reusable package `bazelcas` using strong types (like `RootCASPath`, `UserCASPath`, `WorkspaceCASPath`) with safe traversal helper methods to make path resolution bulletproof.

---

## 5. Detailed Design

### 5.1. Directory Scanning & CAS Mapping
The walking loop crawls the output bases `_bazel_[user]/[workspace_md5]`.
During scanning, for each file encountered, we retrieve its directory path and metadata via `stat(2)`.

#### CAS Inode Indexing
On startup, the tool walks `$HOME/.cache/bazel/_bazel_[user]/cache/repos/v1/content_addressable/sha256/` and builds an in-memory map:
```go
type CASMap map[uint64]string // maps Inode -> SHA-256 Hex String
```
When walking workspace directories, if `stat.Inode` exists in `CASMap`, we associate its hash and skip content comparison entirely.

### 5.2. Equivalence Class Partitioning (Fast Matching)
Workspace files that are not already linked are processed as follows:
1. Files are grouped in a map: `map[int64][]FileEntry` where the key is the file size.
2. Groups with only one file are discarded.
3. For groups with multiple files, we partition them into equivalence classes. An equivalence class represents a set of identical files.
4. To match a file `F` against an class represented by `R`:
   - We open both files and read them in chunks (e.g., 4096 bytes).
   - If any chunk differs, we immediately close the files and test `F` against the next class.
   - If all bytes match, we group `F` with `R` and schedule `F` to be replaced with a link to `R`.
   - If `F` matches no existing class, a new class is created with `F` as the representative.

### 5.3. File Replacement Mechanics
To replace `target` with a link to `source`:
1. Resolve target file permissions and filesystem device ID (`sys.Dev`). Verify `sys.Dev` matches `source` to prevent cross-device linking errors (`EXDEV`).
2. Generate a temporary path: `tempPath := target + ".tmp-dedup"`.
3. Perform the link operation:
   - **Reflink (Linux/macOS)**: Open `source` for reading, open `tempPath` for writing, and call `unix.IoctlFileClone(dstFd, srcFd)`.
   - **Hard Link Fallback**: If reflinking fails or is unsupported on the host filesystem, call `os.Link(source, tempPath)`. This fallback is restricted to read-only files (`mode & 0222 == 0`).
4. Atomically swap: `os.Rename(tempPath, target)`.

---

## 6. Testing Plan

### 6.1. Unit Testing
Unit tests in `pkg/dedupe/dedupe_test.go` and `pkg/bazelcas/bazelcas_test.go` will use standard Go test tables to verify package behaviors on sandboxed test directories:
- **bazelcas Utility Validation**:
  - Test `RootCASPath`, `UserCASPath`, and `WorkspaceCASPath` conversions and child/parent traversals.
  - Test resolving default user roots on Linux and macOS.
  - Test detection of custom repository caches.
- **Fast Matcher Validation**:
  - Verify that files of different sizes are never compared.
  - Verify that files of identical sizes but different contents are distinguished on the first mismatched byte.
  - Verify that identical files are correctly grouped.
- **Safeties and Permission Checks**:
  - Test that writeable files are ignored during hard-linking fallback checks.
  - Test that files on different device partitions are skipped to prevent cross-device link errors (`EXDEV`).
- **Atomic Swap Mechanics**:
  - Simulate interruption between link creation and renaming to verify that the target directory remains consistent and the target file is never left missing.
  - Verify that target replacement works cleanly when target files are read-only.
- **Reflink/Hardlink Logic**:
  - Verify that the system attempts an `ioctl` clone first, falling back to a hard link when clone fails.

### 6.2. Integration Testing
A series of end-to-end integration tests will run locally:
- **Sandbox Creation**: Programmatic generation of a mock Bazel root containing:
  - A mock repository cache with files inside `content_addressable/sha256/`.
  - Multiple mock workspace hash directories with identical and unique files under `external/` and `bazel-out/`.
- **Deduplication Verification**:
  - Run the deduplicator CLI.
  - Verify that identical files now share the same inode (or are reflinks) and the physical disk footprint (`du -sh`) has decreased.
  - Check that unique files remain untouched.
- **Hermeticity Simulation**:
  - Simulate Bazel's action execution by deleting (unlinking) a deduplicated target and creating a new one in its place.
  - Verify that updating the file in one workspace does not affect the linked files in other workspaces (proving correct write safety).

---

## Appendix: Design Safeties and User Review Items

### A.1. Copy-on-Write (Reflinks) vs. Hard Links
Hard links share the same inode. If a user or tool writes to a hard-linked file, the changes modify the underlying data blocks, corrupting all other linked references (including the CAS or other workspaces).
- **Reflinks**: On filesystems that support copy-on-write cloning (Btrfs, XFS, APFS), we will use `unix.IoctlFileClone` (or `clonefile(2)` on macOS). This creates a copy that shares disk space but has a separate inode. Writing to one copy automatically triggers OS-level copy-on-write, preventing any cross-workspace corruption.
- **Hard Link Safety**: On filesystems without reflink support (such as ext4), we will fall back to hard links, but **only** on files that are read-only (`mode & 0222 == 0`). Since Bazel output files are read-only by default and unlinked before rebuilding, this is safe for Bazel.

### A.2. Filesystem Boundaries
Hard links cannot cross filesystem boundaries. The deduplicator will check the device ID (`sys.Dev`) of the source and target files and skip linking if they reside on different devices.

### A.3. Rationale for Hashed Workspace Directories
Bazel constructs output bases using the MD5 hash of the workspace directory's absolute path to enforce correct build state and isolation:
1. **Collision Avoidance**: Hashing ensures that multiple checkout directories of the same codebase, or different projects with identical names (e.g. `frontend`), do not overwrite or corrupt each other's output structures.
2. **Symlink and Absolute Path Safety**: Bazel constructs local build sandboxes and action execution roots (`execroot/`) using absolute symlinks pointing back to source files. If a workspace is renamed or moved, reusing the old output base would cause broken symlinks and cache invalidation failures. Scopes are safely separated by forcing a clean output base on workspace moves.
3. **Build Concurrency**: Fully isolated output paths allow developers to run concurrent builds in different workspaces without locking or cache corruption.
4. **Dependency Version Isolation**: Extracted external dependency repository code resides in `external/` within the workspace-specific hashed base. This isolates dependency configurations (e.g., distinct version checkouts) across workspaces, while the unextracted downloaded packages remain safely deduplicated in the common CAS repository cache.

### A.4. Draft Go Implementation Structure
Below is the draft structure of the core packages and entry points:

```
bazel-cache-share/
├── go.mod
├── main.go               # Command-line interface and daemon loops
├── style.md              # Project coding standard references
├── design.md             # This design document
└── pkg/
    ├── bazelcas/
    │   ├── doc.go        # Package documentation for bazelcas
    │   ├── bazelcas.go   # Strongly-typed path structure definitions and helpers
    │   └── bazelcas_test.go# Unit tests for bazelcas paths
    └── dedupe/
        ├── doc.go        # Package documentation for dedupe
        ├── dedupe.go     # Core scanner, matcher, and link wrapper logic
        └── dedupe_test.go# Unit test suite for deduplication
```

#### pkg/bazelcas/doc.go
```go
// Package bazelcas provides strongly-typed abstractions and directory traversals
// for navigating Bazel's local output base and repository cache structures.
//
// Bazel organizes its build metadata, output artifacts, and external downloaded
// files under a hashed structure representing system user isolation and workspace
// directory absolute paths. This package models these concepts as immutable
// string-based types to ensure compile-time safety when performing path operations.
//
// # Path Hierarchy
//
// The path types form a three-tier hierarchy corresponding to Bazel's filesystem layout:
//   - [RootCASPath]: Represents the base cache directory (typically $HOME/.cache/bazel).
//   - [UserCASPath]: Represents the system user's root base (typically .../_bazel_[username]).
//   - [WorkspaceCASPath]: Represents a project workspace output base (typically .../[workspace_md5]).
//
// Navigating between these paths is done using helper methods that encapsulate
// parent-child relationships (e.g., resolving a [WorkspaceCASPath] from a [UserCASPath]).
//
// # Usage Example
//
//	import "bazel-cache-share/pkg/bazelcas"
//
//	// Instantiate the root cache path
//	root := bazelcas.RootCASPath("/home/red/.cache/bazel")
//
//	// Resolve the user-specific output root
//	userRoot := root.User("red")
//	fmt.Println("User Root:", userRoot.String()) // /home/red/.cache/bazel/_bazel_red
//
//	// Access the repository cache CAS path
//	repoCAS := userRoot.RepositoryCache()
//	fmt.Println("Repo CAS:", repoCAS) // /home/red/.cache/bazel/_bazel_red/cache/repos/v1/content_addressable
//
//	// Access a specific workspace output base
//	workspaceBase := userRoot.Workspace("206ef5d7...")
//	fmt.Println("Workspace bazel-out:", workspaceBase.BazelOut()) // .../206ef5d7.../bazel-out
//
// # Concurrency and Thread Safety
//
// Since all path types are defined as string under the hood, they are immutable
// and completely safe for concurrent use across multiple go-routines without locking.
package bazelcas
```

#### pkg/bazelcas/bazelcas.go Outline
```go
package bazelcas

import "io/fs"

// RootCASPath represents the root Bazel cache directory (e.g., $HOME/.cache/bazel).
type RootCASPath string

// String returns the clean string representation of the root path.
func (r RootCASPath) String() string

// User resolves the user-specific output root (UserCASPath).
func (r RootCASPath) User(username string) UserCASPath

// UserCASPath represents the user's output base root (e.g., .../_bazel_[username]).
type UserCASPath string

// String returns the clean string representation of the user path.
func (u UserCASPath) String() string

// Parent returns the RootCASPath of this UserCASPath.
func (u UserCASPath) Parent() RootCASPath

// RepositoryCache resolves the active CAS repository cache directory (RepoCASPath).
func (u UserCASPath) RepositoryCache() RepoCASPath

// Workspace resolves the specific workspace output base (WorkspaceCASPath) by its MD5 hash.
func (u UserCASPath) Workspace(md5 string) WorkspaceCASPath

// WorkspaceCASPath represents the output base directory for a specific workspace.
type WorkspaceCASPath string

// String returns the clean string representation of the workspace path.
func (w WorkspaceCASPath) String() string

// Parent returns the UserCASPath of this WorkspaceCASPath.
func (w WorkspaceCASPath) Parent() UserCASPath

// ExecRoot resolves the execroot directory.
func (w WorkspaceCASPath) ExecRoot() string

// BazelOut resolves the bazel-out directory.
func (w WorkspaceCASPath) BazelOut() string

// External resolves the external repository dependency directory.
func (w WorkspaceCASPath) External() string

// RepoCASPath represents the root of the content-addressable storage (CAS)
// directory within Bazel's repository cache.
type RepoCASPath string

// HashDir resolves the path to the directory containing a specific content hash.
func (r RepoCASPath) HashDir(algo, hash string) HashDirPath

// HashDirPath represents a directory containing a single content-addressed
// archive or dependency file inside the Bazel CAS.
type HashDirPath string

// File returns the absolute path to the actual cached dependency file.
func (h HashDirPath) File() string

// TempFile returns the path to a temporary download file inside the same directory.
func (h HashDirPath) TempFile(suffix string) string

// CacheTree represents the parsed in-memory structure of the Bazel cache.
type CacheTree struct {
	// unexported fields
}

// Users returns the map of usernames to their parsed UserCaches.
func (c *CacheTree) Users() map[string]*UserCache

// UserCache represents the output user root and cache files for a specific system user.
type UserCache struct {
	// unexported fields
}

// Username returns the user's username.
func (u *UserCache) Username() string

// UserPath returns the strongly-typed UserCASPath.
func (u *UserCache) UserPath() UserCASPath

// Workspaces returns the map of workspace MD5s to WorkspaceCaches.
func (u *UserCache) Workspaces() map[string]*WorkspaceCache

// RepositoryCache returns the RepoCache metadata representation.
func (u *UserCache) RepositoryCache() *RepoCache

// RepoCache represents the metadata of the user's Repository Cache (CAS).
type RepoCache struct {
	// unexported fields
}

// Path returns the RepoCASPath.
func (r *RepoCache) Path() RepoCASPath

// Hashes returns the list of SHA-256 content hashes found in the Repository Cache.
func (r *RepoCache) Hashes() []string

// WorkspaceCache represents a discovered workspace output base directory structure.
type WorkspaceCache struct {
	// unexported fields
}

// MD5 returns the workspace MD5.
func (w *WorkspaceCache) MD5() string

// Path returns the WorkspaceCASPath.
func (w *WorkspaceCache) Path() WorkspaceCASPath

// ExternalRepos returns the names of any repositories discovered under the external/ folder.
func (w *WorkspaceCache) ExternalRepos() []string

// Walk walks the provided fs.FS starting at the FS root and builds the [CacheTree].
func Walk(sys fs.FS) (*CacheTree, error)
```

#### pkg/dedupe/doc.go
```go
// Package dedupe provides filesystem walk, byte-by-byte matching,
// and atomic file link replacement capabilities for Bazel caches.
//
// It is designed to run safely concurrent with active Bazel builds by
// executing all links atomically.
package dedupe
```

#### pkg/dedupe/dedupe.go Outline
```go
package dedupe

import (
	"context"
	"os"
)

// FileEntry represents filesystem metadata for a scanned file.
type FileEntry struct {
	Path  string
	Size  int64
	Inode uint64
	Dev   uint64
	Hash  string // Resolved from CAS or calculated selectively for reporting
}

// Config holds runtime options for the deduplication execution.
type Config struct {
	DryRun         bool
	PreferReflink  bool
	MinReportSize  int64
	Verbose        bool
}

// Deduplicator manages the walking, matching, and linking process.
type Deduplicator struct {
	config Config
	casMap map[uint64]string // Inode -> SHA-256 hash resolved from repository cache
}

// NewDeduplicator instantiates a new deduplicator runner.
func NewDeduplicator(cfg Config) *Deduplicator

// Scan walks the workspaces and builds the list of file entries.
func (d *Deduplicator) Scan(ctx context.Context, root string) ([]FileEntry, error)

// Deduplicate executes matching and link replacement.
func (d *Deduplicator) Deduplicate(ctx context.Context, entries []FileEntry) error
```

---

## References

[^1]: [Bazel Output Directory Layout Documentation](https://bazel.build/concepts/output-directories) - Details on how Bazel structures its output bases and execution root.
[^2]: [Bazel Repository Cache Flag Reference](https://bazel.build/reference/command-line-reference#flag--repository_cache) - Command-line flag reference explaining repository download sharing and hardlinking options.
[^3]: [Bazel Hermetic Builds Guide](https://bazel.build/extending/rules#hermetic-builds) - Guidelines on managing read-only build outputs to enforce hermeticity.
[^4]: [ext4 Disk Layout Specification](https://ext4.wiki.kernel.org/index.php/Ext4_Disk_Layout) - Official Linux ext4 filesystem layout, detailing directory entries, inodes, and hard link representation.
[^5]: [POSIX link(2) Standard](https://pubs.opengroup.org/onlinepubs/9699919799/functions/link.html) - Standards definition for the hard link system call.
[^6]: [POSIX unlink(2) Standard](https://pubs.opengroup.org/onlinepubs/9699919799/functions/unlink.html) - Standards definition for unlinking directory entries.
[^7]: [XFS Filesystem: Reference Cloning Design](https://xfs.org/) - XFS documentation detailing the architecture of sharing data blocks across multiple independent inodes.
[^8]: [Btrfs Documentation: Copy-on-Write and Reflinks](https://btrfs.readthedocs.io/en/latest/) - Technical specification of Btrfs's block-sharing and copy-on-write features.
[^9]: [Linux ioctl_ficlonerange(2) Manual Page](https://man7.org/linux/man-pages/man2/ioctl_ficlonerange.2.html) - Documentation for the `ioctl` file-cloning operations in the Linux kernel.

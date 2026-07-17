# bazelmop: Cross-Workspace Bazel Cache Deduplicator

`bazelmop` is a high-performance command-line utility designed to reclaim wasted disk space in Bazel's output cache directory. By identifying byte-for-byte identical files across multiple workspaces, checkouts, and build configurations, `bazelmop` replaces duplicates with filesystem hard links or copy-on-write clones (reflinks), keeping your builds warm while saving gigabytes of storage.

---

## Quickstart

### 1. Build the Binary
Clone the repository and compile the program using Go:
```bash
go build -o bazelmop main.go
```

### 2. Run in Dry-Run Mode (Safe Analysis)
Scan the Bazel cache and report potential space savings without modifying any files on disk:
```bash
./bazelmop --root=$HOME/.cache/bazel --scan-external=true --scan-bazel-out=true --dry-run
```

### 3. Run Deduplication (Reclaim Space)
Perform atomic link replacements to merge duplicate files and reclaim space:
```bash
./bazelmop --root=$HOME/.cache/bazel --scan-external=true --scan-bazel-out=true --dry-run=false
```

---

## Example Abbreviated Output

```markdown
# Bazel Cache Deduplication Report

## Executive Summary

| Category | Files Scanned | Apparent Size | Reclaimable Space | Duplicates | % Reclaimed |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **1. Locally Compiled Build Outputs (`bazel-out/`)** | 524,630 | 76.03 GB | 55.03 GB | 475,080 | 72.4% |
| **2. Extracted Repository Sources (`external/`)** | 956,879 | 79.92 GB | 62.47 GB | 765,258 | 78.2% |
| **Total Cache Profile** | **1,481,509** | **155.95 GB** | **117.51 GB** | **1,240,338** | **75.4%** |

## Detailed Category Views

### 1. Locally Compiled Build Outputs (`bazel-out/`)
| Workspace MD5 | Apparent Size | Reclaimable Space | Duplicates | % Reclaimed |
| :--- | :--- | :--- | :--- | :--- |
| `2041e6ce...` | 4.65 GB | 3.44 GB | 26,341 | 73.8% |
| `b1f5b40a...` | 4.75 GB | 3.36 GB | 26,527 | 70.6% |
```

---

## How the Bazel Cache Works

Bazel maintains all of its build state and cached objects under a user-wide root directory, typically at `~/.cache/bazel/_bazel_[username]/`. 

Inside this directory, space is consumed by three primary categories:

### 1. Repository Cache (`cache/repos/v1/`)
* **What it is**: A central, content-addressable store (CAS) for raw downloaded dependency archives (tarballs, zips, jars) and toolchains.
* **Space profile**: Growing but naturally deduplicated. Bazel only downloads a specific URL/hash once system-wide.

### 2. Extracted Repository Sources (`[workspace_md5]/external/`)
* **What it is**: The decompressed source code, rule configurations, and SDKs extracted from downloaded archives.
* **Why it duplicates**: When Bazel compiles dependencies, it must extract them into the workspace's unique output base. If you have 20 branch checkouts, Bazel extracts and stores **20 separate copies** of the Go SDK, Node packages, and Protobuf compiler rules.

### 3. Locally Compiled Build Outputs (`[workspace_md5]/execroot/[name]/bazel-out/`)
* **What it is**: The objects (`.o`, `.a`), libraries, generated files, and deployable archives (`.jar`, `.war`) compiled during a build.
* **Why it duplicates**:
  * **Across configs**: Building the same target for debugging (`dbg`), optimization (`opt`), and fast-building (`fastbuild`) creates separate output folders.
  * **Across workspaces**: Checking out different branches into separate directories causes Bazel to compile the identical toolchain wrappers and static libraries repeatedly across workspaces.

---

## FAQ

### How does `bazelmop` compare to `bazel clean` and `bazel clean --expunge`?

| Operation | Reclaims Space? | Impact on Next Build | Scope | Non-destructive? |
| :--- | :--- | :--- | :--- | :--- |
| **`bazel clean`** | Yes (local only) | Re-compiles all local targets from scratch | Current workspace | No (deletes local builds) |
| **`bazel clean --expunge`** | Yes (moderate) | Re-downloads, re-extracts, and re-compiles everything | Current workspace | No (wipes output base) |
| **`bazelmop`** | **Yes (massive)** | **Zero impact (builds stay 100% warm/instant)** | **All workspaces** | **Yes (non-destructive)** |

#### 1. `bazel clean`
`bazel clean` merely deletes the `bazel-out/` folder of the **current active workspace**. It does not clean up external repositories, does not touch other branches, and forces your very next build in that workspace to compile all local targets from scratch, wasting developer time.

#### 2. `bazel clean --expunge`
`bazel clean --expunge` completely deletes the output base directory for the current workspace. This includes all extracted external repositories, downloaded tools, and cached metadata. The next build will have to re-download the toolchains from the internet and re-extract them, consuming high network bandwidth and causing a massive build latency penalty.

#### 3. `bazelmop`
`bazelmop` is **non-destructive**. It does not delete any files or wipe caches. Instead, it inspects all workspaces and merges identical files by replacing them with hard links or reflinks. Your build files remain exactly where they are—fully visible to Bazel—meaning **your next builds will be warm and instantaneous**, but you get the space recovery of an expunge across all workspaces combined.

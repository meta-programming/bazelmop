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
./bazelmop report --scan-external=true --scan-bazel-out=true
```

### 3. Run Deduplication (Reclaim Space)
Perform atomic link replacements to merge duplicate files and reclaim space:
```bash
./bazelmop clean --scan-external=true --scan-bazel-out=true
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

Bazel maintains all of its build state, metadata, and compiled artifacts under a user-wide root directory, typically defaulting to `~/.cache/bazel/_bazel_[username]/` on Linux[^1]. 

Here is an example structure of a typical Bazel cache directory layout:

```text
~/.cache/bazel/
└── _bazel_username/
    ├── install/
    │   └── 6e2b9c8.../
    │       ├── bazel (Bazel server binary)
    │       └── embedded_jdk/
    ├── cache/
    │   └── repos/
    │       └── v1/
    │           ├── content_addressable/
    │           │   └── sha256/
    │           │       └── 1b4f4ef.../
    │           │           └── file (Raw downloaded rules_go.tar.gz)
    │           └── metadata/
    │               └── 1b4f4ef... (Download metadata JSON)
    └── 2041e6c... (Workspace Output Base MD5)
        ├── external/
        │   └── rules_go/ (Extracted source code)
        │       └── README.md
        ├── execroot/
        │   └── _main/ (Execution root for workspace)
        │       ├── bazel-out/
        │       │   └── k8-fastbuild/
        │       │       └── bin/
        │       │           └── app_deploy.jar (Compiled local output)
        │       └── external/ (Symlinks to extracted external/ repos)
        └── action_cache/ (Build state databases)
```

Inside this output user root, space is consumed by three primary categories:

### 1. Repository Cache (`cache/repos/v1/`)[^2]
* **What it is**: A central, user-wide content-addressable store (CAS) for raw downloaded dependency archives (tarballs, zips, jars) and toolchains.
* **Space profile**: Growing but naturally deduplicated. Bazel only downloads a specific URL/hash once system-wide, storing it by its SHA-256 hash.

### 2. Extracted Repository Sources (`[workspace_hash]/external/`)[^3]
* **What it is**: The decompressed source code, rule configurations, and toolchains extracted from downloaded archives.
* **Why it duplicates**: When Bazel compiles dependencies, it must extract them into the workspace's unique `outputBase` directory. By default, Bazel copies these extracted files. If you have 20 branch checkouts, Bazel extracts and stores **20 separate copies** of Go compiler libraries, NodeJS packages, or Protobuf rules[^4].

### 3. Locally Compiled Build Outputs (`[workspace_hash]/execroot/[name]/bazel-out/`)[^5]
* **What it is**: The objects (`.o`, `.a`), libraries, generated files, and deployable archives (`.jar`, `.war`) compiled during a local build.
* **Are these symlinks?**: 
  * While Bazel creates symlinks in your local repository directory (like `~/ws/my-project/bazel-out`) to point to the output base, the files **inside the physical output base `bazel-out` directory are typically regular files**.
  * Depending on the build action, actions *can* output symlinks inside `bazel-out` (e.g., symlinked assets, node package layouts). To ensure safety, `bazelmop` explicitly checks if each file is a **regular file** (using `info.Mode().IsRegular()`) and ignores directories, symlinks, or special devices.
* **Why it duplicates**:
  * **Across configurations**: Building the same target under different build configurations (e.g., debug vs optimized, or target platform transitions like `k8-fastbuild` and `k8-opt-exec`) creates separate output folders.
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

---

### Footnotes & Citations

[^1]: **Output Directory Layout**: Officially documented at [Bazel Output Directory Layout](https://bazel.build/remote/output-directory-layout). The layout separates the `installBase` (Bazel binaries) from the `outputBase` (workspace build areas).
[^2]: **Repository Cache**: Refer to [Running Builds with Bazel](https://bazel.build/run/build) details in the user manual. The cache stores downloaded archives and is shared across all workspaces.
[^3]: **External Dependencies**: Bazel handles external dependencies by running repository rules and placing the results under the `external/` folder of each workspace output base. See [Bazel External Dependencies](https://bazel.build/concepts/dependencies) documentation.
[^4]: **Experimental Hardlinks**: Bazel provides a flag `--experimental_repository_cache_hardlinks` to hard-link files from the central repository cache to a workspace's `external/` folder on cache hits. However, this is disabled by default and only shares links *within* the same workspace output base, not across different workspace directories. See the flag in the [Bazel Command-Line Reference](https://bazel.build/reference/command-line-reference).
[^5]: **Execution Root and bazel-out**: The execution root is the working directory for action execution. It contains symlinks to inputs and the `bazel-out/` directory for outputs. Refer to [Bazel Execution Root Layout](https://bazel.build/remote/output-directory-layout#layout-diagram) for the exact path diagrams.

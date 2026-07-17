# TODO: Handling Hard Link Limits (ext4 EMLINK)

Although our current count of workspaces (68) is well below the hard link limits of common filesystems, we should proactively handle scenarios where the link count of a shared inode reaches the filesystem's maximum capacity (e.g., 65,000 links on ext4).

## Planned Improvements

### 1. Detect and Handle `EMLINK`
* **Goal**: Prevent deduplication from failing or crashing the run if a file reaches the maximum link count limit.
* **Action**: Capture the `EMLINK` error (`too many links` / `syscall.EMLINK`) during the `os.Link` operation.
* **Fallback**: 
  1. Fall back to copying the file bytes normally to keep the build working.
  2. Log a warning explaining that the link limit has been reached for the file.

### 2. Inode Link Grouping (Smart Scaled Hard-linking)
* **Goal**: Allow deduplication to continue even after the maximum link count is reached by creating a new "seed" copy.
* **Action**: 
  * If a file needs to be linked but its representative inode already has `MAX_LINKS - threshold` link count:
    1. Create a brand new copy of the file (new inode).
    2. Make this new copy the "representative" file for subsequent duplicate link operations.
  * This effectively groups the links (e.g., 60,000 links on Inode A, and subsequent links on Inode B), scaling hard-linking infinitely.

### 3. Filesystem-Specific Threshold Detection
* **Goal**: Proactively fetch limit thresholds from the filesystem.
* **Action**: Research how to detect filesystem types dynamically (e.g., using `statfs`) to adjust the link-grouping threshold automatically depending on whether the drive is ext4, XFS, Btrfs, or APFS.

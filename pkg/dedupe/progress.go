package dedupe

// Phase is a step of a deduplication run.
type Phase int

const (
	// PhaseIdle means no run is in progress.
	PhaseIdle Phase = iota
	// PhaseScan walks the cache root and stats every file.
	PhaseScan
	// PhaseReadExtents reads where candidate files sit on disk.
	PhaseReadExtents
	// PhaseHash reads and hashes the files that may be duplicates.
	PhaseHash
	// PhaseLink replaces duplicates with reflinks or hard links.
	PhaseLink
)

// Progress reports how far a run has got. Done and Total count files or
// inodes in the current phase; Total is zero when it is not known yet.
type Progress struct {
	Phase   Phase
	Done    int64
	Total   int64
	Message string
}

// Stats describes the outcome of one run.
type Stats struct {
	FilesScanned int64
	FilesHashed  int64
	LinksCreated int64
	LinksFailed  int64
	// ApparentBytes is the total size of every scanned file, counted once
	// per path.
	ApparentBytes int64
	// SharedBytes is how much of ApparentBytes is already stored only once,
	// because paths share an inode or extents, at the start of the run.
	SharedBytes int64
	// ReclaimableBytes is the size of the duplicates found by this run, which
	// it then links unless it is a dry run.
	ReclaimableBytes int64
}

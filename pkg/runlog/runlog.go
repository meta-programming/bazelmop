// Package runlog keeps a history of deduplication runs in a JSON lines file.
//
// Each run is written twice: once when it starts, with outcome "running",
// and again when it ends. A run whose last record still says "running" and
// that is not the current process's run was cut short, for example by the
// process being killed, and is reported as "interrupted".
package runlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Outcome is how a run ended.
type Outcome string

const (
	Running     Outcome = "running"
	Completed   Outcome = "completed"
	Failed      Outcome = "failed"
	Interrupted Outcome = "interrupted"
)

// Run is one deduplication run.
type Run struct {
	ID               string    `json:"id"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end,omitempty"`
	Outcome          Outcome   `json:"outcome"`
	Error            string    `json:"error,omitempty"`
	FilesScanned     int64     `json:"files_scanned,omitempty"`
	FilesHashed      int64     `json:"files_hashed,omitempty"`
	LinksCreated     int64     `json:"links_created,omitempty"`
	LinksFailed      int64     `json:"links_failed,omitempty"`
	ApparentBytes    int64     `json:"apparent_bytes,omitempty"`
	SharedBytes      int64     `json:"shared_bytes,omitempty"`
	ReclaimableBytes int64     `json:"reclaimable_bytes,omitempty"`
	PeakMemoryBytes  int64     `json:"peak_memory_bytes,omitempty"`
	DiskFreeBytes    int64     `json:"disk_free_bytes,omitempty"`
}

// Log appends runs to a file and reads them back.
type Log struct {
	path string

	mu      sync.Mutex
	current string // ID of the run this process has in progress
}

// Open returns a Log stored at path, creating its directory.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Log{path: path}, nil
}

// DefaultPath is $XDG_STATE_HOME/bazelmop/runs.jsonl, falling back to
// ~/.local/state.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "bazelmop", "runs.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "bazelmop", "runs.jsonl"), nil
}

// Start records that run has begun. run.Outcome is set to Running.
func (l *Log) Start(run Run) error {
	run.Outcome = Running
	l.mu.Lock()
	l.current = run.ID
	l.mu.Unlock()
	return l.append(run)
}

// Finish records how run ended.
func (l *Log) Finish(run Run) error {
	l.mu.Lock()
	if l.current == run.ID {
		l.current = ""
	}
	l.mu.Unlock()
	return l.append(run)
}

func (l *Log) append(run Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Recent returns up to limit runs, newest first. A limit of zero or less
// returns every run.
func (l *Log) Recent(limit int) ([]Run, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	byID := make(map[string]Run)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Run
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // a line cut short when the process was killed
		}
		byID[r.ID] = r
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	l.mu.Lock()
	current := l.current
	l.mu.Unlock()

	runs := make([]Run, 0, len(byID))
	for _, r := range byID {
		if r.Outcome == Running && r.ID != current {
			r.Outcome = Interrupted
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Start.After(runs[j].Start) })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

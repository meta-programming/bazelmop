package runlog

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "runs.jsonl")
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// A previous process started a run and was killed.
	old, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Start(Run{ID: "a", Start: t0}); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Start(Run{ID: "b", Start: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(Run{ID: "b", Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour), Outcome: Completed, LinksCreated: 5}); err != nil {
		t.Fatal(err)
	}
	if err := l.Start(Run{ID: "c", Start: t0.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	runs, err := l.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Outcome{}
	for _, r := range runs {
		got[r.ID] = r.Outcome
	}
	want := map[string]Outcome{"a": Interrupted, "b": Completed, "c": Running}
	for id, o := range want {
		if got[id] != o {
			t.Errorf("run %s outcome = %q, want %q", id, got[id], o)
		}
	}
	if len(runs) != 3 || runs[0].ID != "c" || runs[2].ID != "a" {
		t.Errorf("order = %v, want newest first", runs)
	}
	if runs[1].LinksCreated != 5 {
		t.Errorf("finished run lost its stats: %+v", runs[1])
	}

	if top, _ := l.Recent(1); len(top) != 1 || top[0].ID != "c" {
		t.Errorf("Recent(1) = %v", top)
	}
}

func TestRecentWithoutFile(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := l.Recent(10); err != nil || len(runs) != 0 {
		t.Errorf("Recent = %v, %v", runs, err)
	}
}

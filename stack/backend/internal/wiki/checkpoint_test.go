package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoCheckpointRemembersNothing(t *testing.T) {
	t.Parallel()
	var cp Checkpoint = NoCheckpoint{}
	cp.MarkDone(1, 2, 3)
	if cp.Done(1) {
		t.Fatal("NoCheckpoint reported a page done; it must remember nothing")
	}
	if err := cp.Save(); err != nil {
		t.Fatalf("NoCheckpoint.Save() error = %v", err)
	}
}

func TestLoadCheckpointEmptyPathDisablesResume(t *testing.T) {
	t.Parallel()
	cp, err := LoadCheckpoint("")
	if err != nil {
		t.Fatalf("LoadCheckpoint(\"\") error = %v", err)
	}
	if _, ok := cp.(NoCheckpoint); !ok {
		t.Fatalf("LoadCheckpoint(\"\") = %T, want NoCheckpoint", cp)
	}
}

func TestCheckpointRoundTripsAcrossLoads(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "crawl-checkpoint.json")

	cp, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadCheckpoint(missing) error = %v", err)
	}
	if cp.Done(42) {
		t.Fatal("a fresh checkpoint reported page 42 done")
	}
	cp.MarkDone(42, 7, 42) // duplicate is ignored
	if err := cp.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// A fresh load from the same path sees the persisted set (and created the dir).
	reloaded, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadCheckpoint(existing) error = %v", err)
	}
	if !reloaded.Done(42) || !reloaded.Done(7) {
		t.Fatal("reloaded checkpoint lost a resolved page")
	}
	if reloaded.Done(99) {
		t.Fatal("reloaded checkpoint invented a resolved page")
	}
}

func TestCheckpointClearRemovesFileAndState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "crawl-checkpoint.json")
	cp, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadCheckpoint error = %v", err)
	}
	cp.MarkDone(1, 2)
	if err := cp.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("checkpoint file missing before Clear: %v", err)
	}

	if err := cp.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if cp.Done(1) {
		t.Fatal("Clear did not drop the in-memory resolved set")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Clear did not remove the checkpoint file (stat err = %v)", err)
	}
	// Clearing an already-absent file is not an error.
	if err := cp.Clear(); err != nil {
		t.Fatalf("second Clear() error = %v", err)
	}
}

func TestCheckpointCorruptFileTreatedAsFresh(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "crawl-checkpoint.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	cp, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadCheckpoint(corrupt) error = %v, want nil (corrupt is treated as fresh)", err)
	}
	if cp.Done(1) {
		t.Fatal("a corrupt checkpoint must be treated as nothing-done so the run re-crawls")
	}
}

func TestCheckpointSaveIsNoOpWhenUnchanged(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "crawl-checkpoint.json")
	cp, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadCheckpoint error = %v", err)
	}
	// Nothing marked: Save writes no file.
	if err := cp.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Save() wrote a file with nothing marked done (stat err = %v)", err)
	}
}

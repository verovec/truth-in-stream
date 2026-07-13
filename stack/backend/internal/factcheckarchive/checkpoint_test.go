package factcheckarchive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStreamCheckpointPersistsAndResumes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cp.json")

	cp, err := LoadStreamCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadStreamCheckpoint: %v", err)
	}
	cp.MarkDone("q=retraites&pub=")
	cp.MarkDone("q=&pub=lemonde.fr")
	if err := cp.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh load sees the persisted set: a resumed run skips those streams.
	reloaded, err := LoadStreamCheckpoint(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.Done("q=retraites&pub=") || !reloaded.Done("q=&pub=lemonde.fr") {
		t.Fatal("reloaded checkpoint lost drained streams")
	}
	if reloaded.Done("q=immigration&pub=") {
		t.Fatal("reloaded checkpoint reports an undrained stream as done")
	}

	// Clear removes the file so the next scheduled run starts fresh.
	if err := reloaded.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("checkpoint file still present after Clear: %v", statErr)
	}
}

func TestLoadStreamCheckpointEmptyPathDisables(t *testing.T) {
	t.Parallel()
	cp, err := LoadStreamCheckpoint("")
	if err != nil {
		t.Fatalf("LoadStreamCheckpoint: %v", err)
	}
	if _, ok := cp.(NoStreamCheckpoint); !ok {
		t.Fatalf("empty path = %T, want NoStreamCheckpoint", cp)
	}
	cp.MarkDone("x")
	if cp.Done("x") {
		t.Fatal("NoStreamCheckpoint remembered a stream")
	}
}

func TestLoadStreamCheckpointCorruptFileTreatedAsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	cp, err := LoadStreamCheckpoint(path)
	if err != nil {
		t.Fatalf("LoadStreamCheckpoint: %v", err)
	}
	if cp.Done("anything") {
		t.Fatal("corrupt checkpoint should be treated as empty, not skip streams")
	}
}

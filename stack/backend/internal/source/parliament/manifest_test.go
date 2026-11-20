package parliament

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestChangedDetectsNewAndMutated(t *testing.T) {
	t.Parallel()
	m := newManifest()
	m.Set("a", "fp1")
	m.Set("b", "fp2")

	tests := []struct {
		name string
		id   string
		fp   string
		want bool
	}{
		{"unchanged", "a", "fp1", false},
		{"mutated fingerprint", "a", "fp1-new", true},
		{"new id", "c", "fp9", true},
		{"other unchanged", "b", "fp2", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := m.Changed(tc.id, tc.fp); got != tc.want {
				t.Errorf("Changed(%q,%q) = %v, want %v", tc.id, tc.fp, got, tc.want)
			}
		})
	}
}

func TestNilManifestTreatsEverythingAsChanged(t *testing.T) {
	t.Parallel()
	var m *Manifest
	if !m.Changed("x", "fp") {
		t.Error("nil manifest should treat every record as changed")
	}
}

func TestManifestRoundTripsThroughDisk(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "manifest.json")

	first := newManifest()
	first.Set("a", "fp1")
	first.Set("b", "fp2")
	if err := saveManifest(path, first); err != nil {
		t.Fatalf("saveManifest: %v", err)
	}

	loaded, err := loadManifest(path)
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("loaded len = %d, want 2", loaded.Len())
	}
	if loaded.Changed("a", "fp1") || loaded.Changed("b", "fp2") {
		t.Error("round-tripped fingerprints should read as unchanged")
	}
	if !loaded.Changed("a", "fp1-new") {
		t.Error("a mutated fingerprint should read as changed after reload")
	}
}

func TestLoadManifestMissingFileIsEmptyNotError(t *testing.T) {
	t.Parallel()
	m, err := loadManifest(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("loadManifest on missing file: %v", err)
	}
	if m.Len() != 0 || !m.Changed("anything", "fp") {
		t.Error("missing manifest should be empty and treat every record as new")
	}
}

func TestLoadManifestCorruptFileErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}
	if _, err := loadManifest(path); err == nil {
		t.Error("a corrupt manifest must error, not silently force a full re-embed")
	}
}

func TestEmptyManifestPathDisablesCheckpoint(t *testing.T) {
	t.Parallel()
	m, err := loadManifest("")
	if err != nil {
		t.Fatalf("loadManifest(\"\"): %v", err)
	}
	if !m.Changed("x", "fp") {
		t.Error("disabled checkpoint should treat every record as new")
	}
	if err := saveManifest("", m); err != nil {
		t.Errorf("saveManifest(\"\") should be a no-op, got %v", err)
	}
}

func TestFingerprintIsStableAndSensitive(t *testing.T) {
	t.Parallel()
	base := fingerprint("title", "content")
	if base != fingerprint("title", "content") {
		t.Error("fingerprint must be deterministic")
	}
	if base == fingerprint("title", "content changed") {
		t.Error("fingerprint must change when content changes")
	}
	// The NUL separator prevents field-boundary collisions.
	if fingerprint("ab", "c") == fingerprint("a", "bc") {
		t.Error("fingerprint must not collide across field boundaries")
	}
}

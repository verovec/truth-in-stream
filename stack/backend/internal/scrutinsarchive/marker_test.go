package scrutinsarchive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkerRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "marker.json")

	want := marker{ETag: `"abc"`, LastModified: "Wed, 18 Jun 2026 06:24:15 GMT"}
	if err := saveMarker(path, want); err != nil {
		t.Fatalf("saveMarker: %v", err)
	}
	got, err := loadMarker(path)
	if err != nil {
		t.Fatalf("loadMarker: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

func TestLoadMarkerMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := loadMarker(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("loadMarker missing: %v", err)
	}
	if !got.empty() {
		t.Fatalf("missing marker = %+v, want empty", got)
	}
}

func TestLoadMarkerCorruptedIsError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt marker: %v", err)
	}
	if _, err := loadMarker(path); err == nil {
		t.Fatal("loadMarker over corrupt file returned nil error, want error")
	}
}

func TestEmptyMarkerPathIsNoOp(t *testing.T) {
	t.Parallel()
	if err := saveMarker("", marker{ETag: "x"}); err != nil {
		t.Fatalf("saveMarker empty path: %v", err)
	}
	got, err := loadMarker("")
	if err != nil {
		t.Fatalf("loadMarker empty path: %v", err)
	}
	if !got.empty() {
		t.Fatalf("empty-path marker = %+v, want empty", got)
	}
}

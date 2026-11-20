// Package evidencesrc is the shared scaffolding the Phase-2 institutional
// evidence connectors are built on: a conditional-GET [Marker], a per-identifier
// diff [Manifest], the corpus chunk-rendering helpers, and a generic
// [DumpProducer] that downloads a bulk open-data dump, diffs it, and publishes
// only new or changed records as the framework's connector.EvidenceJob.
//
// It is the source-neutral generalisation of the pattern the parliament
// connector established: a producer for a bulk-dump institutional source
// (vie-publique discours metadata, HATVP declarations) is reduced to one
// extract function that turns a downloaded archive into [Record] values, plus a
// thin cmd and one registry entry. The API-per-item sources (Legifrance PISTE)
// reuse the chunk rendering and the manifest diff without the dump download.
//
// Nothing here imports the broker transport: a producer publishes through the
// transport-free [Publisher] port the cmd layer adapts, exactly like every other
// source.
package evidencesrc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Marker is the persisted conditional-GET validators for a source's bulk dump.
// ETag and LastModified are the response headers from the last successful 200
// download; the next run replays them as If-None-Match / If-Modified-Since so an
// unchanged dump returns 304 and the run does no redundant download. A missing or
// unreadable marker is treated as "never fetched", so the first run (or a
// corrupted marker) always downloads.
type Marker struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// Empty reports whether the marker carries neither validator, in which case there
// is nothing to send as a conditional request.
func (m Marker) Empty() bool {
	return m.ETag == "" && m.LastModified == ""
}

// LoadMarker reads the persisted validators from path. A missing file yields a
// zero marker and no error (the dump has never been fetched); any other read or
// decode error is returned so a genuinely broken state surfaces rather than
// silently re-downloading every run. An empty path yields a zero marker.
func LoadMarker(path string) (Marker, error) {
	if path == "" {
		return Marker{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marker{}, nil
		}
		return Marker{}, fmt.Errorf("evidencesrc: read marker %q: %w", path, err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("evidencesrc: decode marker %q: %w", path, err)
	}
	return m, nil
}

// SaveMarker writes the validators to path atomically (write to a temp file in
// the same directory, then rename) so a crash mid-write never leaves a truncated
// marker that would be read as "never fetched" and force a needless re-download.
// An empty path is a no-op.
func SaveMarker(path string, m Marker) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("evidencesrc: encode marker: %w", err)
	}
	return writeFileAtomic(path, data, ".marker-*.tmp")
}

// writeFileAtomic writes data to path by creating a temp file in the same
// directory and renaming it over path, so a reader never observes a half-written
// file. The temp file is removed on any error before the rename.
func writeFileAtomic(path string, data []byte, pattern string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("evidencesrc: create dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("evidencesrc: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("evidencesrc: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("evidencesrc: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("evidencesrc: replace %q: %w", path, err)
	}
	return nil
}

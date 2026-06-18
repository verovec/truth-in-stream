package scrutinsarchive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// marker is the persisted conditional-GET validators for the archive. ETag and
// LastModified are the response headers from the last successful 200 download;
// the next run replays them as If-None-Match / If-Modified-Since so an unchanged
// archive returns 304 and the run does no redundant work. A missing or unreadable
// marker is treated as "never fetched", so the first run (or a corrupted marker)
// always downloads.
type marker struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// empty reports whether the marker carries neither validator, in which case
// there is nothing to send as a conditional request.
func (m marker) empty() bool {
	return m.ETag == "" && m.LastModified == ""
}

// loadMarker reads the persisted validators from path. A missing file yields a
// zero marker and no error (the archive has never been fetched); any other read
// or decode error is returned so a genuinely broken state surface rather than
// silently re-downloading every run.
func loadMarker(path string) (marker, error) {
	if path == "" {
		return marker{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return marker{}, nil
		}
		return marker{}, fmt.Errorf("scrutinsarchive: read marker %q: %w", path, err)
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, fmt.Errorf("scrutinsarchive: decode marker %q: %w", path, err)
	}
	return m, nil
}

// saveMarker writes the validators to path atomically (write to a temp file in
// the same directory, then rename) so a crash mid-write never leaves a truncated
// marker that would be read as "never fetched" and force a needless re-download.
func saveMarker(path string, m marker) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("scrutinsarchive: encode marker: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scrutinsarchive: create marker dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".marker-*.tmp")
	if err != nil {
		return fmt.Errorf("scrutinsarchive: create temp marker: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scrutinsarchive: write temp marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("scrutinsarchive: close temp marker: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("scrutinsarchive: replace marker %q: %w", path, err)
	}
	return nil
}

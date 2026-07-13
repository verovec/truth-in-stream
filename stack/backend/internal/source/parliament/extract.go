package parliament

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"
)

// Bounds shared by the archive readers: a decompressed entry is read in full under
// maxEntryBytes so a zip-bomb entry is rejected before it inflates, and a whole
// download is capped by maxArchiveBytes (enforced in the producer's streamToTemp).
const (
	// maxArchiveBytes caps a downloaded dump so a misconfigured or hostile URL cannot
	// fill the disk; the real dumps (a few hundred MB at most) are well under this.
	maxArchiveBytes = 2 << 30 // 2 GiB
	// maxEntryBytes caps one decompressed entry read into memory, bounding a zip-bomb
	// entry while leaving generous headroom for a large aggregate JSON/XML/SQL file.
	maxEntryBytes = 512 << 20 // 512 MiB
)

// entryParser turns one raw archive entry (one JSON/XML file's bytes) into records.
// A single file may hold one record (a question, a seance) or several (an
// aggregate amendments file).
type entryParser func(source string, raw []byte) ([]record, error)

// extractZipEntries opens the downloaded zip and parses every entry whose name ends
// in ext (case-insensitive), accumulating the records. It stops at the first
// malformed entry so a broken dump is never half-ingested without notice.
func extractZipEntries(source, archivePath, ext string, parse entryParser) ([]record, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("parliament: open dump: %w", err)
	}
	defer func() { _ = zr.Close() }()

	var records []record
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(fileExt(f.Name), ext) {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		recs, err := parse(source, data)
		if err != nil {
			return nil, fmt.Errorf("parliament: entry %q: %w", f.Name, err)
		}
		records = append(records, recs...)
	}
	return records, nil
}

// readZipFile reads one zip entry in full under the size bound.
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("parliament: open entry %q: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("parliament: read entry %q: %w", f.Name, err)
	}
	if len(data) > maxEntryBytes {
		return nil, fmt.Errorf("parliament: entry %q exceeds %d bytes", f.Name, maxEntryBytes)
	}
	return data, nil
}

// fileExt returns the lowercase extension of name (including the dot), or "" when
// it has none. It avoids importing path/filepath for a single suffix check on a
// zip entry name, which always uses forward slashes.
func fileExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || strings.ContainsAny(name[i:], "/") {
		return ""
	}
	return strings.ToLower(name[i:])
}

// scrutinPayload is one Senat scrutin ready to publish to the scrutins queue: its
// stable id (for the manifest diff), a content fingerprint, and the marshaled
// chamber-aware scrutins job body the existing scrutins worker drains.
type scrutinPayload struct {
	id          string
	fingerprint string
	body        []byte
}

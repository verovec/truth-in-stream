package evidencesrc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// manifestVersion tags the persisted manifest schema so a future format change is
// detectable rather than silently misread.
const manifestVersion = 1

// Manifest is the per-identifier incremental-diff checkpoint: it maps each record
// identifier (a stable external id, e.g. a discours id or a declaration uuid) to
// a content fingerprint from the last run. A run fingerprints every record in the
// fresh dump and republishes only the records whose fingerprint is new or
// changed, so a daily run over a bulk dump reprocesses only what actually moved
// rather than the whole corpus.
//
// The zero value is a usable empty manifest (every record reads as new), so the
// first run - or a missing/corrupt checkpoint - republishes everything. It is not
// safe for concurrent mutation; a single producer run owns it.
type Manifest struct {
	fingerprints map[string]string
}

// NewManifest builds an empty manifest ready to accumulate a run's fingerprints.
func NewManifest() *Manifest {
	return &Manifest{fingerprints: make(map[string]string)}
}

// Changed reports whether the record identified by id with content fingerprint fp
// is new or changed relative to this manifest: true when id is absent or its
// stored fingerprint differs. It is the diff decision the producer publishes on.
func (m *Manifest) Changed(id, fp string) bool {
	if m == nil || m.fingerprints == nil {
		return true
	}
	prev, ok := m.fingerprints[id]
	return !ok || prev != fp
}

// Set records id's fingerprint as of this run, so the saved manifest is the full
// snapshot of the current dump for the next run to diff against.
func (m *Manifest) Set(id, fp string) {
	if m.fingerprints == nil {
		m.fingerprints = make(map[string]string)
	}
	m.fingerprints[id] = fp
}

// Len is the number of records the manifest tracks.
func (m *Manifest) Len() int { return len(m.fingerprints) }

// persistedManifest is the on-disk envelope: a version tag plus the fingerprint
// map, so the schema is explicit and evolvable.
type persistedManifest struct {
	Version      int               `json:"version"`
	Fingerprints map[string]string `json:"fingerprints"`
}

// LoadManifest reads the persisted fingerprints from path. A missing file yields
// an empty manifest and no error (never diffed before), so the first run
// republishes everything; any other read or decode error is returned so a
// genuinely broken checkpoint surfaces rather than silently re-embedding the
// whole corpus. An empty path disables the checkpoint (every record reads as
// new).
func LoadManifest(path string) (*Manifest, error) {
	if path == "" {
		return NewManifest(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewManifest(), nil
		}
		return nil, fmt.Errorf("evidencesrc: read manifest %q: %w", path, err)
	}
	var p persistedManifest
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("evidencesrc: decode manifest %q: %w", path, err)
	}
	m := NewManifest()
	for id, fp := range p.Fingerprints {
		m.fingerprints[id] = fp
	}
	return m, nil
}

// SaveManifest writes the fingerprints to path atomically (temp file in the same
// directory, then rename) so a crash mid-write never leaves a truncated manifest
// that would be read as "never diffed" and force a needless full re-embed. An
// empty path is a no-op.
func SaveManifest(path string, m *Manifest) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(persistedManifest{Version: manifestVersion, Fingerprints: m.fingerprints})
	if err != nil {
		return fmt.Errorf("evidencesrc: encode manifest: %w", err)
	}
	return writeFileAtomic(path, data, ".manifest-*.tmp")
}

// Fingerprint reduces the content-bearing parts of a record to a stable short
// digest, so a run can tell an unchanged record from a mutated one without
// storing the whole text. Parts are joined with a NUL separator that cannot
// appear in the source text, so distinct field boundaries never collide.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

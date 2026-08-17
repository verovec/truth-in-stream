package connector

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Manifest is the JSON envelope the shell host tooling reads. It is the same
// declaration [All] returns, marshaled to a stable, indented document so
// scripts/ingest-host.sh can resolve a source's producer, worker, queue, and
// forwarded env with jq instead of a hand-maintained case statement.
type Manifest struct {
	Sources []Descriptor `json:"sources"`
}

//go:embed sources.json
var embeddedManifest []byte

// MarshalManifest renders the registry as the canonical manifest JSON: indented,
// newline-terminated, and in declaration order, so the committed sources.json is
// a byte-stable snapshot a drift test can compare against.
func MarshalManifest() ([]byte, error) {
	out, err := json.MarshalIndent(Manifest{Sources: All()}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("connector: marshal manifest: %w", err)
	}
	return append(out, '\n'), nil
}

// EmbeddedManifest returns the committed sources.json bytes the host scripts
// read. A test asserts it equals MarshalManifest, so the checked-in file can
// never drift from the registry.
func EmbeddedManifest() []byte { return embeddedManifest }

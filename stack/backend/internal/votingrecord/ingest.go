package votingrecord

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Store is the slice of the voting store the ingest needs: one upsert keyed by
// (person, scrutin), which is what makes a bulk re-run idempotent.
type Store interface {
	UpsertVotingRecord(ctx context.Context, record domain.VotingRecord) error
}

// Summary reports what one ingest run wrote.
type Summary struct {
	// Files is the number of scrutin JSON files parsed.
	Files int
	// Records is the number of per-person positions upserted.
	Records int
}

// IngestDir parses every *.json file in dir as an AN scrutin and upserts each
// person's recorded position into store. dir is the unzipped Scrutins.json.zip
// archive (one scrutin per file). Re-running over the same dir overwrites the
// same rows by (person, scrutin), so the ingest is idempotent. The walk is
// sorted by filename for a deterministic, resumable order, and it stops at the
// first error so a malformed file is never silently skipped.
func IngestDir(ctx context.Context, store Store, dir string) (Summary, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Summary{}, fmt.Errorf("votingrecord: read dir %q: %w", dir, err)
	}

	var summary Summary
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		path := filepath.Join(dir, e.Name())
		n, err := ingestFile(ctx, store, path)
		if err != nil {
			return summary, err
		}
		summary.Files++
		summary.Records += n
	}
	return summary, nil
}

// ingestFile parses one scrutin file and upserts its records, returning the
// number written.
func ingestFile(ctx context.Context, store Store, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("votingrecord: read %q: %w", path, err)
	}
	records, err := ParseScrutin(data)
	if err != nil {
		return 0, fmt.Errorf("votingrecord: parse %q: %w", path, err)
	}
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := store.UpsertVotingRecord(ctx, r); err != nil {
			return 0, fmt.Errorf("votingrecord: upsert %q/%q: %w", r.PersonID, r.ScrutinID, err)
		}
	}
	return len(records), nil
}

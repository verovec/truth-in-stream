package claimreviewsite

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
)

// normalizeDate returns an RFC3339 string for the worker, or "" when the date is
// absent or unparseable (the worker stores the zero time as SQL NULL). Outlets emit
// datePublished as either a bare date or RFC3339; both normalise to a stable
// RFC3339 string so a re-run produces byte-identical job bodies (idempotency).
func normalizeDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// marshalJob encodes a claim job for publishing.
func marshalJob(job factcheckjob.ClaimJob) ([]byte, error) {
	return json.Marshal(job)
}

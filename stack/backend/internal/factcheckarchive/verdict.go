package factcheckarchive

import (
	"github.com/verovec/truth-in-stream/backend/internal/claimrating"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// MapTextualRating exposes the shared reviewable rating table (internal/claimrating)
// so the Google-API path and the other claim-corpus producers fold an outlet's
// textual verdict through one table rather than duplicating it. It returns ok=false
// for an empty or unrecognized rating; the Google-API producer skips such a claim
// (it will not store a guessed verdict), whereas the feed and outlet producers map
// an unmapped rating to unverifiable.
func MapTextualRating(rating string) (domain.LiteralVerdict, bool) {
	return claimrating.Lookup(rating)
}

// mapVerdict maps an outlet's textual rating onto the literal verdict axis via the
// shared table, returning ok=false when the rating is empty or unrecognized so the
// producer skips the claim rather than storing an empty verdict that would violate
// the column CHECK constraint.
func mapVerdict(rating string) (domain.LiteralVerdict, bool) {
	return claimrating.Lookup(rating)
}

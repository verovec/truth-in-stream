package datacommons

import (
	"github.com/verovec/truth-in-stream/backend/internal/claimrating"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// normalizeRating maps a feed record's rating onto the literal accuracy axis via
// the shared reviewable table (internal/claimrating): the outlet's textual rating
// (alternateName) first, then the numeric scale, and finally the conservative
// unverifiable fallback. The bool reports whether either path mapped the rating,
// so the caller can count the unverifiable fallbacks. Per the card's policy an
// unmapped rating is stored as unverifiable rather than dropped.
func normalizeRating(r feedRating) (domain.LiteralVerdict, bool) {
	return claimrating.Normalize(r.AlternateName, claimrating.NumericRating{
		Value:    r.RatingValue.val,
		ValueSet: r.RatingValue.set,
		Best:     r.BestRating.val,
		BestSet:  r.BestRating.set,
		Worst:    r.WorstRating.val,
		WorstSet: r.WorstRating.set,
	})
}

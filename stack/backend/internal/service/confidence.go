package service

import "github.com/verovec/truth-in-stream/backend/internal/domain"

// confidenceParams bounds confidence scoring. clusterSize caps how many of the
// strongest matches feed the score (the cluster is already sorted by descending
// similarity, so this keeps only the most relevant evidence). leadWeight and
// bodyWeight scale a Wikipedia evidence hit's corroboration weight by its chunk
// kind: a lead summary is higher-signal than buried body prose, so it weighs
// more. Curated claims are authoritative and always weigh at their full
// similarity.
type confidenceParams struct {
	clusterSize int
	leadWeight  float64
	bodyWeight  float64
}

// computeConfidence aggregates a statement's retrieved cluster into a single
// corroboration score, deterministically and with no further retrieval. It is
// the one place the scoring formula lives.
//
// The formula treats curated-claim verdicts as signed evidence and Wikipedia
// similarity as unsigned corroboration: a corroborating claim and any evidence
// hit add their weight to Supporting, a contradicting claim adds to
// Contradicting, and an unclear claim carries no stance and is ignored. A
// claim's weight is its similarity; an evidence hit's weight is its similarity
// scaled by its chunk-kind weight. Score is the fraction of stance-bearing
// weight that supports the statement, Supporting / (Supporting + Contradicting),
// bounded to [0, 1]; it is 0 when the cluster carries no stance-bearing weight,
// the honest reading that nothing in the corpus corroborates the statement.
func computeConfidence(matches []Match, p confidenceParams) domain.Confidence {
	limit := len(matches)
	if p.clusterSize > 0 && p.clusterSize < limit {
		limit = p.clusterSize
	}

	var supporting, contradicting float64
	items := 0
	for _, m := range matches[:limit] {
		weight := matchWeight(m, p)
		if weight == 0 {
			continue
		}
		if m.Kind == domain.MatchKindClaim && m.Verdict == domain.VerdictContradicts {
			contradicting += weight
		} else {
			supporting += weight
		}
		items++
	}

	total := supporting + contradicting
	score := 0.0
	if total > 0 {
		score = supporting / total
	}
	return domain.Confidence{
		Score:         score,
		Supporting:    supporting,
		Contradicting: contradicting,
		EvidenceItems: items,
	}
}

// matchContributions returns, in match order, the stance-bearing weight each
// match added to the confidence aggregate: matchWeight for a match inside the
// scored cluster cap, and 0 for one beyond the cap (it never fed the score). It
// is the per-match companion to computeConfidence and shares its cluster-cap and
// matchWeight logic, so a surfaced contribution always equals what the score
// actually counted - the two never drift. The returned slice has one entry per
// input match.
func matchContributions(matches []Match, p confidenceParams) []float64 {
	limit := len(matches)
	if p.clusterSize > 0 && p.clusterSize < limit {
		limit = p.clusterSize
	}

	contributions := make([]float64, len(matches))
	for i := 0; i < limit; i++ {
		contributions[i] = matchWeight(matches[i], p)
	}
	return contributions
}

// matchWeight is the stance-bearing weight one match contributes to the score: a
// claim weighs at its similarity unless its verdict is unclear (no stance, zero
// weight), and an evidence hit weighs at its similarity scaled by its chunk-kind
// weight. A non-positive similarity contributes nothing.
func matchWeight(m Match, p confidenceParams) float64 {
	if m.Score <= 0 {
		return 0
	}
	if m.Kind == domain.MatchKindEvidence {
		return m.Score * chunkKindWeight(m.WikiKind, p)
	}
	if m.Verdict == domain.VerdictUnclear {
		return 0
	}
	return m.Score
}

// chunkKindWeight maps a chunk kind to its corroboration weight, defaulting an
// unknown kind to the lead weight so a future chunk classification never silently
// drops to zero evidence weight.
func chunkKindWeight(kind domain.EvidenceChunkKind, p confidenceParams) float64 {
	if kind == domain.EvidenceKindBody {
		return p.bodyWeight
	}
	return p.leadWeight
}

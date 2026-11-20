package wiki

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// charsPerToken is Voyage's documented average for estimating token counts
// from character counts without calling the tokenizer (docs.voyageai.com,
// verified 2026-06). It only feeds the dry-run cost estimate, never billing.
const charsPerToken = 5

// pricePerMTokenUSD is the voyage-4-large price per one million tokens
// (docs.voyageai.com, verified 2026-06: $0.12/M, first 200M tokens free).
const pricePerMTokenUSD = 0.12

// BulkPlan is what a bulk run should do for the dump version it resolved,
// decided by the store from the staging table and the live checkpoint.
type BulkPlan int

const (
	// PlanBuild rebuilds staging from the dump, then embeds and swaps. It is the
	// default: a fresh corpus, an interrupted build, or a staging left from a
	// different dump all fall here.
	PlanBuild BulkPlan = iota
	// PlanResumeEmbed keeps a staging already materialized for this dump and
	// embeds only its remaining chunks before swapping.
	PlanResumeEmbed
	// PlanAlreadyCurrent means the live corpus already serves this dump fully
	// embedded; the run is a no-op.
	PlanAlreadyCurrent
)

// Estimate is the dry-run projection of a bulk-embedding run.
type Estimate struct {
	Pages   int64
	Chunks  int64
	Tokens  int64
	CostUSD float64
}

// EmbedSource is the read side the dry-run estimate needs: how many staging
// chunks remain to embed. The estimate depends on this alone, so it never
// touches the write side.
type EmbedSource interface {
	// StagingRemaining counts the staging chunks, pages, and characters still to
	// embed.
	StagingRemaining(ctx context.Context) (domain.EvidenceRemaining, error)
}

// EstimateBulkEmbed projects the cost of embedding the staging chunks still
// pending, without calling the embedding API. On a freshly built staging this is
// the whole corpus; on a resumed one it is only what is left to embed.
func EstimateBulkEmbed(ctx context.Context, src EmbedSource) (Estimate, error) {
	rem, err := src.StagingRemaining(ctx)
	if err != nil {
		return Estimate{}, fmt.Errorf("wiki: staging remaining: %w", err)
	}
	return EstimateFromRemaining(rem), nil
}

// EstimateFromRemaining projects the embedding cost of a pending chunk set
// without calling the API. The bulk-into-live dry-run uses it directly over the
// live remaining count, since that path has no staging table to estimate from.
func EstimateFromRemaining(rem domain.EvidenceRemaining) Estimate {
	tokens := rem.Chars / charsPerToken
	return Estimate{
		Pages:   rem.Documents,
		Chunks:  rem.Chunks,
		Tokens:  tokens,
		CostUSD: float64(tokens) / 1e6 * pricePerMTokenUSD,
	}
}

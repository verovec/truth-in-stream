package service

import (
	"context"
	"log/slog"
)

// LocalWorthinessScorer scores a statement's check-worthiness locally,
// returning a calibrated probability in [0, 1] that the statement is worth
// fact-checking. Implementations run on-CPU with no external API call; an
// error means the score is unavailable (model missing, inference timeout),
// never a judgment about the text.
type LocalWorthinessScorer interface {
	Score(ctx context.Context, text string) (float64, error)
}

// BandedCascadeClassifier runs the check-worthiness cascade with a local
// scorer between the deterministic heuristic and the generative model:
// heuristic -> local score -> (uncertainty band only) -> model. Scores below
// the band reject and scores at or above its upper bound accept without any
// model call, so the generative gate is consulted only where the local
// classifier is genuinely uncertain. It is itself a ClaimClassifier, so both
// live gate paths pick it up unchanged.
//
// Degradation mirrors CascadeClassifier: a scorer failure falls back to the
// pre-local behavior (model verdict when one is configured, otherwise the
// heuristic's positive decision), and a model failure inside the band keeps
// the heuristic's positive decision. The gate never blocks a session on a
// missing or slow model.
type BandedCascadeClassifier struct {
	heuristic ClaimClassifier
	scorer    LocalWorthinessScorer
	model     CheckWorthinessClassifier
	low       float64
	high      float64
	logger    *slog.Logger
}

// NewBandedCascadeClassifier composes the three-stage cascade. model may be
// nil when no generative gate is configured; in-band statements are then
// accepted, matching the heuristic-only wiring's behavior. The band is
// [low, high): a score below low rejects, a score at or above high accepts,
// anything in between routes to the model. low must not exceed high; both are
// validated at config load. The logger records degradations and must be
// non-nil.
func NewBandedCascadeClassifier(heuristic ClaimClassifier, scorer LocalWorthinessScorer, model CheckWorthinessClassifier, low, high float64, logger *slog.Logger) *BandedCascadeClassifier {
	return &BandedCascadeClassifier{heuristic: heuristic, scorer: scorer, model: model, low: low, high: high, logger: logger}
}

// Classify runs the cascade. The heuristic short-circuits rejects before any
// scoring; the local score then decides clear cases; only the uncertainty
// band reaches the generative model.
func (c *BandedCascadeClassifier) Classify(ctx context.Context, text string) (bool, error) {
	heuristicClaim, err := c.heuristic.Classify(ctx, text)
	if err != nil {
		return false, err
	}
	if !heuristicClaim {
		return false, nil
	}

	score, err := c.scorer.Score(ctx, text)
	if err != nil {
		c.logger.WarnContext(ctx, "local check-worthiness scorer failed; falling back to model cascade", slog.String("error", err.Error()))
		return c.modelOrAccept(ctx, text)
	}
	if score < c.low {
		return false, nil
	}
	if score >= c.high {
		return true, nil
	}
	return c.modelOrAccept(ctx, text)
}

// modelOrAccept resolves a statement the local stage could not decide. With no
// model configured the heuristic's positive decision stands; a model error
// degrades the same way, keeping the gate open through a provider outage.
func (c *BandedCascadeClassifier) modelOrAccept(ctx context.Context, text string) (bool, error) {
	if c.model == nil {
		return true, nil
	}
	worthy, err := c.model.CheckWorthy(ctx, text)
	if err != nil {
		c.logger.WarnContext(ctx, "check-worthiness model failed; falling back to heuristic", slog.String("error", err.Error()))
		return true, nil
	}
	return worthy, nil
}

package service

import (
	"context"
	"log/slog"
)

// CheckWorthinessClassifier is the model stage of the cascade: given a segment
// the heuristic already accepted as a declarative assertion, it decides whether
// the statement is a check-worthy public factual claim rather than casual or
// personal small talk. It is the consumer-defined port for the LLM-backed
// adapter; the cascade depends on this interface, not the client.
type CheckWorthinessClassifier interface {
	CheckWorthy(ctx context.Context, text string) (bool, error)
}

// CascadeClassifier composes the cheap heuristic first-pass with a model-based
// check-worthiness judgment, in that order: the heuristic rejects obvious
// non-claims (questions, greetings, fragments, opinions) for free, and only the
// segments it accepts reach the model. This keeps the model off the easy rejects
// - bounding cost and latency - while the model supplies the judgment a
// word-list cannot: telling a check-worthy public claim from a grammatically
// valid but casual declarative.
//
// It is itself a ClaimClassifier, so it drops into the gate's stage one with no
// change to gate logic. A model error degrades to the heuristic's decision -
// which, by construction, was positive when the model was consulted - so a
// provider outage skips the model judgment rather than disabling fact-checking
// or ending the session.
type CascadeClassifier struct {
	heuristic ClaimClassifier
	model     CheckWorthinessClassifier
	logger    *slog.Logger
}

// NewCascadeClassifier composes a heuristic first-pass with a model-based
// check-worthiness stage. The logger records model-call failures that degrade to
// the heuristic decision; it must be non-nil.
func NewCascadeClassifier(heuristic ClaimClassifier, model CheckWorthinessClassifier, logger *slog.Logger) *CascadeClassifier {
	return &CascadeClassifier{heuristic: heuristic, model: model, logger: logger}
}

// Classify runs the cascade. If the heuristic rejects, the segment is not a
// claim and no model call is made. Otherwise the model decides check-worthiness;
// on a model error the heuristic's positive decision stands and the failure is
// logged, so the gate keeps fact-checking through a provider outage.
func (c *CascadeClassifier) Classify(ctx context.Context, text string) (bool, error) {
	heuristicClaim, err := c.heuristic.Classify(ctx, text)
	if err != nil {
		return false, err
	}
	if !heuristicClaim {
		return false, nil
	}

	worthy, err := c.model.CheckWorthy(ctx, text)
	if err != nil {
		c.logger.WarnContext(ctx, "check-worthiness model failed; falling back to heuristic", slog.String("error", err.Error()))
		return true, nil
	}
	return worthy, nil
}

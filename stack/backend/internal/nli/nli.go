// Package nli scores the stance of retrieved evidence toward a claim with a
// locally-served French NLI cross-encoder (camembertav2-base-xnli, ONNX), so
// clear verdicts are decided without any generative call. FEVER conventions
// apply: the evidence passage is the premise, the claim the hypothesis;
// entailment means the passage supports the claim, contradiction that it
// refutes it, neutral that it does not bear on it.
//
// Like the check-worthiness scorer, the real implementation needs cgo and the
// native ONNX Runtime and tokenizers libraries, so it compiles only under the
// localinference build tag; the default pure-Go build ships a stub whose
// constructor reports the scorer unavailable, and the verify path keeps
// today's LLM-first behavior.
package nli

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// ErrUnavailable reports that this binary was built without the
// localinference build tag, so no local inference substrate is linked in.
var ErrUnavailable = errors.New("nli: built without the localinference build tag")

// Stance is the calibrated probability distribution over the three NLI
// classes for one (passage, claim) pair. The three fields sum to one.
type Stance struct {
	Entailment    float64
	Neutral       float64
	Contradiction float64
}

// Config locates the model artifacts and bounds inference. The upstream ONNX
// artifact is consumed as published, so the calibration temperature is
// applied at runtime rather than folded into the weights; its value comes
// from the training pipeline's calibration run and ships as configuration.
type Config struct {
	// ModelPath is the NLI classifier ONNX graph.
	ModelPath string
	// TokenizerPath is the tokenizer.json published with the model.
	TokenizerPath string
	// LibraryPath is the ONNX Runtime shared library; empty falls back to the
	// loader's platform default lookup.
	LibraryPath string
	// Temperature rescales the logits before softmax (calibration).
	Temperature float64
	// Timeout bounds scoring one claim against all its passages.
	Timeout time.Duration
	// Logger receives shared-runtime diagnostics (a library-path mismatch
	// between scorers); nil logs nothing.
	Logger *slog.Logger
}

func (c Config) validate() error {
	if c.ModelPath == "" {
		return errors.New("nli: model path is required")
	}
	if c.TokenizerPath == "" {
		return errors.New("nli: tokenizer path is required")
	}
	if !(c.Temperature > 0) {
		return fmt.Errorf("nli: temperature must be positive, got %v", c.Temperature)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("nli: timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

// stanceFromLogits converts the model's raw three-class logits (index 0
// entailment, 1 neutral, 2 contradiction - the camembertav2-base-xnli label
// order) into a calibrated distribution via a numerically-stable softmax at
// the configured temperature.
func stanceFromLogits(entail, neutral, contradiction float32, temperature float64) Stance {
	ze := float64(entail) / temperature
	zn := float64(neutral) / temperature
	zc := float64(contradiction) / temperature
	m := math.Max(ze, math.Max(zn, zc))
	ee := math.Exp(ze - m)
	en := math.Exp(zn - m)
	ec := math.Exp(zc - m)
	total := ee + en + ec
	return Stance{Entailment: ee / total, Neutral: en / total, Contradiction: ec / total}
}

// Package localworthy scores check-worthiness with a locally-served ONNX
// text classifier (a fine-tuned CamemBERTa-v2 head), replacing the generative
// gate for clear cases at zero marginal cost. The real scorer needs cgo and
// two native libraries (ONNX Runtime and the HuggingFace tokenizers FFI), so
// it compiles only under the `localinference` build tag; the default pure-Go
// build ships a stub whose constructor reports the scorer unavailable, and
// the caller degrades to the existing heuristic-plus-model cascade. That
// makes "built without the tag" just another instance of the mandatory
// fail-open contract, exercised by the same wiring path as a missing model
// file.
package localworthy

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrUnavailable reports that this binary was built without the localworthy
// build tag, so no local inference substrate is linked in.
var ErrUnavailable = errors.New("localworthy: built without the localinference build tag")

// Config locates the model artifacts and bounds inference. All three paths
// come from configuration, never from the repository: the ONNX model and
// tokenizer are training-pipeline exports distributed via the container image
// or object storage, and the ONNX Runtime shared library is installed next to
// the binary per platform.
type Config struct {
	// ModelPath is the exported ONNX classifier graph.
	ModelPath string
	// TokenizerPath is the HuggingFace tokenizer.json the model was trained
	// with.
	TokenizerPath string
	// LibraryPath is the ONNX Runtime shared library (libonnxruntime.so or
	// .dylib). Empty falls back to the loader's platform default lookup.
	LibraryPath string
	// Timeout bounds a single inference; an overrun returns an error and the
	// cascade falls back rather than stalling the live loop.
	Timeout time.Duration
}

func (c Config) validate() error {
	if c.ModelPath == "" {
		return errors.New("localworthy: model path is required")
	}
	if c.TokenizerPath == "" {
		return errors.New("localworthy: tokenizer path is required")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("localworthy: timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

// positiveProbability converts the classifier's two raw logits into the
// probability of the positive (check-worthy) class via a numerically-stable
// softmax. The training pipeline calibrates the head with temperature scaling
// before export, so this probability is directly comparable to the configured
// band.
func positiveProbability(negative, positive float32) float64 {
	m := math.Max(float64(negative), float64(positive))
	en := math.Exp(float64(negative) - m)
	ep := math.Exp(float64(positive) - m)
	return ep / (en + ep)
}

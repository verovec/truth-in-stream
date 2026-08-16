//go:build localworthy

package localworthy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// maxSequenceTokens caps the encoded input defensively. The exported
// tokenizer.json carries the authoritative truncation; this guard only
// protects the fixed-cost contract if an artifact ships without it.
const maxSequenceTokens = 128

var (
	initOnce sync.Once
	initErr  error
)

// initRuntime loads the ONNX Runtime shared library once per process. The
// library path is global to the runtime, so the first scorer's configuration
// wins; the server wires exactly one.
func initRuntime(libraryPath string) error {
	initOnce.Do(func() {
		if libraryPath != "" {
			ort.SetSharedLibraryPath(libraryPath)
		}
		initErr = ort.InitializeEnvironment()
	})
	return initErr
}

// Scorer serves the fine-tuned check-worthiness classifier in-process on CPU.
// Sessions and tokenizers are safe for concurrent Score calls.
type Scorer struct {
	session    *ort.DynamicAdvancedSession
	tokenizer  *tokenizers.Tokenizer
	inputNames []string
	timeout    time.Duration
}

// New loads the tokenizer and model, then proves the pair usable with a
// warmup inference so a corrupt artifact fails at boot instead of on the
// first live statement. Callers treat any error as "scorer unavailable" and
// keep the existing cascade.
func New(cfg Config) (*Scorer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := initRuntime(cfg.LibraryPath); err != nil {
		return nil, fmt.Errorf("localworthy: initialize onnx runtime: %w", err)
	}

	tk, err := tokenizers.FromFile(cfg.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("localworthy: load tokenizer: %w", err)
	}

	inputs, outputs, err := ort.GetInputOutputInfo(cfg.ModelPath)
	if err != nil {
		_ = tk.Close()
		return nil, fmt.Errorf("localworthy: read model graph: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		_ = tk.Close()
		return nil, errors.New("localworthy: model graph declares no inputs or outputs")
	}
	inputNames := make([]string, len(inputs))
	for i, in := range inputs {
		inputNames[i] = in.Name
	}

	session, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inputNames, []string{outputs[0].Name}, nil)
	if err != nil {
		_ = tk.Close()
		return nil, fmt.Errorf("localworthy: create session: %w", err)
	}

	s := &Scorer{session: session, tokenizer: tk, inputNames: inputNames, timeout: cfg.Timeout}
	if _, err := s.Score(context.Background(), "bonjour"); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("localworthy: health check: %w", err)
	}
	return s, nil
}

// Score returns the calibrated probability that text is check-worthy. The
// configured timeout bounds the call; on overrun the inference goroutine
// finishes in the background and its result is discarded.
func (s *Scorer) Score(ctx context.Context, text string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	type outcome struct {
		p   float64
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := s.infer(text)
		done <- outcome{p: p, err: err}
	}()

	select {
	case <-ctx.Done():
		return 0, fmt.Errorf("localworthy: inference: %w", ctx.Err())
	case out := <-done:
		return out.p, out.err
	}
}

// infer tokenizes, runs the graph, and reads the two-class logits.
func (s *Scorer) infer(text string) (float64, error) {
	enc := s.tokenizer.EncodeWithOptions(text, true, tokenizers.WithReturnAttentionMask(), tokenizers.WithReturnTypeIDs())
	ids := truncateKeepingLast(enc.IDs)
	if len(ids) == 0 {
		return 0, errors.New("localworthy: empty encoding")
	}
	mask := truncateKeepingLast(enc.AttentionMask)
	if len(mask) == 0 {
		// An unpadded single sequence attends to every token; synthesize the
		// mask rather than feeding zeros if a tokenizer omitted it.
		mask = make([]uint32, len(ids))
		for i := range mask {
			mask[i] = 1
		}
	}
	typeIDs := truncateKeepingLast(enc.TypeIDs)

	shape := ort.NewShape(1, int64(len(ids)))
	values := make([]ort.Value, 0, len(s.inputNames))
	defer func() {
		for _, v := range values {
			_ = v.Destroy()
		}
	}()
	for _, name := range s.inputNames {
		var data []uint32
		switch name {
		case "input_ids":
			data = ids
		case "attention_mask":
			data = mask
		case "token_type_ids":
			data = typeIDs
		default:
			return 0, fmt.Errorf("localworthy: model declares unsupported input %q", name)
		}
		tensor, err := ort.NewTensor(shape, toInt64(data, len(ids)))
		if err != nil {
			return 0, fmt.Errorf("localworthy: build %s tensor: %w", name, err)
		}
		values = append(values, tensor)
	}

	outs := []ort.Value{nil}
	if err := s.session.Run(values, outs); err != nil {
		return 0, fmt.Errorf("localworthy: run model: %w", err)
	}
	defer func() { _ = outs[0].Destroy() }()

	logits, ok := outs[0].(*ort.Tensor[float32])
	if !ok {
		return 0, fmt.Errorf("localworthy: unexpected output type %T", outs[0])
	}
	data := logits.GetData()
	if len(data) != 2 {
		return 0, fmt.Errorf("localworthy: expected 2 logits, got %d", len(data))
	}
	return positiveProbability(data[0], data[1]), nil
}

// Close releases the session and tokenizer. The process-wide runtime
// environment stays initialized for the server's lifetime.
func (s *Scorer) Close() error {
	err := s.session.Destroy()
	if tkErr := s.tokenizer.Close(); err == nil {
		err = tkErr
	}
	return err
}

// truncateKeepingLast caps a sequence at maxSequenceTokens while preserving
// the final entry, so a closing special token survives the defensive cut.
func truncateKeepingLast(seq []uint32) []uint32 {
	if len(seq) <= maxSequenceTokens {
		return seq
	}
	out := make([]uint32, maxSequenceTokens)
	copy(out, seq[:maxSequenceTokens-1])
	out[maxSequenceTokens-1] = seq[len(seq)-1]
	return out
}

// toInt64 widens tokenizer output to the int64 tensors the exported graph
// expects, padding with zeros if a tokenizer omitted an optional sequence.
func toInt64(data []uint32, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		if i < len(data) {
			out[i] = int64(data[i])
		}
	}
	return out
}

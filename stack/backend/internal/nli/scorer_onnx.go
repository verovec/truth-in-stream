//go:build localinference

package nli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/daulet/tokenizers"
	"github.com/verovec/truth-in-stream/backend/internal/onnxrt"
	ort "github.com/yalue/onnxruntime_go"
)

// maxPairTokens caps one encoded (premise, hypothesis) pair, matching the
// calibration pipeline's budget; the model was fine-tuned at 160 tokens, so
// 256 is a defensive ceiling, not a target.
const maxPairTokens = 256

// maxHypothesisTokens bounds the claim's share of the pair so a long passage
// can never squeeze the claim out of the window.
const maxHypothesisTokens = 96

// maxConcurrentClaims bounds concurrent ScoreStances calls, mirroring the
// check-worthiness scorer's rationale: a timed-out inference cannot be
// cancelled inside ONNX Runtime, so excess callers fail open immediately and
// the verify path escalates to its existing fallback instead of compounding
// an overload.
const maxConcurrentClaims = 4

// Scorer serves the NLI cross-encoder in-process on CPU. Sessions and
// tokenizers are safe for concurrent use.
type Scorer struct {
	session     *ort.DynamicAdvancedSession
	tokenizer   *tokenizers.Tokenizer
	inputNames  []string
	clsID       uint32
	sepID       uint32
	temperature float64
	timeout     time.Duration
	inflight    chan struct{}
}

// New loads the tokenizer and model, derives the special-token ids from the
// tokenizer itself, and proves the pair pipeline usable with a warmup
// inference so a corrupt artifact fails at boot instead of on the first live
// claim. Callers treat any error as "scorer unavailable" and keep the
// LLM-first verify path.
func New(cfg Config) (*Scorer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := onnxrt.Init(cfg.LibraryPath, cfg.Logger); err != nil {
		return nil, fmt.Errorf("nli: initialize onnx runtime: %w", err)
	}

	tk, err := tokenizers.FromFile(cfg.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("nli: load tokenizer: %w", err)
	}
	// Encoding an empty string with specials yields exactly [CLS, SEP] for
	// this tokenizer family, which pins both ids without hard-coding a vocab.
	specials, _ := tk.Encode("", true)
	if len(specials) != 2 {
		_ = tk.Close()
		return nil, fmt.Errorf("nli: expected [CLS, SEP] from an empty encode, got %d ids", len(specials))
	}

	inputs, outputs, err := ort.GetInputOutputInfo(cfg.ModelPath)
	if err != nil {
		_ = tk.Close()
		return nil, fmt.Errorf("nli: read model graph: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		_ = tk.Close()
		return nil, errors.New("nli: model graph declares no inputs or outputs")
	}
	inputNames := make([]string, len(inputs))
	for i, in := range inputs {
		inputNames[i] = in.Name
	}

	session, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inputNames, []string{outputs[0].Name}, nil)
	if err != nil {
		_ = tk.Close()
		return nil, fmt.Errorf("nli: create session: %w", err)
	}

	s := &Scorer{
		session:     session,
		tokenizer:   tk,
		inputNames:  inputNames,
		clsID:       specials[0],
		sepID:       specials[1],
		temperature: cfg.Temperature,
		timeout:     cfg.Timeout,
		inflight:    make(chan struct{}, maxConcurrentClaims),
	}
	if _, err := s.ScoreStances(context.Background(), "Le chômage baisse.", []string{"Le chômage a baissé de deux points cette année."}); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("nli: health check: %w", err)
	}
	return s, nil
}

// ScoreStances scores the claim against every evidence passage and returns
// one calibrated stance per passage, in order. The configured timeout bounds
// the whole call; on overrun the inference goroutine finishes in the
// background and its result is discarded.
func (s *Scorer) ScoreStances(ctx context.Context, claim string, passages []string) ([]Stance, error) {
	if len(passages) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	select {
	case s.inflight <- struct{}{}:
	default:
		return nil, fmt.Errorf("nli: %d claims already being scored", maxConcurrentClaims)
	}

	type outcome struct {
		stances []Stance
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() { <-s.inflight }()
		stances := make([]Stance, 0, len(passages))
		for _, passage := range passages {
			stance, err := s.inferPair(passage, claim)
			if err != nil {
				done <- outcome{err: err}
				return
			}
			stances = append(stances, stance)
		}
		done <- outcome{stances: stances}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("nli: inference: %w", ctx.Err())
	case out := <-done:
		return out.stances, out.err
	}
}

// inferPair assembles [CLS] premise [SEP] [SEP] hypothesis [SEP] by token
// ids - the tokenizer binding has no pair API, and the calibration pipeline
// proves this assembly identical to the reference pair encoding - then runs
// the graph and reads the three-class logits.
func (s *Scorer) inferPair(premise, hypothesis string) (Stance, error) {
	premiseIDs, _ := s.tokenizer.Encode(premise, false)
	hypothesisIDs, _ := s.tokenizer.Encode(hypothesis, false)
	if len(premiseIDs) == 0 || len(hypothesisIDs) == 0 {
		return Stance{}, errors.New("nli: empty encoding")
	}
	if len(hypothesisIDs) > maxHypothesisTokens {
		hypothesisIDs = hypothesisIDs[:maxHypothesisTokens]
	}
	premiseBudget := maxPairTokens - len(hypothesisIDs) - 4
	if len(premiseIDs) > premiseBudget {
		premiseIDs = premiseIDs[:premiseBudget]
	}

	ids := make([]int64, 0, len(premiseIDs)+len(hypothesisIDs)+4)
	ids = append(ids, int64(s.clsID))
	for _, id := range premiseIDs {
		ids = append(ids, int64(id))
	}
	ids = append(ids, int64(s.sepID), int64(s.sepID))
	for _, id := range hypothesisIDs {
		ids = append(ids, int64(id))
	}
	ids = append(ids, int64(s.sepID))

	shape := ort.NewShape(1, int64(len(ids)))
	values := make([]ort.Value, 0, len(s.inputNames))
	defer func() {
		for _, v := range values {
			_ = v.Destroy()
		}
	}()
	for _, name := range s.inputNames {
		var data []int64
		switch name {
		case "input_ids":
			data = ids
		case "attention_mask":
			data = make([]int64, len(ids))
			for i := range data {
				data[i] = 1
			}
		case "token_type_ids":
			// type_vocab_size is zero for this model family; all-zero segment
			// ids are the reference behavior.
			data = make([]int64, len(ids))
		default:
			return Stance{}, fmt.Errorf("nli: model declares unsupported input %q", name)
		}
		tensor, err := ort.NewTensor(shape, data)
		if err != nil {
			return Stance{}, fmt.Errorf("nli: build %s tensor: %w", name, err)
		}
		values = append(values, tensor)
	}

	outs := []ort.Value{nil}
	if err := s.session.Run(values, outs); err != nil {
		return Stance{}, fmt.Errorf("nli: run model: %w", err)
	}
	defer func() { _ = outs[0].Destroy() }()

	logits, ok := outs[0].(*ort.Tensor[float32])
	if !ok {
		return Stance{}, fmt.Errorf("nli: unexpected output type %T", outs[0])
	}
	data := logits.GetData()
	if len(data) != 3 {
		return Stance{}, fmt.Errorf("nli: expected 3 logits, got %d", len(data))
	}
	return stanceFromLogits(data[0], data[1], data[2], s.temperature), nil
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

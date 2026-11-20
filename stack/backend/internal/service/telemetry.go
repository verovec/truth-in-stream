package service

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ClaimCheckWriter is the slice of the store the telemetry recorder needs:
// batch-appending analytics rows.
type ClaimCheckWriter interface {
	InsertClaimChecks(ctx context.Context, checks []domain.ClaimCheck) error
}

// TelemetryConfig bounds the asynchronous claim-check recorder. QueueDepth is
// the in-memory buffer between the live loop and the writer; BatchSize and
// FlushEvery shape the write batches; SampleRate in (0, 1] keeps only that
// fraction of rows on high-volume sessions. Locale is stamped on every row
// (the session-level pipeline locale).
type TelemetryConfig struct {
	QueueDepth int
	BatchSize  int
	FlushEvery time.Duration
	SampleRate float64
	Locale     string
	Logger     *slog.Logger
}

// TelemetryRecorder buffers claim-check rows and writes them in batches off
// the hot path. Record never blocks: when the buffer is full the row is
// dropped and counted, because a telemetry write must never cost a verdict -
// the same honest shedding the verify pool applies to its own load.
type TelemetryRecorder struct {
	writer ClaimCheckWriter
	cfg    TelemetryConfig
	ch     chan domain.ClaimCheck
	drops  atomic.Int64
	logger *slog.Logger
}

// NewTelemetryRecorder builds a recorder, failing on bounds that would make it
// meaningless. Run must be started for rows to reach the store.
func NewTelemetryRecorder(writer ClaimCheckWriter, cfg TelemetryConfig) (*TelemetryRecorder, error) {
	switch {
	case writer == nil:
		return nil, fmt.Errorf("service: telemetry recorder requires a writer")
	case cfg.QueueDepth < 1:
		return nil, fmt.Errorf("service: telemetry queue depth must be positive, got %d", cfg.QueueDepth)
	case cfg.BatchSize < 1:
		return nil, fmt.Errorf("service: telemetry batch size must be positive, got %d", cfg.BatchSize)
	case cfg.FlushEvery <= 0:
		return nil, fmt.Errorf("service: telemetry flush interval must be positive, got %s", cfg.FlushEvery)
	case cfg.SampleRate <= 0 || cfg.SampleRate > 1:
		return nil, fmt.Errorf("service: telemetry sample rate must be in (0, 1], got %v", cfg.SampleRate)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &TelemetryRecorder{
		writer: writer,
		cfg:    cfg,
		ch:     make(chan domain.ClaimCheck, cfg.QueueDepth),
		logger: logger,
	}, nil
}

// Record enqueues one row without ever blocking the caller: a sampled-out row
// is skipped, a full buffer drops the row and counts it.
func (r *TelemetryRecorder) Record(c domain.ClaimCheck) {
	if r.cfg.SampleRate < 1 && rand.Float64() >= r.cfg.SampleRate {
		return
	}
	if c.OccurredAt.IsZero() {
		c.OccurredAt = time.Now()
	}
	if c.Locale == "" {
		c.Locale = r.cfg.Locale
	}
	select {
	case r.ch <- c:
	default:
		r.drops.Add(1)
	}
}

// Drops reports how many rows were shed on a full buffer since start.
func (r *TelemetryRecorder) Drops() int64 { return r.drops.Load() }

// Run drains the buffer into batched writes until ctx is canceled, then flushes
// what remains. Write failures drop the batch (counted and logged) rather than
// retrying into a growing backlog; telemetry is lossy by contract.
func (r *TelemetryRecorder) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.FlushEvery)
	defer ticker.Stop()

	batch := make([]domain.ClaimCheck, 0, r.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.writer.InsertClaimChecks(wctx, batch)
		cancel()
		if err != nil {
			r.drops.Add(int64(len(batch)))
			r.logger.Warn("telemetry batch write failed; batch dropped",
				slog.Int("rows", len(batch)), slog.String("error", err.Error()))
		}
		batch = batch[:0]
	}

	for {
		select {
		case c := <-r.ch:
			batch = append(batch, c)
			if len(batch) >= r.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
			if n := r.drops.Load(); n > 0 {
				r.logger.Debug("telemetry drops so far", slog.Int64("dropped", n))
			}
		case <-ctx.Done():
			for {
				select {
				case c := <-r.ch:
					batch = append(batch, c)
					if len(batch) >= r.cfg.BatchSize {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// recordCheck forwards a telemetry row when a recorder is wired; without one
// the pipeline behaves exactly as before this card.
func (vp *VerifyPath) recordCheck(c domain.ClaimCheck) {
	if vp.telemetry == nil {
		return
	}
	vp.telemetry.Record(c)
}

// newClaimCheck seeds the telemetry row every live decision shares: the unit
// and claim texts, the speaker, the retrieval quality snapshot, and the
// decision latency measured from the moment the claim entered its lifecycle.
func newClaimCheck(start time.Time, speaker, unitText, claimText, path string, matches []domain.SegmentMatch) domain.ClaimCheck {
	c := domain.ClaimCheck{
		SessionKind:  "live",
		Speaker:      speaker,
		UnitText:     unitText,
		ClaimText:    claimText,
		DecisionPath: path,
		LatencyMS:    time.Since(start).Milliseconds(),
	}
	c.RetrievalCandidates = len(matches)
	for _, m := range matches {
		if m.Similarity > c.RetrievalTop {
			c.RetrievalTop = m.Similarity
		}
		if m.Kind == domain.MatchKindClaim {
			c.RetrievalClaimHits++
		} else {
			c.RetrievalEvidenceHits++
		}
	}
	return c
}

// withVerdict copies the emitted verdict axes onto the row.
func withVerdict(c domain.ClaimCheck, source string, v *VerifiedVerdict) domain.ClaimCheck {
	c.Source = source
	c.Verdict = v.Verdict
	c.Basis = v.Basis
	c.Literal = v.Literal
	c.Confidence = v.Confidence
	return c
}

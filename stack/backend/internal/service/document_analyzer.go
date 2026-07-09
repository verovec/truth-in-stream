package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ErrAnalysisDisabled is returned when an analysis is triggered but the verify
// path is not configured. Upload, extraction, list, and view work regardless;
// only starting analysis surfaces this.
var ErrAnalysisDisabled = errors.New("document analysis is disabled: the verify path is not configured")

// failRecordTimeout bounds the terminal FailDocumentAnalysis write, which runs
// on a fresh context so a run that failed because its own context expired can
// still record the failure.
const failRecordTimeout = 10 * time.Second

// BatchVerifier analyses one text unit outside the live socket and returns the
// resolved per-claim verdicts. *VerifyPath satisfies it via AnalyzeText.
type BatchVerifier interface {
	AnalyzeText(ctx context.Context, gate SegmentPrechecker, text, anchorID string) (BatchUnitResult, error)
}

// analyzerStore is the persistence slice the analyzer needs: the transactional
// job lifecycle plus the stored sentences it iterates. *postgres.Store
// satisfies it structurally.
type analyzerStore interface {
	StartDocumentAnalysis(ctx context.Context, id string) (domain.Document, error)
	ListDocumentSentences(ctx context.Context, id string) ([]domain.DocumentSentence, error)
	RecordDocumentSentenceResult(ctx context.Context, id string, seq int, skip domain.SkipReason, claims []domain.DocumentClaim) error
	CompleteDocumentAnalysis(ctx context.Context, id string) error
	FailDocumentAnalysis(ctx context.Context, id, reason string) error
	RecoverInterruptedAnalyses(ctx context.Context) ([]string, error)
}

// DocumentAnalyzerConfig configures a DocumentAnalyzer. Timeout bounds one whole
// analysis run (all sentences); a run that overruns it is failed and the admin
// reanalyses.
type DocumentAnalyzerConfig struct {
	Timeout time.Duration
}

// DocumentAnalyzer runs a document's stored sentences through the verify path as
// an in-process background job, following the video-ingest async pattern: the
// trigger flips state under a lock and a spawn-injected goroutine processes on
// its own context. When verify is nil the feature is disabled: Start reports
// ErrAnalysisDisabled and upload/view keep working. It holds no HTTP types.
type DocumentAnalyzer struct {
	store   analyzerStore
	verify  BatchVerifier
	gate    SegmentPrechecker
	timeout time.Duration
	logger  *slog.Logger
	// spawn runs the background worker; a real analyzer starts a goroutine, tests
	// inject a synchronous runner. Set only by the constructor and tests.
	spawn func(func())
}

// NewDocumentAnalyzer builds a DocumentAnalyzer. A nil verify (verify path off)
// is allowed and disables analysis; when verify is non-nil the timeout must be
// positive. A nil gate defaults to "check everything", mirroring the live
// analyzer, so a deployment with the check-worthiness gate disabled still
// analyses every sentence rather than failing to start.
func NewDocumentAnalyzer(store analyzerStore, verify BatchVerifier, gate SegmentPrechecker, cfg DocumentAnalyzerConfig, logger *slog.Logger) (*DocumentAnalyzer, error) {
	if store == nil {
		return nil, errors.New("document analyzer: store is required")
	}
	if verify != nil {
		if cfg.Timeout <= 0 {
			return nil, fmt.Errorf("document analyzer: timeout must be positive, got %s", cfg.Timeout)
		}
		if gate == nil {
			gate = allowAllPrechecker{}
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DocumentAnalyzer{
		store:   store,
		verify:  verify,
		gate:    gate,
		timeout: cfg.Timeout,
		logger:  logger,
		spawn:   func(f func()) { go f() },
	}, nil
}

// Enabled reports whether analysis can run (the verify path is configured).
func (a *DocumentAnalyzer) Enabled() bool { return a.verify != nil }

// Start triggers a document's analysis: it claims the document under the
// analysing lock (returning ErrDocumentNotFound, ErrDocumentNotReady, or
// ErrAnalysisInProgress synchronously so the caller maps the status code), then
// spawns the background worker. It returns ErrAnalysisDisabled when the verify
// path is off. The run itself is asynchronous; Start returns as soon as the
// document is locked.
func (a *DocumentAnalyzer) Start(ctx context.Context, id string) error {
	if !a.Enabled() {
		return ErrAnalysisDisabled
	}
	doc, err := a.store.StartDocumentAnalysis(ctx, id)
	if err != nil {
		return err
	}
	a.spawn(func() { a.run(doc.ID) })
	return nil
}

// run processes every stored sentence of a locked document through the verify
// path and persists each result, then marks the run complete. It runs on a
// fresh, timeout-bounded context detached from the trigger request. A gate
// failure or a persistence failure fails the whole run (the admin reanalyses);
// a per-claim retrieval or verify failure is a non-fatal error claim, so a run
// still completes with error claims for the sentences that could not verify.
func (a *DocumentAnalyzer) run(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	sentences, err := a.store.ListDocumentSentences(ctx, id)
	if err != nil {
		a.failRun(id, fmt.Errorf("loading sentences: %w", err))
		return
	}

	for _, sentence := range sentences {
		result, err := a.verify.AnalyzeText(ctx, a.gate, sentence.Text, strconv.Itoa(sentence.Seq))
		if err != nil {
			a.failRun(id, fmt.Errorf("analysing sentence %d: %w", sentence.Seq, err))
			return
		}
		skip, claims := sentenceOutcome(sentence.Seq, result)
		if err := a.store.RecordDocumentSentenceResult(ctx, id, sentence.Seq, skip, claims); err != nil {
			a.failRun(id, fmt.Errorf("recording sentence %d: %w", sentence.Seq, err))
			return
		}
	}

	if err := a.store.CompleteDocumentAnalysis(ctx, id); err != nil {
		// The run's work is persisted; only the terminal flip failed. Record it as
		// a failed run so the document does not linger analysing.
		a.failRun(id, fmt.Errorf("completing run: %w", err))
	}
}

// failRun records a terminal failure with a clear reason on a fresh context, so
// a run that failed because its own context expired can still write the failure.
func (a *DocumentAnalyzer) failRun(id string, cause error) {
	a.logger.Error("document analysis failed", slog.String("document_id", id), slog.Any("err", cause))
	ctx, cancel := context.WithTimeout(context.Background(), failRecordTimeout)
	defer cancel()
	if err := a.store.FailDocumentAnalysis(ctx, id, cause.Error()); err != nil {
		a.logger.Error("recording analysis failure", slog.String("document_id", id), slog.Any("err", err))
	}
}

// Recover flips any document left analysing by a crashed run to failed, so its
// admin can reanalyse. It runs at startup and is independent of whether the
// verify path is currently enabled (a config change could strand a row).
func (a *DocumentAnalyzer) Recover(ctx context.Context) error {
	ids, err := a.store.RecoverInterruptedAnalyses(ctx)
	if err != nil {
		return fmt.Errorf("document analyzer: recover: %w", err)
	}
	if len(ids) > 0 {
		a.logger.Warn("recovered interrupted document analyses", slog.Int("count", len(ids)))
	}
	return nil
}

// sentenceOutcome maps one sentence's batch result to the persisted skip reason
// and claim rows. A gated or claimless sentence carries a skip reason and no
// claims; a check-worthy sentence carries its claim rows and no skip.
func sentenceOutcome(seq int, result BatchUnitResult) (domain.SkipReason, []domain.DocumentClaim) {
	if !result.Checkable {
		return result.SkipReason, nil
	}
	if len(result.Claims) == 0 {
		return domain.SkipReasonNotAClaim, nil
	}
	claims := make([]domain.DocumentClaim, len(result.Claims))
	for i, r := range result.Claims {
		claim := domain.DocumentClaim{
			SentenceSeq: seq,
			ClaimID:     r.Claim.ClaimID,
			Text:        r.Claim.Text,
			Status:      documentClaimStatus(r.Status),
			Source:      string(r.Source),
		}
		if r.Verdict != nil {
			claim.Verdict = r.Verdict.Verdict
			claim.Basis = r.Verdict.Basis
			claim.Literal = r.Verdict.Literal
			claim.Flags = r.Verdict.Flags
			claim.Confidence = r.Verdict.Confidence
			claim.Rationale = r.Verdict.Rationale
			claim.Citations = r.Verdict.Citations
		}
		claims[i] = claim
	}
	return domain.SkipReasonNone, claims
}

// documentClaimStatus maps a batch claim status to the persisted status. The
// batch path never sheds, so only verified and error occur; anything else is
// recorded as error rather than an invalid status.
func documentClaimStatus(s ClaimStatus) domain.DocumentClaimStatus {
	if s == ClaimStatusVerified {
		return domain.DocumentClaimVerified
	}
	return domain.DocumentClaimError
}

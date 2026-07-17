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
// resolved per-claim verdicts. recentContext is the prior text the decomposer
// resolves references against (the previous sentence); it is never analyzed
// itself. *VerifyPath satisfies it via AnalyzeText.
type BatchVerifier interface {
	AnalyzeText(ctx context.Context, gate SegmentPrechecker, text, recentContext, anchorID string) (BatchUnitResult, error)
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
// fresh, timeout-bounded context detached from the trigger request. Only a
// terminal failure - the run's context expiring/canceling, or a persistence
// failure - fails the whole run (the admin reanalyses). A transient gate error
// on one sentence, like a per-claim verify failure, is recorded as an error
// outcome for that sentence and the run continues, so one flaky classify call
// does not discard every sentence already verified.
func (a *DocumentAnalyzer) run(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	sentences, err := a.store.ListDocumentSentences(ctx, id)
	if err != nil {
		a.failRun(id, fmt.Errorf("loading sentences: %w", err))
		return
	}

	// Each sentence decomposes with its predecessor as reference context, so a
	// sentence opening with a pronoun still yields self-contained claims; the
	// context is never analyzed itself.
	recentContext := ""
	for _, sentence := range sentences {
		result, err := a.verify.AnalyzeText(ctx, a.gate, sentence.Text, recentContext, strconv.Itoa(sentence.Seq))
		recentContext = sentence.Text
		if err != nil {
			if ctx.Err() != nil {
				// The run's own context is done (timeout or shutdown): the whole run
				// cannot proceed, so fail it. Sentences already recorded keep their
				// results (per-sentence replace); the admin reanalyses.
				a.failRun(id, fmt.Errorf("analysing sentence %d: %w", sentence.Seq, err))
				return
			}
			// A transient gate failure on a live context is non-fatal: record the
			// sentence as an error outcome and keep going, mirroring how a per-claim
			// verify failure is a non-fatal error claim.
			a.logger.WarnContext(ctx, "document analysis gate failed for a sentence, recording an error and continuing",
				slog.String("document_id", id), slog.Int("seq", sentence.Seq), slog.Any("err", err))
			result = gateErrorResult(sentence.Text)
		}
		skip, claims := a.sentenceOutcome(ctx, id, sentence.Seq, result)
		if err := a.store.RecordDocumentSentenceResult(ctx, id, sentence.Seq, skip, claims); err != nil {
			a.failRun(id, fmt.Errorf("recording sentence %d: %w", sentence.Seq, err))
			return
		}
	}

	a.completeRun(id)
}

// completeRun makes the terminal complete flip on a fresh context. Using the
// run's own timeout context would let a run that consumed most of its budget
// record a spurious failure when only the final flip races the deadline, even
// though every sentence's result is already persisted.
func (a *DocumentAnalyzer) completeRun(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), failRecordTimeout)
	defer cancel()
	if err := a.store.CompleteDocumentAnalysis(ctx, id); err != nil {
		// The run's work is persisted; only the terminal flip failed. Record it as
		// a failed run so the document does not linger analysing.
		a.failRun(id, fmt.Errorf("completing run: %w", err))
	}
}

// gateErrorResult is the batch result recorded for a sentence whose gate call
// failed transiently: one error claim, so the sentence still ends with a real
// per-claim outcome (the "every sentence gets an outcome" contract) and the run
// continues.
func gateErrorResult(text string) BatchUnitResult {
	return BatchUnitResult{Checkable: true, Claims: []BatchClaimResult{
		{Claim: AtomicClaim{ClaimID: "gate-error", Text: text}, Status: ClaimStatusError},
	}}
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

// documentSkipReasons is the vocabulary the document_sentences.skip_reason
// CHECK constraint permits. The verify path's gate emits only not_a_claim, but
// the analyzer takes whatever SegmentPrechecker it is handed, so a reason
// outside this set (e.g. a capacity shed's not_checked) is normalized to none
// rather than being persisted into a CHECK violation that would fail the run.
var documentSkipReasons = map[domain.SkipReason]bool{
	domain.SkipReasonNone:       true,
	domain.SkipReasonNotAClaim:  true,
	domain.SkipReasonNotCovered: true,
}

// documentVerdicts, documentBases, and documentLiterals are the enum
// vocabularies the document_claims CHECK constraints permit. The verifier
// adapter validates most of this, but the LLM tool-schema enum is a soft hint
// the model can violate, so the analyzer clamps here: an out-of-vocabulary
// verdict would otherwise fail the whole document run on a CHECK violation.
var (
	documentVerdicts = map[string]bool{"": true, VerdictCredible: true, VerdictDisputed: true, VerdictUnverifiable: true}
	documentBases    = map[string]bool{"": true, BasisEvidence: true, BasisKnowledge: true}
	documentLiterals = map[string]bool{"": true, string(domain.LiteralAccurate): true, string(domain.LiteralInaccurate): true, string(domain.LiteralUnverifiable): true}
)

// sentenceOutcome maps one sentence's batch result to the persisted skip reason
// and claim rows, validating every enum against the document CHECK vocabularies
// so a stray LLM string can never fail the whole run. A gated or claimless
// sentence carries a skip reason and no claims; a check-worthy sentence carries
// its claim rows and no skip.
func (a *DocumentAnalyzer) sentenceOutcome(ctx context.Context, id string, seq int, result BatchUnitResult) (domain.SkipReason, []domain.DocumentClaim) {
	if !result.Checkable {
		if !documentSkipReasons[result.SkipReason] {
			a.logger.WarnContext(ctx, "document analysis gate returned an unsupported skip reason, recording none",
				slog.String("document_id", id), slog.Int("seq", seq), slog.String("skip_reason", string(result.SkipReason)))
			return domain.SkipReasonNone, nil
		}
		return result.SkipReason, nil
	}
	if len(result.Claims) == 0 {
		return domain.SkipReasonNotAClaim, nil
	}
	claims := make([]domain.DocumentClaim, len(result.Claims))
	for i, r := range result.Claims {
		claims[i] = a.documentClaim(ctx, id, seq, r)
	}
	return domain.SkipReasonNone, claims
}

// documentClaim maps one batch claim result to a persisted claim row, clamping
// any out-of-vocabulary LLM verdict/basis/literal to an error claim so it cannot
// violate a CHECK constraint and fail the run.
func (a *DocumentAnalyzer) documentClaim(ctx context.Context, id string, seq int, r BatchClaimResult) domain.DocumentClaim {
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
	if claim.Status == domain.DocumentClaimVerified && !validClaimEnums(claim) {
		a.logger.WarnContext(ctx, "document analysis verdict outside the enum vocabulary, recording an error claim",
			slog.String("document_id", id), slog.Int("seq", seq),
			slog.String("verdict", claim.Verdict), slog.String("basis", claim.Basis), slog.String("literal", claim.Literal))
		return domain.DocumentClaim{
			SentenceSeq: seq,
			ClaimID:     r.Claim.ClaimID,
			Text:        r.Claim.Text,
			Status:      domain.DocumentClaimError,
		}
	}
	return claim
}

// validClaimEnums reports whether a claim's verdict, basis, and literal all fall
// within the document_claims CHECK vocabularies.
func validClaimEnums(claim domain.DocumentClaim) bool {
	return documentVerdicts[claim.Verdict] && documentBases[claim.Basis] && documentLiterals[claim.Literal]
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

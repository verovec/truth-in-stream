package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeAnalyzerStore is an in-memory analyzerStore for DocumentAnalyzer tests.
type fakeAnalyzerStore struct {
	mu sync.Mutex

	locked    domain.Document
	sentences []domain.DocumentSentence

	startErr   error
	listErr    error
	recordErr  error
	recoverIDs []string
	recoverEr  error

	started   []string
	recorded  []recordedSentence
	completed []string
	failed    []failedRun
}

type recordedSentence struct {
	id     string
	seq    int
	skip   domain.SkipReason
	claims []domain.DocumentClaim
}

type failedRun struct {
	id     string
	reason string
}

func (f *fakeAnalyzerStore) StartDocumentAnalysis(_ context.Context, id string) (domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	if f.startErr != nil {
		return domain.Document{}, f.startErr
	}
	doc := f.locked
	doc.ID = id
	doc.AnalysisStatus = domain.DocumentAnalysisAnalysing
	return doc, nil
}

func (f *fakeAnalyzerStore) ListDocumentSentences(_ context.Context, _ string) ([]domain.DocumentSentence, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.sentences, nil
}

func (f *fakeAnalyzerStore) RecordDocumentSentenceResult(_ context.Context, id string, seq int, skip domain.SkipReason, claims []domain.DocumentClaim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, recordedSentence{id: id, seq: seq, skip: skip, claims: claims})
	return nil
}

func (f *fakeAnalyzerStore) CompleteDocumentAnalysis(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeAnalyzerStore) FailDocumentAnalysis(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failedRun{id: id, reason: reason})
	return nil
}

func (f *fakeAnalyzerStore) RecoverInterruptedAnalyses(_ context.Context) ([]string, error) {
	if f.recoverEr != nil {
		return nil, f.recoverEr
	}
	return f.recoverIDs, nil
}

func (f *fakeAnalyzerStore) snapshot() ([]recordedSentence, []string, []failedRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedSentence(nil), f.recorded...),
		append([]string(nil), f.completed...),
		append([]failedRun(nil), f.failed...)
}

// fakeBatchVerifier returns a canned BatchUnitResult per text.
type fakeBatchVerifier struct {
	byText map[string]BatchUnitResult
	err    map[string]error
}

func (f fakeBatchVerifier) AnalyzeText(_ context.Context, _ SegmentPrechecker, text, _, _ string) (BatchUnitResult, error) {
	if err := f.err[text]; err != nil {
		return BatchUnitResult{}, err
	}
	return f.byText[text], nil
}

// syncAnalyzer builds an analyzer whose spawn runs the worker inline, so a test
// observes the completed run synchronously.
func syncAnalyzer(t *testing.T, store analyzerStore, verify BatchVerifier) *DocumentAnalyzer {
	t.Helper()
	a, err := NewDocumentAnalyzer(store, verify, allowAllPrechecker{}, DocumentAnalyzerConfig{Timeout: time.Minute}, discardLogger())
	if err != nil {
		t.Fatalf("NewDocumentAnalyzer: %v", err)
	}
	a.spawn = func(f func()) { f() }
	return a
}

func TestDocumentAnalyzerDisabled(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{}
	a := syncAnalyzer(t, store, nil) // nil verify = analysis disabled

	if a.Enabled() {
		t.Error("Enabled() true with a nil verifier")
	}
	if err := a.Start(t.Context(), "doc-1"); !errors.Is(err, ErrAnalysisDisabled) {
		t.Errorf("Start err = %v, want ErrAnalysisDisabled", err)
	}
	if len(store.started) != 0 {
		t.Error("disabled Start still hit the store")
	}
}

func TestDocumentAnalyzerRunsToCompletion(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "gated", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "verdict", Occurrence: 1},
		{Seq: 2, Page: 1, Text: "empty", Occurrence: 1},
	}}
	verify := fakeBatchVerifier{byText: map[string]BatchUnitResult{
		"gated": {Checkable: false, SkipReason: domain.SkipReasonNotCovered},
		"verdict": {Checkable: true, Claims: []BatchClaimResult{
			{
				Claim: AtomicClaim{ClaimID: "1-0", Text: "verdict"}, Status: ClaimStatusVerified, Source: SourceVerified,
				Verdict: &VerifiedVerdict{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.9},
			},
			{Claim: AtomicClaim{ClaimID: "1-1", Text: "verdict"}, Status: ClaimStatusError},
		}},
		"empty": {Checkable: true, Claims: nil},
	}}
	a := syncAnalyzer(t, store, verify)

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	recorded, completed, failed := store.snapshot()
	if len(failed) != 0 {
		t.Fatalf("run failed: %+v", failed)
	}
	if len(completed) != 1 || completed[0] != "doc-1" {
		t.Errorf("completed = %v, want [doc-1]", completed)
	}
	if len(recorded) != 3 {
		t.Fatalf("recorded %d sentences, want 3", len(recorded))
	}
	// Sentence 0: gated skip.
	if recorded[0].seq != 0 || recorded[0].skip != domain.SkipReasonNotCovered || len(recorded[0].claims) != 0 {
		t.Errorf("sentence 0 = %+v, want a not_covered skip", recorded[0])
	}
	// Sentence 1: two claims, verdict + error, mapped to domain types.
	if recorded[1].skip != domain.SkipReasonNone || len(recorded[1].claims) != 2 {
		t.Fatalf("sentence 1 = %+v, want two claims no skip", recorded[1])
	}
	c0 := recorded[1].claims[0]
	if c0.SentenceSeq != 1 || c0.ClaimID != "1-0" || c0.Status != domain.DocumentClaimVerified || c0.Verdict != "credible" || c0.Confidence != 0.9 {
		t.Errorf("claim 0 = %+v, want the mapped verified verdict", c0)
	}
	c1 := recorded[1].claims[1]
	if c1.Status != domain.DocumentClaimError || c1.Verdict != "" {
		t.Errorf("claim 1 = %+v, want an error claim with no verdict", c1)
	}
	// Sentence 2: checkable but no claims -> not_a_claim.
	if recorded[2].skip != domain.SkipReasonNotAClaim || len(recorded[2].claims) != 0 {
		t.Errorf("sentence 2 = %+v, want a not_a_claim skip", recorded[2])
	}
}

func TestDocumentAnalyzerStartPropagatesLockErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"not found", domain.ErrDocumentNotFound},
		{"already analysing", domain.ErrAnalysisInProgress},
		{"not ready", domain.ErrDocumentNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeAnalyzerStore{startErr: tc.err}
			a := syncAnalyzer(t, store, fakeBatchVerifier{})
			if err := a.Start(t.Context(), "doc-1"); !errors.Is(err, tc.err) {
				t.Errorf("Start err = %v, want %v", err, tc.err)
			}
			if _, _, failed := store.snapshot(); len(failed) != 0 {
				t.Error("a lock rejection should not spawn a run")
			}
		})
	}
}

// contextRecordingBatchVerifier records the recent context passed per sentence,
// so a test asserts each sentence decomposes with the preceding group of
// sentences as context.
type contextRecordingBatchVerifier struct {
	mu       sync.Mutex
	contexts []string
}

func (f *contextRecordingBatchVerifier) AnalyzeText(_ context.Context, _ SegmentPrechecker, _, recentContext, _ string) (BatchUnitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contexts = append(f.contexts, recentContext)
	return BatchUnitResult{Checkable: false, SkipReason: domain.SkipReasonNotAClaim}, nil
}

func TestDocumentAnalyzerPassesPrecedingSentencesAsContext(t *testing.T) {
	t.Parallel()
	// Six sentences: each decomposes with the group of preceding sentences as
	// context, and the window slides once it holds batchContextSentences, so the
	// sixth sentence sees sentences two through five but not the first.
	store := &fakeAnalyzerStore{sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Premiere phrase.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Elle est fausse.", Occurrence: 1},
		{Seq: 2, Page: 1, Text: "Troisieme phrase.", Occurrence: 1},
		{Seq: 3, Page: 1, Text: "Quatrieme phrase.", Occurrence: 1},
		{Seq: 4, Page: 1, Text: "Cinquieme phrase.", Occurrence: 1},
		{Seq: 5, Page: 1, Text: "Sixieme phrase.", Occurrence: 1},
	}}
	verify := &contextRecordingBatchVerifier{}
	a := syncAnalyzer(t, store, verify)

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := []string{
		"",
		"Premiere phrase.",
		"Premiere phrase. Elle est fausse.",
		"Premiere phrase. Elle est fausse. Troisieme phrase.",
		"Premiere phrase. Elle est fausse. Troisieme phrase. Quatrieme phrase.",
		"Elle est fausse. Troisieme phrase. Quatrieme phrase. Cinquieme phrase.",
	}
	verify.mu.Lock()
	defer verify.mu.Unlock()
	if !slices.Equal(verify.contexts, want) {
		t.Errorf("contexts = %q, want the sliding preceding group %q", verify.contexts, want)
	}
}

// TestDocumentAnalyzerGateErrorIsNonFatal proves a transient gate error on one
// sentence (the run context still live) records that sentence as an error
// outcome and lets the run complete, rather than discarding every other
// sentence's work.
func TestDocumentAnalyzerGateErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "boom", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "fine", Occurrence: 1},
	}}
	verify := fakeBatchVerifier{
		err:    map[string]error{"boom": errors.New("gate down")},
		byText: map[string]BatchUnitResult{"fine": {Checkable: false, SkipReason: domain.SkipReasonNotAClaim}},
	}
	a := syncAnalyzer(t, store, verify)

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	recorded, completed, failed := store.snapshot()
	if len(failed) != 0 {
		t.Fatalf("a transient gate error failed the whole run: %+v", failed)
	}
	if len(completed) != 1 {
		t.Errorf("run did not complete: %v", completed)
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded %d sentences, want both processed", len(recorded))
	}
	if len(recorded[0].claims) != 1 || recorded[0].claims[0].Status != domain.DocumentClaimError {
		t.Errorf("gated-error sentence = %+v, want a single error claim", recorded[0])
	}
	if recorded[1].skip != domain.SkipReasonNotAClaim {
		t.Errorf("sentence 1 = %+v, want its normal skip", recorded[1])
	}
}

// TestDocumentAnalyzerFailsRunOnCanceledContext proves that when the run's own
// context is already done (timeout/shutdown), a gate error fails the whole run
// rather than recording an error claim against a dead context.
func TestDocumentAnalyzerFailsRunOnCanceledContext(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "boom", Occurrence: 1}}}
	verify := fakeBatchVerifier{err: map[string]error{"boom": errors.New("gate down")}}
	a, err := NewDocumentAnalyzer(store, verify, allowAllPrechecker{}, DocumentAnalyzerConfig{Timeout: time.Nanosecond}, discardLogger())
	if err != nil {
		t.Fatalf("NewDocumentAnalyzer: %v", err)
	}
	a.spawn = func(f func()) { f() }

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, completed, failed := store.snapshot()
	if len(completed) != 0 {
		t.Error("a context-canceled run still completed")
	}
	if len(failed) != 1 || failed[0].id != "doc-1" {
		t.Errorf("failed = %+v, want the run failed", failed)
	}
}

// TestDocumentAnalyzerClampsBadVerdict proves an out-of-enum LLM verdict is
// recorded as an error claim rather than being persisted into a CHECK violation
// that would fail the run.
func TestDocumentAnalyzerClampsBadVerdict(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "claim", Occurrence: 1}}}
	verify := fakeBatchVerifier{byText: map[string]BatchUnitResult{
		"claim": {Checkable: true, Claims: []BatchClaimResult{
			{
				Claim: AtomicClaim{ClaimID: "0-0", Text: "claim"}, Status: ClaimStatusVerified, Source: SourceVerified,
				Verdict: &VerifiedVerdict{Verdict: "mostly-credible", Basis: BasisEvidence, Confidence: 0.7},
			},
		}},
	}}
	a := syncAnalyzer(t, store, verify)

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	recorded, completed, failed := store.snapshot()
	if len(failed) != 0 {
		t.Fatalf("a bad verdict failed the run: %+v", failed)
	}
	if len(completed) != 1 || len(recorded) != 1 || len(recorded[0].claims) != 1 {
		t.Fatalf("run outcome = completed %v recorded %+v", completed, recorded)
	}
	c := recorded[0].claims[0]
	if c.Status != domain.DocumentClaimError || c.Verdict != "" {
		t.Errorf("claim = %+v, want an error claim with the out-of-enum verdict cleared", c)
	}
}

func TestDocumentAnalyzerFailsRunOnRecordError(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{
		sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "x", Occurrence: 1}},
		recordErr: errors.New("db down"),
	}
	verify := fakeBatchVerifier{byText: map[string]BatchUnitResult{"x": {Checkable: true, Claims: nil}}}
	a := syncAnalyzer(t, store, verify)

	if err := a.Start(t.Context(), "doc-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, completed, failed := store.snapshot()
	if len(completed) != 0 || len(failed) != 1 {
		t.Errorf("record failure: completed=%v failed=%v, want the run failed", completed, failed)
	}
}

func TestDocumentAnalyzerRecover(t *testing.T) {
	t.Parallel()
	store := &fakeAnalyzerStore{recoverIDs: []string{"a", "b"}}
	a := syncAnalyzer(t, store, fakeBatchVerifier{})
	if err := a.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	failing := &fakeAnalyzerStore{recoverEr: errors.New("db down")}
	a2 := syncAnalyzer(t, failing, fakeBatchVerifier{})
	if err := a2.Recover(t.Context()); err == nil {
		t.Error("Recover swallowed a store error")
	}
}

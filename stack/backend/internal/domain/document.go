package domain

import (
	"context"
	"errors"
	"time"
)

// ErrDocumentNotFound is returned by a DocumentStore when no record matches the
// given id. Callers detect it with errors.Is and map it to a 404; it never
// wraps a transport type.
var ErrDocumentNotFound = errors.New("document not found")

// ErrDocumentNotPending is returned when an extraction is posted for a document
// that is not in the pending state. Extraction runs exactly once per upload;
// re-posting it is a conflict, not a retry.
var ErrDocumentNotPending = errors.New("document is not pending extraction")

// ErrDocumentNotReady is returned when an analysis is triggered on a document
// whose upload is not ready (still pending or failed): there are no stored
// sentences to analyze.
var ErrDocumentNotReady = errors.New("document is not ready for analysis")

// ErrAnalysisInProgress is returned when an analysis is triggered on a document
// whose analysis is already running. The analysing status is the job lock, so a
// concurrent trigger is a conflict.
var ErrAnalysisInProgress = errors.New("document analysis is already in progress")

// DocumentStatus is the upload lifecycle of a document record, mirroring
// VideoStatus: an upload starts Pending, becomes Ready once its extraction is
// stored and its object confirmed in storage, and is Failed when it will never
// become readable.
type DocumentStatus string

const (
	// DocumentStatusPending is an upload whose object or extraction has not yet
	// been confirmed.
	DocumentStatusPending DocumentStatus = "pending"
	// DocumentStatusReady is a document whose PDF and sentences are stored.
	DocumentStatusReady DocumentStatus = "ready"
	// DocumentStatusFailed is an upload that will never become readable.
	DocumentStatusFailed DocumentStatus = "failed"
)

// Valid reports whether s is a known document status.
func (s DocumentStatus) Valid() bool {
	switch s {
	case DocumentStatusPending, DocumentStatusReady, DocumentStatusFailed:
		return true
	default:
		return false
	}
}

// DocumentAnalysisStatus is the fact-check analysis lifecycle of a document,
// orthogonal to the upload lifecycle: a ready document may never have been
// analyzed (None), be mid-run (Analysing), or have finished (Complete/Failed).
// Analysing doubles as the job lock: a concurrent trigger on an analysing
// document is rejected.
type DocumentAnalysisStatus string

const (
	// DocumentAnalysisNone means no analysis has ever started.
	DocumentAnalysisNone DocumentAnalysisStatus = "none"
	// DocumentAnalysisAnalysing means a run is in progress.
	DocumentAnalysisAnalysing DocumentAnalysisStatus = "analysing"
	// DocumentAnalysisComplete means the latest run finished and its claims are
	// stored.
	DocumentAnalysisComplete DocumentAnalysisStatus = "complete"
	// DocumentAnalysisFailed means the latest run ended with an error.
	DocumentAnalysisFailed DocumentAnalysisStatus = "failed"
)

// Valid reports whether s is a known analysis status.
func (s DocumentAnalysisStatus) Valid() bool {
	switch s {
	case DocumentAnalysisNone, DocumentAnalysisAnalysing, DocumentAnalysisComplete, DocumentAnalysisFailed:
		return true
	default:
		return false
	}
}

// DocumentClaimStatus is the outcome of one atomic claim's verification. There
// is no unchecked state: the document analyzer blocks on verify-pool
// backpressure instead of shedding, so a claim either verifies or errors.
type DocumentClaimStatus string

const (
	// DocumentClaimVerified is a claim the verify path judged.
	DocumentClaimVerified DocumentClaimStatus = "verified"
	// DocumentClaimError is a claim whose verification failed.
	DocumentClaimError DocumentClaimStatus = "error"
)

// Valid reports whether s is a known claim status.
func (s DocumentClaimStatus) Valid() bool {
	switch s {
	case DocumentClaimVerified, DocumentClaimError:
		return true
	default:
		return false
	}
}

// DocumentObjectKey is the storage key of a document's original PDF: one folder
// per document under the documents/ prefix, leaving the folder free for future
// derived assets.
func DocumentObjectKey(documentID string) string {
	return "documents/" + documentID + "/original.pdf"
}

// Document is a first-class PDF record: its storage object, upload lifecycle,
// and persisted fact-check analysis state. ID is the canonical string form of
// the row's UUID.
type Document struct {
	ID          string
	Title       string
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	// PageCount is the page total reported by the browser extraction; 0 until
	// the extraction is stored.
	PageCount int
	Status    DocumentStatus
	// AnalysisStatus tracks the persisted fact-check run; upload, list, and view
	// work regardless of it.
	AnalysisStatus DocumentAnalysisStatus
	// AnalysisError is the reason the latest run failed; empty otherwise.
	AnalysisError string
	// SentencesTotal and SentencesProcessed are the run's progress counters,
	// persisted so progress survives a refresh.
	SentencesTotal     int
	SentencesProcessed int
	// AnalyzedAt is when the latest run completed; zero when never completed.
	AnalyzedAt time.Time
	// AnalysisRuns counts completed runs, so a reanalysis is observable.
	AnalysisRuns int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DocumentListItem is one library row: the document plus its verdict summary
// counts, which the library page shows as badges.
type DocumentListItem struct {
	Document
	// CredibleClaims and DisputedClaims count this document's stored claims by
	// verdict; unverifiable claims appear only in the document's own panel.
	CredibleClaims int
	DisputedClaims int
}

// DocumentSentence is one extracted sentence, stored once at upload and reused
// by every analysis run. Text is normalized by the browser extraction with the
// same rules the viewer applies at anchor time, so highlight anchoring is
// deterministic.
type DocumentSentence struct {
	DocumentID string
	// Seq is the sentence's document-order index, dense from 0.
	Seq int
	// Page is the 1-based page the sentence appears on.
	Page int
	Text string
	// Occurrence is the nth identical sentence text on that page (1-based),
	// disambiguating duplicate sentences at anchor time.
	Occurrence int
	// SkipReason records why the analyzer did not verify this sentence;
	// SkipReasonNone means it produced claims (or has not been analyzed).
	SkipReason SkipReason
}

// DocumentClaim is one atomic claim's persisted verdict, wiped and rewritten on
// each analysis run. Verdict, Basis, Literal, and Flags mirror the verify
// path's VerifiedVerdict; Citations carries the retrieved matches in the live
// wire shape so the frontend renders document claims with the same components.
type DocumentClaim struct {
	ID         string
	DocumentID string
	// SentenceSeq joins the claim to its document sentence.
	SentenceSeq int
	// ClaimID is the pipeline correlation id of the atomic claim.
	ClaimID string
	Text    string
	Status  DocumentClaimStatus
	// Source is curated (fast-match) or verified (LLM); empty on an error claim.
	Source string
	// Verdict is the credibility axis (credible/disputed/unverifiable); empty on
	// an error claim.
	Verdict string
	// Basis is evidence or knowledge; empty on an error or curated claim.
	Basis string
	// Literal is the political face-value axis; empty on the credibility-only
	// path.
	Literal string
	// Flags is the political manipulation-flag axis; empty when none apply.
	Flags      []string
	Confidence float64
	Rationale  string
	Citations  []SegmentMatch
	CreatedAt  time.Time
}

// DocumentStore is the persistence port for document records, implemented by
// internal/store/postgres. It holds no transport types.
type DocumentStore interface {
	// CreateDocument inserts d with its caller-minted ID (the object key embeds
	// it) and returns the stored record.
	CreateDocument(ctx context.Context, d Document) (Document, error)
	// GetDocument returns the record with the given id, or ErrDocumentNotFound.
	GetDocument(ctx context.Context, id string) (Document, error)
	// ListDocuments returns every record, newest first, with verdict summary
	// counts.
	ListDocuments(ctx context.Context) ([]DocumentListItem, error)
	// StoreDocumentExtraction atomically stores the extracted sentences, records
	// the page count and sentence total, and flips the pending document to
	// ready. It returns ErrDocumentNotFound for an unknown id and
	// ErrDocumentNotPending when the document is not pending, storing nothing.
	StoreDocumentExtraction(ctx context.Context, id string, pageCount int, sentences []DocumentSentence) (Document, error)
	// ListDocumentSentences returns the document's sentences in document order.
	ListDocumentSentences(ctx context.Context, id string) ([]DocumentSentence, error)
	// ListDocumentClaims returns the document's stored claims in document order.
	ListDocumentClaims(ctx context.Context, id string) ([]DocumentClaim, error)
	// DeleteDocument removes the record and, by cascade, its sentences and
	// claims, or returns ErrDocumentNotFound.
	DeleteDocument(ctx context.Context, id string) error
}

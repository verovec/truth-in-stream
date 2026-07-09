package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the document records port.
var _ domain.DocumentStore = (*Store)(nil)

// CreateDocument inserts a new document record. The id is caller-minted (the
// object key embeds it, so the service must know it before the insert); the
// timestamps are assigned by the database.
func (s *Store) CreateDocument(ctx context.Context, d domain.Document) (domain.Document, error) {
	if !d.Status.Valid() {
		return domain.Document{}, fmt.Errorf("postgres: create document: invalid status %q", d.Status)
	}
	uid, err := uuid.Parse(d.ID)
	if err != nil {
		return domain.Document{}, fmt.Errorf("postgres: create document: parse id %q: %w", d.ID, err)
	}
	row, err := s.queries.CreateDocument(ctx, db.CreateDocumentParams{
		ID:          uid,
		Title:       d.Title,
		ObjectKey:   d.ObjectKey,
		ContentType: d.ContentType,
		SizeBytes:   d.SizeBytes,
		Status:      string(d.Status),
	})
	if err != nil {
		return domain.Document{}, fmt.Errorf("postgres: create document: %w", err)
	}
	return documentFromRow(row), nil
}

// GetDocument returns the record with the given id. An unparseable id, like a
// missing row, maps to domain.ErrDocumentNotFound: neither can name a record.
func (s *Store) GetDocument(ctx context.Context, id string) (domain.Document, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Document{}, domain.ErrDocumentNotFound
	}
	row, err := s.queries.GetDocument(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, domain.ErrDocumentNotFound
	}
	if err != nil {
		return domain.Document{}, fmt.Errorf("postgres: get document %s: %w", id, err)
	}
	return documentFromRow(row), nil
}

// ListDocuments returns every record, newest first, each with its verdict
// summary counts.
func (s *Store) ListDocuments(ctx context.Context) ([]domain.DocumentListItem, error) {
	rows, err := s.queries.ListDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: list documents: %w", err)
	}
	items := make([]domain.DocumentListItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, domain.DocumentListItem{
			Document:       documentFromRow(r.Document),
			CredibleClaims: int(r.CredibleClaims),
			DisputedClaims: int(r.DisputedClaims),
		})
	}
	return items, nil
}

// StoreDocumentExtraction atomically records the extraction: the status-guarded
// update (pending -> ready) and every sentence insert run in one transaction,
// so a rejected extraction leaves no orphaned rows and a document can never be
// ready with half its sentences.
func (s *Store) StoreDocumentExtraction(ctx context.Context, id string, pageCount int, sentences []domain.DocumentSentence) (domain.Document, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Document{}, domain.ErrDocumentNotFound
	}

	var doc domain.Document
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		row, err := q.SetDocumentExtracted(ctx, db.SetDocumentExtractedParams{
			ID:             uid,
			PageCount:      int32(pageCount),
			SentencesTotal: int32(len(sentences)),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The guard matched no row: either the document does not exist or it
			// is no longer pending. Tell the two apart for the caller.
			if _, getErr := q.GetDocument(ctx, uid); getErr != nil {
				if errors.Is(getErr, pgx.ErrNoRows) {
					return domain.ErrDocumentNotFound
				}
				return fmt.Errorf("resolve extraction conflict: %w", getErr)
			}
			return domain.ErrDocumentNotPending
		}
		if err != nil {
			return fmt.Errorf("mark extracted: %w", err)
		}

		params := make([]db.InsertDocumentSentenceParams, len(sentences))
		for i, sentence := range sentences {
			params[i] = db.InsertDocumentSentenceParams{
				DocumentID: uid,
				Seq:        int32(sentence.Seq),
				Page:       int32(sentence.Page),
				Text:       sentence.Text,
				Occurrence: int32(sentence.Occurrence),
			}
		}
		if err := firstBatchError(q.InsertDocumentSentence(ctx, params)); err != nil {
			return fmt.Errorf("insert sentences: %w", err)
		}
		doc = documentFromRow(row)
		return nil
	})
	if errors.Is(err, domain.ErrDocumentNotFound) || errors.Is(err, domain.ErrDocumentNotPending) {
		return domain.Document{}, err
	}
	if err != nil {
		return domain.Document{}, fmt.Errorf("postgres: store extraction %s: %w", id, err)
	}
	return doc, nil
}

// ListDocumentSentences returns the document's sentences in document order. An
// unknown document simply has none.
func (s *Store) ListDocumentSentences(ctx context.Context, id string) ([]domain.DocumentSentence, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return []domain.DocumentSentence{}, nil
	}
	rows, err := s.queries.ListDocumentSentences(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("postgres: list document sentences %s: %w", id, err)
	}
	sentences := make([]domain.DocumentSentence, 0, len(rows))
	for _, r := range rows {
		sentences = append(sentences, domain.DocumentSentence{
			DocumentID: r.DocumentID.String(),
			Seq:        int(r.Seq),
			Page:       int(r.Page),
			Text:       r.Text,
			Occurrence: int(r.Occurrence),
			SkipReason: domain.SkipReason(r.SkipReason),
		})
	}
	return sentences, nil
}

// ListDocumentClaims returns the document's stored claims in document order
// (sentence, then insertion order). An unknown document simply has none.
func (s *Store) ListDocumentClaims(ctx context.Context, id string) ([]domain.DocumentClaim, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return []domain.DocumentClaim{}, nil
	}
	rows, err := s.queries.ListDocumentClaims(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("postgres: list document claims %s: %w", id, err)
	}
	claims := make([]domain.DocumentClaim, 0, len(rows))
	for _, r := range rows {
		citations, err := unmarshalCitations(r.Citations)
		if err != nil {
			return nil, fmt.Errorf("postgres: list document claims %s: claim %s: %w", id, r.ID, err)
		}
		flags := r.Flags
		if flags == nil {
			flags = []string{}
		}
		claims = append(claims, domain.DocumentClaim{
			ID:          r.ID.String(),
			DocumentID:  r.DocumentID.String(),
			SentenceSeq: int(r.SentenceSeq),
			ClaimID:     r.ClaimID,
			Text:        r.Text,
			Status:      domain.DocumentClaimStatus(r.Status),
			Source:      r.Source,
			Verdict:     r.Verdict,
			Basis:       r.Basis,
			Literal:     r.Literal,
			Flags:       flags,
			Confidence:  r.Confidence,
			Rationale:   r.Rationale,
			Citations:   citations,
			CreatedAt:   r.CreatedAt.Time,
		})
	}
	return claims, nil
}

// DeleteDocument removes the record; sentences and claims go with it via
// ON DELETE CASCADE. A missing row, like an unparseable id, is
// domain.ErrDocumentNotFound.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrDocumentNotFound
	}
	deleted, err := s.queries.DeleteDocument(ctx, uid)
	if err != nil {
		return fmt.Errorf("postgres: delete document %s: %w", id, err)
	}
	if deleted == 0 {
		return domain.ErrDocumentNotFound
	}
	return nil
}

// StartDocumentAnalysis claims a ready document for a fresh analysis run in one
// transaction: it flips the document to analysing (the job lock), zeroes the
// progress counter, then wipes the prior run's claims and clears every
// sentence's skip reason. It returns the locked record, or a classified error
// when the guard admits no row: ErrDocumentNotFound (unknown),
// ErrAnalysisInProgress (already analysing), or ErrDocumentNotReady (upload not
// ready).
func (s *Store) StartDocumentAnalysis(ctx context.Context, id string) (domain.Document, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Document{}, domain.ErrDocumentNotFound
	}

	var doc domain.Document
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		row, err := q.LockDocumentForAnalysis(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			// The guard matched no row: resolve why for the caller.
			existing, getErr := q.GetDocument(ctx, uid)
			if errors.Is(getErr, pgx.ErrNoRows) {
				return domain.ErrDocumentNotFound
			}
			if getErr != nil {
				return fmt.Errorf("resolve analysis conflict: %w", getErr)
			}
			if existing.AnalysisStatus == string(domain.DocumentAnalysisAnalysing) {
				return domain.ErrAnalysisInProgress
			}
			return domain.ErrDocumentNotReady
		}
		if err != nil {
			return fmt.Errorf("lock for analysis: %w", err)
		}
		if err := q.DeleteDocumentClaims(ctx, uid); err != nil {
			return fmt.Errorf("wipe claims: %w", err)
		}
		if err := q.ClearDocumentSkipReasons(ctx, uid); err != nil {
			return fmt.Errorf("clear skip reasons: %w", err)
		}
		doc = documentFromRow(row)
		return nil
	})
	switch {
	case errors.Is(err, domain.ErrDocumentNotFound),
		errors.Is(err, domain.ErrAnalysisInProgress),
		errors.Is(err, domain.ErrDocumentNotReady):
		return domain.Document{}, err
	case err != nil:
		return domain.Document{}, fmt.Errorf("postgres: start analysis %s: %w", id, err)
	}
	return doc, nil
}

// RecordDocumentSentenceResult persists one analyzed sentence's outcome and
// advances progress in one transaction: a skipped sentence records its skip
// reason, a check-worthy sentence inserts its claim rows, and either way the
// progress counter is bumped. Bundling the write with the bump keeps progress
// consistent with the stored results after a mid-run refresh.
func (s *Store) RecordDocumentSentenceResult(ctx context.Context, id string, seq int, skipReason domain.SkipReason, claims []domain.DocumentClaim) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrDocumentNotFound
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if skipReason != domain.SkipReasonNone {
			if err := q.SetDocumentSentenceSkipReason(ctx, db.SetDocumentSentenceSkipReasonParams{
				DocumentID: uid,
				Seq:        int32(seq),
				SkipReason: string(skipReason),
			}); err != nil {
				return fmt.Errorf("set skip reason: %w", err)
			}
		}
		if len(claims) > 0 {
			params := make([]db.InsertDocumentClaimParams, len(claims))
			for i, c := range claims {
				citations, err := marshalCitations(c.Citations)
				if err != nil {
					return fmt.Errorf("marshal citations: %w", err)
				}
				flags := c.Flags
				if flags == nil {
					flags = []string{}
				}
				params[i] = db.InsertDocumentClaimParams{
					DocumentID:  uid,
					SentenceSeq: int32(seq),
					ClaimID:     c.ClaimID,
					Text:        c.Text,
					Status:      string(c.Status),
					Source:      c.Source,
					Verdict:     c.Verdict,
					Basis:       c.Basis,
					Literal:     c.Literal,
					Flags:       flags,
					Confidence:  c.Confidence,
					Rationale:   c.Rationale,
					Citations:   citations,
				}
			}
			if err := firstBatchError(q.InsertDocumentClaim(ctx, params)); err != nil {
				return fmt.Errorf("insert claims: %w", err)
			}
		}
		if err := q.BumpDocumentSentencesProcessed(ctx, uid); err != nil {
			return fmt.Errorf("bump progress: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: record sentence result %s seq %d: %w", id, seq, err)
	}
	return nil
}

// CompleteDocumentAnalysis marks a run complete, stamps the completion time, and
// increments the run count.
func (s *Store) CompleteDocumentAnalysis(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrDocumentNotFound
	}
	if err := s.queries.CompleteDocumentAnalysis(ctx, uid); err != nil {
		return fmt.Errorf("postgres: complete analysis %s: %w", id, err)
	}
	return nil
}

// FailDocumentAnalysis records the reason a run failed and flips it to failed so
// the admin can reanalyse.
func (s *Store) FailDocumentAnalysis(ctx context.Context, id, reason string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrDocumentNotFound
	}
	if err := s.queries.FailDocumentAnalysis(ctx, db.FailDocumentAnalysisParams{ID: uid, AnalysisError: reason}); err != nil {
		return fmt.Errorf("postgres: fail analysis %s: %w", id, err)
	}
	return nil
}

// RecoverInterruptedAnalyses flips every document left analysing (the process
// died mid-run) to failed with a clear reason, returning the recovered ids for
// startup logging.
func (s *Store) RecoverInterruptedAnalyses(ctx context.Context) ([]string, error) {
	ids, err := s.queries.RecoverInterruptedAnalyses(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: recover interrupted analyses: %w", err)
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out, nil
}

// marshalCitations encodes a claim's citations as a jsonb array, normalizing a
// nil slice to an empty array so the column never holds SQL NULL or 'null'.
func marshalCitations(citations []domain.SegmentMatch) ([]byte, error) {
	if citations == nil {
		citations = []domain.SegmentMatch{}
	}
	raw, err := json.Marshal(citations)
	if err != nil {
		return nil, fmt.Errorf("marshal citations: %w", err)
	}
	return raw, nil
}

// documentFromRow maps a generated row to the domain type. A NULL analyzed_at
// maps to the zero time.
func documentFromRow(r db.Document) domain.Document {
	return domain.Document{
		ID:                 r.ID.String(),
		Title:              r.Title,
		ObjectKey:          r.ObjectKey,
		ContentType:        r.ContentType,
		SizeBytes:          r.SizeBytes,
		PageCount:          int(r.PageCount),
		Status:             domain.DocumentStatus(r.Status),
		AnalysisStatus:     domain.DocumentAnalysisStatus(r.AnalysisStatus),
		AnalysisError:      r.AnalysisError,
		SentencesTotal:     int(r.SentencesTotal),
		SentencesProcessed: int(r.SentencesProcessed),
		AnalyzedAt:         r.AnalyzedAt.Time,
		AnalysisRuns:       int(r.AnalysisRuns),
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

// unmarshalCitations decodes a document_claims.citations jsonb value, always
// yielding a non-nil slice so consumers and the wire format never see null.
func unmarshalCitations(raw []byte) ([]domain.SegmentMatch, error) {
	citations := []domain.SegmentMatch{}
	if len(raw) == 0 {
		return citations, nil
	}
	if err := json.Unmarshal(raw, &citations); err != nil {
		return nil, fmt.Errorf("unmarshal citations: %w", err)
	}
	if citations == nil {
		citations = []domain.SegmentMatch{}
	}
	return citations, nil
}

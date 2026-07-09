package handler

// PDF document library and upload API.
//
// Documents mirror the video-record pattern: a durable UUID identity, direct
// browser-to-storage transfer via presigned URLs, and a pending -> ready
// lifecycle. Sentences arrive pre-extracted from the browser (the same pdf.js
// engine the viewer uses, so highlight anchoring is deterministic); the backend
// validates and persists them, it never parses PDFs. Every authenticated user
// can read; creating and deleting documents is admin-only, enforced by the
// RequireAdmin wrapper at route registration.
//
//	POST   /api/documents/uploads         mint a presigned PUT and a pending record (admin)
//	POST   /api/documents/{id}/extraction store the browser extraction, mark ready (admin)
//	GET    /api/documents                 list the library with verdict summary counts
//	GET    /api/documents/{id}            metadata plus a presigned PDF URL
//	GET    /api/documents/{id}/claims     sentences joined with analysis claims
//	DELETE /api/documents/{id}            remove the record and its storage object (admin)

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// DocumentService is the slice of the document service the library endpoints
// consume, satisfied by *service.DocumentService.
type DocumentService interface {
	RequestUpload(ctx context.Context, req service.DocumentUploadRequest) (service.DocumentUploadTicket, error)
	IngestExtraction(ctx context.Context, id string, ext service.DocumentExtraction) (domain.Document, error)
	List(ctx context.Context) ([]domain.DocumentListItem, error)
	Get(ctx context.Context, id string) (service.ReadableDocument, error)
	Claims(ctx context.Context, id string) (service.DocumentAnalysis, error)
	Delete(ctx context.Context, id string) error
}

// DocumentAnalyzerService is the slice of the analyzer the reanalyse endpoint
// consumes, satisfied by *service.DocumentAnalyzer. Start claims the document
// and spawns its analysis, returning a lifecycle error the handler maps to a
// status code.
type DocumentAnalyzerService interface {
	Start(ctx context.Context, id string) error
}

// maxExtractionBodyBytes bounds the extraction body: the sentence cap bounds
// the row count, this bounds the raw bytes a client can post before decoding.
const maxExtractionBodyBytes = 8 << 20

type documentUploadRequestBody struct {
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// documentUploadResponse carries the ticket plus the extraction sentence cap,
// so the extract-first client can abort an over-long document before the PUT.
type documentUploadResponse struct {
	DocumentID   string        `json:"document_id"`
	ObjectKey    string        `json:"object_key"`
	Status       string        `json:"status"`
	Upload       presignedJSON `json:"upload"`
	MaxSentences int           `json:"max_sentences"`
}

type extractionSentenceBody struct {
	Seq        int    `json:"seq"`
	Page       int    `json:"page"`
	Text       string `json:"text"`
	Occurrence int    `json:"occurrence"`
}

type documentExtractionBody struct {
	PageCount int                      `json:"page_count"`
	Sentences []extractionSentenceBody `json:"sentences"`
}

// documentJSON is the wire form of one domain.Document. ObjectKey is
// intentionally omitted: clients address a document by id and fetch the PDF
// through the presigned URL on the detail endpoint. AnalysisError and
// AnalyzedAt are omitted when zero so a never-analyzed document's wire form
// stays minimal.
type documentJSON struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	AnalysisStatus     string    `json:"analysis_status"`
	AnalysisError      string    `json:"analysis_error,omitzero"`
	ContentType        string    `json:"content_type"`
	SizeBytes          int64     `json:"size_bytes"`
	PageCount          int       `json:"page_count"`
	SentencesTotal     int       `json:"sentences_total"`
	SentencesProcessed int       `json:"sentences_processed"`
	AnalysisRuns       int       `json:"analysis_runs"`
	AnalyzedAt         time.Time `json:"analyzed_at,omitzero"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// documentListItemJSON is one library row: the document plus its verdict
// summary counts.
type documentListItemJSON struct {
	documentJSON
	CredibleClaims int `json:"credible_claims"`
	DisputedClaims int `json:"disputed_claims"`
}

type listDocumentsResponse struct {
	Documents []documentListItemJSON `json:"documents"`
}

// documentResponse is the detail wire form. PDF is present only once the
// document is ready: before that the object may not exist in storage, so no
// download URL is handed out.
type documentResponse struct {
	documentJSON
	PDF *presignedJSON `json:"pdf,omitzero"`
}

// documentClaimJSON is the wire form of one stored claim. Citations carries
// []domain.SegmentMatch verbatim - the live wire shape - so the frontend
// renders document claims with its existing verdict components.
type documentClaimJSON struct {
	ID         string                `json:"id"`
	ClaimID    string                `json:"claim_id"`
	Text       string                `json:"text"`
	Status     string                `json:"status"`
	Source     string                `json:"source,omitzero"`
	Verdict    string                `json:"verdict,omitzero"`
	Basis      string                `json:"basis,omitzero"`
	Literal    string                `json:"literal,omitzero"`
	Flags      []string              `json:"flags"`
	Confidence float64               `json:"confidence"`
	Rationale  string                `json:"rationale,omitzero"`
	Citations  []domain.SegmentMatch `json:"citations"`
}

// documentSentenceJSON is one sentence with the claims it produced; a skipped
// or unanalyzed sentence carries an empty claims array, never null.
type documentSentenceJSON struct {
	Seq        int                 `json:"seq"`
	Page       int                 `json:"page"`
	Text       string              `json:"text"`
	Occurrence int                 `json:"occurrence"`
	SkipReason string              `json:"skip_reason,omitzero"`
	Claims     []documentClaimJSON `json:"claims"`
}

type documentClaimsResponse struct {
	Document  documentJSON           `json:"document"`
	Sentences []documentSentenceJSON `json:"sentences"`
}

func requestDocumentUploadHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body documentUploadRequestBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}

		ticket, err := svc.RequestUpload(r.Context(), service.DocumentUploadRequest{
			Title:       body.Title,
			ContentType: body.ContentType,
			SizeBytes:   body.SizeBytes,
		})
		switch {
		case errors.Is(err, service.ErrDocumentInvalidTitle):
			httpx.Error(w, http.StatusBadRequest, "title is required")
		case errors.Is(err, service.ErrDocumentInvalidContentType):
			httpx.Error(w, http.StatusUnsupportedMediaType, "only application/pdf is supported")
		case errors.Is(err, service.ErrDocumentInvalidSize):
			httpx.Error(w, http.StatusBadRequest, "declared size is out of range")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusCreated, documentUploadResponse{
				DocumentID:   ticket.Document.ID,
				ObjectKey:    ticket.Document.ObjectKey,
				Status:       string(ticket.Document.Status),
				Upload:       toPresignedJSON(ticket.Upload),
				MaxSentences: ticket.MaxSentences,
			})
		}
	}
}

func documentExtractionHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body documentExtractionBody
		if !decodeJSONBody(w, r, maxExtractionBodyBytes, &body) {
			return
		}

		sentences := make([]domain.DocumentSentence, len(body.Sentences))
		for i, s := range body.Sentences {
			sentences[i] = domain.DocumentSentence{
				Seq:        s.Seq,
				Page:       s.Page,
				Text:       s.Text,
				Occurrence: s.Occurrence,
			}
		}
		doc, err := svc.IngestExtraction(r.Context(), r.PathValue("id"), service.DocumentExtraction{
			PageCount: body.PageCount,
			Sentences: sentences,
		})
		switch {
		case errors.Is(err, domain.ErrDocumentNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown document")
		case errors.Is(err, domain.ErrDocumentNotPending):
			httpx.Error(w, http.StatusConflict, "document is not pending extraction")
		case errors.Is(err, service.ErrDocumentObjectMissing):
			httpx.Error(w, http.StatusConflict, "upload not found in storage")
		case errors.Is(err, service.ErrDocumentExtractionEmpty):
			httpx.Error(w, http.StatusBadRequest, "extraction has no sentences")
		case errors.Is(err, service.ErrDocumentTooManySentences):
			httpx.Error(w, http.StatusBadRequest, "extraction exceeds the sentence cap")
		case errors.Is(err, service.ErrDocumentInvalidExtraction):
			httpx.Error(w, http.StatusBadRequest, "extraction is malformed")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toDocumentJSON(doc))
		}
	}
}

func listDocumentsHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := svc.List(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		out := make([]documentListItemJSON, 0, len(items))
		for _, item := range items {
			out = append(out, documentListItemJSON{
				documentJSON:   toDocumentJSON(item.Document),
				CredibleClaims: item.CredibleClaims,
				DisputedClaims: item.DisputedClaims,
			})
		}
		httpx.JSON(w, http.StatusOK, listDocumentsResponse{Documents: out})
	}
}

func getDocumentHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		readable, err := svc.Get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrDocumentNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown document")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			resp := documentResponse{documentJSON: toDocumentJSON(readable.Document)}
			if readable.PDF.URL != "" {
				pdf := toPresignedJSON(readable.PDF)
				resp.PDF = &pdf
			}
			httpx.JSON(w, http.StatusOK, resp)
		}
	}
}

func documentClaimsHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		analysis, err := svc.Claims(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrDocumentNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown document")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			sentences := make([]documentSentenceJSON, 0, len(analysis.Sentences))
			for _, sc := range analysis.Sentences {
				sentences = append(sentences, toDocumentSentenceJSON(sc))
			}
			httpx.JSON(w, http.StatusOK, documentClaimsResponse{
				Document:  toDocumentJSON(analysis.Document),
				Sentences: sentences,
			})
		}
	}
}

// reanalyseDocumentHandler triggers a fresh analysis run over a document's
// stored sentences and returns 202; the run proceeds in the background. A
// document that does not exist, is not ready, or is already analysing maps to
// its own status; when the verify path is not configured the endpoint reports
// analysis is unavailable. Upload, list, and view work regardless.
func reanalyseDocumentHandler(svc DocumentAnalyzerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.Start(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrDocumentNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown document")
		case errors.Is(err, domain.ErrDocumentNotReady):
			httpx.Error(w, http.StatusConflict, "document is not ready for analysis")
		case errors.Is(err, domain.ErrAnalysisInProgress):
			httpx.Error(w, http.StatusConflict, "analysis is already in progress")
		case errors.Is(err, service.ErrAnalysisDisabled):
			httpx.Error(w, http.StatusServiceUnavailable, "analysis is not available")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

func deleteDocumentHandler(svc DocumentService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.Delete(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrDocumentNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown document")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func toDocumentJSON(d domain.Document) documentJSON {
	return documentJSON{
		ID:                 d.ID,
		Title:              d.Title,
		Status:             string(d.Status),
		AnalysisStatus:     string(d.AnalysisStatus),
		AnalysisError:      d.AnalysisError,
		ContentType:        d.ContentType,
		SizeBytes:          d.SizeBytes,
		PageCount:          d.PageCount,
		SentencesTotal:     d.SentencesTotal,
		SentencesProcessed: d.SentencesProcessed,
		AnalysisRuns:       d.AnalysisRuns,
		AnalyzedAt:         d.AnalyzedAt,
		CreatedAt:          d.CreatedAt,
		UpdatedAt:          d.UpdatedAt,
	}
}

func toDocumentSentenceJSON(sc service.DocumentSentenceClaims) documentSentenceJSON {
	claims := make([]documentClaimJSON, 0, len(sc.Claims))
	for _, c := range sc.Claims {
		flags := c.Flags
		if flags == nil {
			flags = []string{}
		}
		citations := c.Citations
		if citations == nil {
			citations = []domain.SegmentMatch{}
		}
		claims = append(claims, documentClaimJSON{
			ID:         c.ID,
			ClaimID:    c.ClaimID,
			Text:       c.Text,
			Status:     string(c.Status),
			Source:     c.Source,
			Verdict:    c.Verdict,
			Basis:      c.Basis,
			Literal:    c.Literal,
			Flags:      flags,
			Confidence: c.Confidence,
			Rationale:  c.Rationale,
			Citations:  citations,
		})
	}
	return documentSentenceJSON{
		Seq:        sc.Sentence.Seq,
		Page:       sc.Sentence.Page,
		Text:       sc.Sentence.Text,
		Occurrence: sc.Sentence.Occurrence,
		SkipReason: string(sc.Sentence.SkipReason),
		Claims:     claims,
	}
}

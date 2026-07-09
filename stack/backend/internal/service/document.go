package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Document service errors. They classify a bad request so the handler maps each
// to its own status code; ErrDocumentNotFound and ErrDocumentNotPending live in
// the domain package because the store raises them.
var (
	// ErrDocumentInvalidTitle is returned when an upload request carries no title.
	ErrDocumentInvalidTitle = errors.New("document: title is required")
	// ErrDocumentInvalidContentType is returned for any content type other than
	// application/pdf.
	ErrDocumentInvalidContentType = errors.New("document: unsupported content type")
	// ErrDocumentInvalidSize is returned when the declared upload size is
	// non-positive or exceeds the configured maximum.
	ErrDocumentInvalidSize = errors.New("document: declared size out of range")
	// ErrDocumentObjectMissing is returned by IngestExtraction when the PDF is
	// not yet present in storage, so the record stays pending and the client can
	// retry after its PUT completes.
	ErrDocumentObjectMissing = errors.New("document: object not found in storage")
	// ErrDocumentExtractionEmpty is returned for an extraction with no sentences:
	// a PDF with no extractable text is rejected in the browser, so an empty
	// extraction is a client bug.
	ErrDocumentExtractionEmpty = errors.New("document: extraction has no sentences")
	// ErrDocumentTooManySentences is returned when an extraction exceeds the
	// configured sentence cap that bounds LLM cost.
	ErrDocumentTooManySentences = errors.New("document: extraction exceeds the sentence cap")
	// ErrDocumentInvalidExtraction is returned for a malformed extraction: a page
	// count below one, a non-dense sentence sequence, a page outside the
	// document, blank sentence text, or an occurrence below one.
	ErrDocumentInvalidExtraction = errors.New("document: extraction is malformed")
)

// documentContentType is the only content type the document API accepts.
const documentContentType = "application/pdf"

// maxDocumentPageCount bounds the client-declared page count and, through the
// per-sentence page check, every page number. It is far above any real PDF and
// far below the int32 range the store narrows to, so a declared value can
// never wrap at insert.
const maxDocumentPageCount = 100_000

// maxDocumentSentenceBytes bounds one sentence's text. The sentence cap bounds
// how many analysis units a document yields; this bounds how large a single
// unit can be, so a mis-segmented extraction cannot smuggle a whole PDF into
// one "sentence" and blow up the analyzer's LLM prompt.
const maxDocumentSentenceBytes = 4096

// DocumentUploadRequest is the input to RequestUpload: the operator-supplied
// title, the declared content type, and the declared size in bytes.
type DocumentUploadRequest struct {
	Title       string
	ContentType string
	SizeBytes   int64
}

// DocumentUploadTicket pairs the created (pending) document record with the
// presigned PUT the browser uses to upload the PDF directly to storage.
// MaxSentences is the extraction cap, surfaced so the extract-first client can
// abort before the PUT when a document is too long instead of discovering the
// cap on the extraction POST.
type DocumentUploadTicket struct {
	Document     domain.Document
	Upload       domain.PresignedRequest
	MaxSentences int
}

// DocumentExtraction is the browser-extracted text of one PDF: the page total
// and the normalized sentences in document order.
type DocumentExtraction struct {
	PageCount int
	Sentences []domain.DocumentSentence
}

// ReadableDocument pairs a document record with a presigned GET for direct
// PDF delivery from storage.
type ReadableDocument struct {
	Document domain.Document
	PDF      domain.PresignedRequest
}

// DocumentSentenceClaims is one analyzed sentence joined with the claims it
// produced; a skipped or unanalyzed sentence carries none.
type DocumentSentenceClaims struct {
	Sentence domain.DocumentSentence
	Claims   []domain.DocumentClaim
}

// DocumentAnalysis is the claims endpoint's view: the document (with its
// analysis state and progress) and every sentence in document order with its
// claims. It is the frontend's polling target.
type DocumentAnalysis struct {
	Document  domain.Document
	Sentences []DocumentSentenceClaims
}

// DocumentConfig configures a DocumentService. MaxSizeBytes bounds a declared
// upload size; MaxSentences bounds an extraction, both to bound LLM cost and
// abuse.
type DocumentConfig struct {
	MaxSizeBytes int64
	MaxSentences int
}

// DocumentService owns the document-record lifecycle: it mints presigned
// uploads, validates and stores browser extractions, and lists, resolves, and
// deletes records. It holds no HTTP types. newDocumentID is a field rather than
// a direct call so tests can inject a deterministic id; it is unexported and
// set only by the constructor, so no caller can bypass validation.
type DocumentService struct {
	store         domain.DocumentStore
	media         domain.MediaStore
	maxSizeBytes  int64
	maxSentences  int
	newDocumentID func() string
}

// NewDocumentService builds a DocumentService. It fails fast on a nil
// dependency or a non-positive bound.
func NewDocumentService(store domain.DocumentStore, media domain.MediaStore, cfg DocumentConfig) (*DocumentService, error) {
	if store == nil {
		return nil, errors.New("document: store is required")
	}
	if media == nil {
		return nil, errors.New("document: media store is required")
	}
	if cfg.MaxSizeBytes <= 0 {
		return nil, fmt.Errorf("document: max size bytes must be positive, got %d", cfg.MaxSizeBytes)
	}
	if cfg.MaxSentences <= 0 {
		return nil, fmt.Errorf("document: max sentences must be positive, got %d", cfg.MaxSentences)
	}
	return &DocumentService{
		store:         store,
		media:         media,
		maxSizeBytes:  cfg.MaxSizeBytes,
		maxSentences:  cfg.MaxSentences,
		newDocumentID: uuid.NewString,
	}, nil
}

// RequestUpload validates the request, records a pending document, and returns
// it with a presigned PUT the browser uses to upload the PDF directly to
// storage. The document id is minted here because the object key embeds it.
// The presign binds the declared content type and exact size into the
// signature and makes the key write-once, so the stored metadata cannot
// diverge from what actually lands in storage and a completed upload cannot be
// overwritten. Presigning runs before the insert: it has no side effects, so a
// storage failure leaves no phantom pending record behind.
func (s *DocumentService) RequestUpload(ctx context.Context, req DocumentUploadRequest) (DocumentUploadTicket, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return DocumentUploadTicket{}, ErrDocumentInvalidTitle
	}
	if req.ContentType != documentContentType {
		return DocumentUploadTicket{}, ErrDocumentInvalidContentType
	}
	if req.SizeBytes <= 0 || req.SizeBytes > s.maxSizeBytes {
		return DocumentUploadTicket{}, ErrDocumentInvalidSize
	}

	id := s.newDocumentID()
	key := domain.DocumentObjectKey(id)
	presigned, err := s.media.PresignUploadOnce(ctx, key, documentContentType, req.SizeBytes)
	if err != nil {
		return DocumentUploadTicket{}, fmt.Errorf("document: presign upload: %w", err)
	}
	doc, err := s.store.CreateDocument(ctx, domain.Document{
		ID:          id,
		Title:       title,
		ObjectKey:   key,
		ContentType: documentContentType,
		SizeBytes:   req.SizeBytes,
		Status:      domain.DocumentStatusPending,
	})
	if err != nil {
		return DocumentUploadTicket{}, fmt.Errorf("document: request upload: %w", err)
	}
	return DocumentUploadTicket{Document: doc, Upload: presigned, MaxSentences: s.maxSentences}, nil
}

// IngestExtraction validates the browser extraction and stores it atomically,
// flipping the pending document to ready. Analysis is not started here: the
// document analyzer owns that wiring, so a fresh extraction leaves
// analysis_status untouched at none. The shape checks run before the storage
// existence probe so malformed input never costs a network round trip.
func (s *DocumentService) IngestExtraction(ctx context.Context, id string, ext DocumentExtraction) (domain.Document, error) {
	if err := validateExtraction(ext, s.maxSentences); err != nil {
		return domain.Document{}, err
	}

	doc, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return domain.Document{}, err
	}
	if doc.Status != domain.DocumentStatusPending {
		// A retry of the extraction that already succeeded (the response was
		// lost) is idempotent, mirroring VideoService.Confirm: the ready
		// document is returned unchanged when the posted extraction is
		// identical to what is stored. The match is on the full sentence
		// content, not just the counts, so a genuinely different re-extraction
		// is a real conflict rather than being silently discarded.
		if doc.Status == domain.DocumentStatusReady {
			matches, err := s.extractionMatchesStored(ctx, doc, ext)
			if err != nil {
				return domain.Document{}, err
			}
			if matches {
				return doc, nil
			}
		}
		return domain.Document{}, domain.ErrDocumentNotPending
	}
	exists, err := s.media.Exists(ctx, doc.ObjectKey)
	if err != nil {
		return domain.Document{}, fmt.Errorf("document: ingest extraction %s: %w", id, err)
	}
	if !exists {
		return domain.Document{}, ErrDocumentObjectMissing
	}

	sentences := make([]domain.DocumentSentence, len(ext.Sentences))
	for i, sentence := range ext.Sentences {
		sentence.DocumentID = doc.ID
		sentences[i] = sentence
	}
	updated, err := s.store.StoreDocumentExtraction(ctx, doc.ID, ext.PageCount, sentences)
	if err != nil {
		if errors.Is(err, domain.ErrDocumentNotFound) || errors.Is(err, domain.ErrDocumentNotPending) {
			return domain.Document{}, err
		}
		return domain.Document{}, fmt.Errorf("document: ingest extraction %s: %w", id, err)
	}
	return updated, nil
}

// extractionMatchesStored reports whether the posted extraction is identical
// to what a prior successful ingest stored, so a retry after a lost response is
// a no-op rather than a conflict. It compares the page count and every
// sentence field, not just the counts, so a genuinely different extraction
// that happens to share the page and sentence totals is still a conflict.
func (s *DocumentService) extractionMatchesStored(ctx context.Context, doc domain.Document, ext DocumentExtraction) (bool, error) {
	if doc.PageCount != ext.PageCount || doc.SentencesTotal != len(ext.Sentences) {
		return false, nil
	}
	stored, err := s.store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		return false, fmt.Errorf("document: compare extraction %s: %w", doc.ID, err)
	}
	if len(stored) != len(ext.Sentences) {
		return false, nil
	}
	for i, want := range ext.Sentences {
		got := stored[i]
		if got.Seq != want.Seq || got.Page != want.Page || got.Text != want.Text || got.Occurrence != want.Occurrence {
			return false, nil
		}
	}
	return true, nil
}

// validateExtraction enforces the extraction shape: a page count in
// [1, maxDocumentPageCount], a non-empty sentence list under the cap,
// sequences dense from 0, pages within the document, non-blank text under the
// per-sentence byte cap, and occurrences that count identical text per page
// exactly (the nth duplicate carries occurrence n). The upper bounds keep
// every value far inside the int32 range the store narrows to; the occurrence
// rule is the anchoring contract, so an incoherent numbering is rejected here
// rather than corrupting highlights at view time.
func validateExtraction(ext DocumentExtraction, maxSentences int) error {
	if len(ext.Sentences) == 0 {
		return ErrDocumentExtractionEmpty
	}
	if len(ext.Sentences) > maxSentences {
		return fmt.Errorf("%w (%d sentences, cap %d)", ErrDocumentTooManySentences, len(ext.Sentences), maxSentences)
	}
	if ext.PageCount < 1 || ext.PageCount > maxDocumentPageCount {
		return fmt.Errorf("%w: page count %d outside 1..%d", ErrDocumentInvalidExtraction, ext.PageCount, maxDocumentPageCount)
	}
	occurrences := make(map[int]map[string]int)
	for i, sentence := range ext.Sentences {
		if sentence.Seq != i {
			return fmt.Errorf("%w: sentence %d has seq %d, want dense from 0", ErrDocumentInvalidExtraction, i, sentence.Seq)
		}
		if sentence.Page < 1 || sentence.Page > ext.PageCount {
			return fmt.Errorf("%w: sentence %d page %d outside 1..%d", ErrDocumentInvalidExtraction, i, sentence.Page, ext.PageCount)
		}
		if strings.TrimSpace(sentence.Text) == "" {
			return fmt.Errorf("%w: sentence %d text is blank", ErrDocumentInvalidExtraction, i)
		}
		if len(sentence.Text) > maxDocumentSentenceBytes {
			return fmt.Errorf("%w: sentence %d is %d bytes, cap %d", ErrDocumentInvalidExtraction, i, len(sentence.Text), maxDocumentSentenceBytes)
		}
		if occurrences[sentence.Page] == nil {
			occurrences[sentence.Page] = make(map[string]int)
		}
		want := occurrences[sentence.Page][sentence.Text] + 1
		if sentence.Occurrence != want {
			return fmt.Errorf("%w: sentence %d occurrence %d, want %d", ErrDocumentInvalidExtraction, i, sentence.Occurrence, want)
		}
		occurrences[sentence.Page][sentence.Text] = want
	}
	return nil
}

// List returns every document record, newest first, with verdict summary
// counts.
func (s *DocumentService) List(ctx context.Context) ([]domain.DocumentListItem, error) {
	items, err := s.store.ListDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("document: list: %w", err)
	}
	return items, nil
}

// Get returns the record with the given id plus, once the document is ready, a
// presigned GET the browser uses to fetch the PDF directly from storage. A
// pending or failed document carries no download: its object may not exist, so
// handing out a URL would send the viewer to a storage 404.
func (s *DocumentService) Get(ctx context.Context, id string) (ReadableDocument, error) {
	doc, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return ReadableDocument{}, err
	}
	if doc.Status != domain.DocumentStatusReady {
		return ReadableDocument{Document: doc}, nil
	}
	presigned, err := s.media.PresignDownload(ctx, doc.ObjectKey)
	if err != nil {
		return ReadableDocument{}, fmt.Errorf("document: get %s: %w", id, err)
	}
	return ReadableDocument{Document: doc, PDF: presigned}, nil
}

// Claims returns the document with every sentence in document order, each
// joined with the claims the latest analysis produced. Before any analysis the
// claim lists are simply empty, so the endpoint works from upload onward.
func (s *DocumentService) Claims(ctx context.Context, id string) (DocumentAnalysis, error) {
	doc, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return DocumentAnalysis{}, err
	}
	sentences, err := s.store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		return DocumentAnalysis{}, fmt.Errorf("document: claims %s: %w", id, err)
	}
	claims, err := s.store.ListDocumentClaims(ctx, doc.ID)
	if err != nil {
		return DocumentAnalysis{}, fmt.Errorf("document: claims %s: %w", id, err)
	}

	bySeq := make(map[int][]domain.DocumentClaim, len(claims))
	for _, claim := range claims {
		bySeq[claim.SentenceSeq] = append(bySeq[claim.SentenceSeq], claim)
	}
	joined := make([]DocumentSentenceClaims, 0, len(sentences))
	for _, sentence := range sentences {
		claimList := bySeq[sentence.Seq]
		if claimList == nil {
			claimList = []domain.DocumentClaim{}
		}
		joined = append(joined, DocumentSentenceClaims{Sentence: sentence, Claims: claimList})
	}
	return DocumentAnalysis{Document: doc, Sentences: joined}, nil
}

// Delete removes the document's storage object, then its rows (sentences and
// claims cascade). The object goes first: if the row deletion fails the record
// stays visible and the operator retries, whereas rows-first would strand an
// invisible object in storage. For a pending document the object is swept once
// more after the rows, best-effort: its write-once presigned PUT is still live
// for its TTL, so an in-flight upload finishing between the two passes would
// otherwise land an object no record references. A ready document's upload
// already completed and its write-once URL is dead (a retry gets 412), so no
// second sweep is needed. A PUT completing after the sweep can still orphan the
// object; that residual window is accepted (its cost is one bounded PDF) rather
// than blocking deletion for the ticket's lifetime.
func (s *DocumentService) Delete(ctx context.Context, id string) error {
	doc, err := s.store.GetDocument(ctx, id)
	if err != nil {
		return err
	}
	if err := s.media.Delete(ctx, doc.ObjectKey); err != nil {
		return fmt.Errorf("document: delete %s: %w", id, err)
	}
	if err := s.store.DeleteDocument(ctx, doc.ID); err != nil {
		return fmt.Errorf("document: delete %s: %w", id, err)
	}
	if doc.Status == domain.DocumentStatusPending {
		_ = s.media.Delete(ctx, doc.ObjectKey)
	}
	return nil
}

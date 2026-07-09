package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeDocumentStore is an in-memory domain.DocumentStore for service tests.
// Each *Err field, when set, makes the matching method fail.
type fakeDocumentStore struct {
	documents map[string]domain.Document
	sentences map[string][]domain.DocumentSentence
	claims    map[string][]domain.DocumentClaim

	createErr  error
	getErr     error
	listErr    error
	extractErr error
	deleteErr  error

	extractCalls int
	deletedIDs   []string
}

func newFakeDocumentStore() *fakeDocumentStore {
	return &fakeDocumentStore{
		documents: map[string]domain.Document{},
		sentences: map[string][]domain.DocumentSentence{},
		claims:    map[string][]domain.DocumentClaim{},
	}
}

func (f *fakeDocumentStore) CreateDocument(_ context.Context, d domain.Document) (domain.Document, error) {
	if f.createErr != nil {
		return domain.Document{}, f.createErr
	}
	d.AnalysisStatus = domain.DocumentAnalysisNone
	f.documents[d.ID] = d
	return d, nil
}

func (f *fakeDocumentStore) GetDocument(_ context.Context, id string) (domain.Document, error) {
	if f.getErr != nil {
		return domain.Document{}, f.getErr
	}
	d, ok := f.documents[id]
	if !ok {
		return domain.Document{}, domain.ErrDocumentNotFound
	}
	return d, nil
}

func (f *fakeDocumentStore) ListDocuments(_ context.Context) ([]domain.DocumentListItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	items := make([]domain.DocumentListItem, 0, len(f.documents))
	for _, d := range f.documents {
		items = append(items, domain.DocumentListItem{Document: d})
	}
	return items, nil
}

func (f *fakeDocumentStore) StoreDocumentExtraction(_ context.Context, id string, pageCount int, sentences []domain.DocumentSentence) (domain.Document, error) {
	f.extractCalls++
	if f.extractErr != nil {
		return domain.Document{}, f.extractErr
	}
	d, ok := f.documents[id]
	if !ok {
		return domain.Document{}, domain.ErrDocumentNotFound
	}
	if d.Status != domain.DocumentStatusPending {
		return domain.Document{}, domain.ErrDocumentNotPending
	}
	d.Status = domain.DocumentStatusReady
	d.PageCount = pageCount
	d.SentencesTotal = len(sentences)
	f.documents[id] = d
	f.sentences[id] = sentences
	return d, nil
}

func (f *fakeDocumentStore) ListDocumentSentences(_ context.Context, id string) ([]domain.DocumentSentence, error) {
	return f.sentences[id], nil
}

func (f *fakeDocumentStore) ListDocumentClaims(_ context.Context, id string) ([]domain.DocumentClaim, error) {
	return f.claims[id], nil
}

func (f *fakeDocumentStore) DeleteDocument(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.documents[id]; !ok {
		return domain.ErrDocumentNotFound
	}
	delete(f.documents, id)
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}

var _ domain.DocumentStore = (*fakeDocumentStore)(nil)

const (
	testMaxDocumentBytes     = 1 << 20
	testMaxDocumentSentences = 5
	testDocumentID           = "123e4567-e89b-12d3-a456-426614174000"
)

// newTestDocumentService builds a DocumentService over the fakes with a
// deterministic document id so object-key assertions are stable. It wires no
// analyzer (analysis disabled).
func newTestDocumentService(t *testing.T, store domain.DocumentStore, media domain.MediaStore) *DocumentService {
	t.Helper()
	return newTestDocumentServiceWithAnalyzer(t, store, media, nil)
}

// newTestDocumentServiceWithAnalyzer is newTestDocumentService with an injected
// analyzer, for the auto-start cases.
func newTestDocumentServiceWithAnalyzer(t *testing.T, store domain.DocumentStore, media domain.MediaStore, analyzer AnalysisStarter) *DocumentService {
	t.Helper()
	svc, err := NewDocumentService(store, media, analyzer, DocumentConfig{
		MaxSizeBytes: testMaxDocumentBytes,
		MaxSentences: testMaxDocumentSentences,
	}, discardLogger())
	if err != nil {
		t.Fatalf("NewDocumentService: %v", err)
	}
	svc.newDocumentID = func() string { return testDocumentID }
	return svc
}

// fakeAnalysisStarter records Start calls; enabled toggles Enabled().
type fakeAnalysisStarter struct {
	enabled  bool
	startErr error
	started  []string
}

func (f *fakeAnalysisStarter) Enabled() bool { return f.enabled }

func (f *fakeAnalysisStarter) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return f.startErr
}

func validSentences(n int) []domain.DocumentSentence {
	out := make([]domain.DocumentSentence, n)
	for i := range out {
		// Identical text on one page must carry sequential occurrences.
		out[i] = domain.DocumentSentence{Seq: i, Page: 1, Text: "Une phrase vérifiable.", Occurrence: i + 1}
	}
	return out
}

func TestNewDocumentServiceValidation(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{}
	if _, err := NewDocumentService(nil, media, nil, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 1}, discardLogger()); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := NewDocumentService(store, nil, nil, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 1}, discardLogger()); err == nil {
		t.Error("nil media accepted")
	}
	if _, err := NewDocumentService(store, media, nil, DocumentConfig{MaxSizeBytes: 0, MaxSentences: 1}, discardLogger()); err == nil {
		t.Error("non-positive size cap accepted")
	}
	if _, err := NewDocumentService(store, media, nil, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 0}, discardLogger()); err == nil {
		t.Error("non-positive sentence cap accepted")
	}
}

func TestDocumentRequestUpload(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{}
	svc := newTestDocumentService(t, store, media)

	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{
		Title:       "  Rapport annuel  ",
		ContentType: "application/pdf",
		SizeBytes:   2048,
	})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	wantKey := "documents/" + testDocumentID + "/original.pdf"
	if ticket.Document.ID != testDocumentID || ticket.Document.ObjectKey != wantKey {
		t.Errorf("identity = %q key %q, want minted id and per-document key", ticket.Document.ID, ticket.Document.ObjectKey)
	}
	if ticket.Document.Title != "Rapport annuel" {
		t.Errorf("title = %q, want trimmed", ticket.Document.Title)
	}
	if ticket.Document.Status != domain.DocumentStatusPending {
		t.Errorf("status = %q, want pending", ticket.Document.Status)
	}
	if ticket.Upload.Method != "PUT" || media.uploadOnceKey != wantKey {
		t.Errorf("presigned PUT not minted for the object key: %+v (signed %q)", ticket.Upload, media.uploadOnceKey)
	}
	if media.uploadOnceType != "application/pdf" || media.uploadOnceSize != 2048 {
		t.Errorf("presign constraints = %q/%d, want the declared type and size signed in", media.uploadOnceType, media.uploadOnceSize)
	}
	if ticket.MaxSentences != testMaxDocumentSentences {
		t.Errorf("ticket max sentences = %d, want %d so the client can fail fast before the PUT", ticket.MaxSentences, testMaxDocumentSentences)
	}
}

// TestDocumentRequestUploadPresignFailureLeavesNoRow proves the presign runs
// before the insert: a storage misconfiguration yields an error and no phantom
// pending record that could never receive an upload.
func TestDocumentRequestUploadPresignFailureLeavesNoRow(t *testing.T) {
	t.Parallel()
	store := newFakeDocumentStore()
	media := &fakeMediaStore{presignUploadOnceErr: errors.New("storage down")}
	svc := newTestDocumentService(t, store, media)

	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(store.documents) != 0 {
		t.Error("presign failure left a pending record behind")
	}
}

func TestDocumentRequestUploadRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		req     DocumentUploadRequest
		wantErr error
	}{
		{name: "empty title", req: DocumentUploadRequest{Title: "   ", ContentType: "application/pdf", SizeBytes: 1}, wantErr: ErrDocumentInvalidTitle},
		{name: "non-pdf type", req: DocumentUploadRequest{Title: "t", ContentType: "video/mp4", SizeBytes: 1}, wantErr: ErrDocumentInvalidContentType},
		{name: "zero size", req: DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 0}, wantErr: ErrDocumentInvalidSize},
		{name: "oversized", req: DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: testMaxDocumentBytes + 1}, wantErr: ErrDocumentInvalidSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeDocumentStore()
			svc := newTestDocumentService(t, store, &fakeMediaStore{})
			if _, err := svc.RequestUpload(t.Context(), tc.req); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(store.documents) != 0 {
				t.Error("rejected upload left a record behind")
			}
		})
	}
}

func TestDocumentIngestExtraction(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	doc, err := svc.IngestExtraction(t.Context(), ticket.Document.ID, DocumentExtraction{
		PageCount: 2,
		Sentences: []domain.DocumentSentence{
			{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
			{Seq: 1, Page: 2, Text: "Deux.", Occurrence: 1},
		},
	})
	if err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}
	if doc.Status != domain.DocumentStatusReady || doc.PageCount != 2 || doc.SentencesTotal != 2 {
		t.Errorf("document = %+v, want ready with extraction metadata", doc)
	}
	if media.existsKey != ticket.Document.ObjectKey {
		t.Errorf("existence checked against %q, want the document object key", media.existsKey)
	}
	stored := store.sentences[ticket.Document.ID]
	if len(stored) != 2 || stored[0].DocumentID != ticket.Document.ID {
		t.Errorf("stored sentences = %+v, want both carrying the document id", stored)
	}
}

func TestDocumentIngestExtractionRejects(t *testing.T) {
	t.Parallel()
	valid := DocumentExtraction{PageCount: 1, Sentences: validSentences(2)}

	tests := []struct {
		name    string
		setup   func(store *fakeDocumentStore, media *fakeMediaStore)
		id      string
		ext     DocumentExtraction
		wantErr error
	}{
		{name: "unknown document", id: "other", ext: valid, wantErr: domain.ErrDocumentNotFound},
		{
			name: "not pending",
			setup: func(store *fakeDocumentStore, _ *fakeMediaStore) {
				d := store.documents[testDocumentID]
				d.Status = domain.DocumentStatusReady
				store.documents[testDocumentID] = d
			},
			id: testDocumentID, ext: valid, wantErr: domain.ErrDocumentNotPending,
		},
		{
			name: "object not uploaded",
			setup: func(_ *fakeDocumentStore, media *fakeMediaStore) {
				media.exists = false
			},
			id: testDocumentID, ext: valid, wantErr: ErrDocumentObjectMissing,
		},
		{name: "empty extraction", id: testDocumentID, ext: DocumentExtraction{PageCount: 1}, wantErr: ErrDocumentExtractionEmpty},
		{
			name:    "sentence cap exceeded",
			id:      testDocumentID,
			ext:     DocumentExtraction{PageCount: 1, Sentences: validSentences(testMaxDocumentSentences + 1)},
			wantErr: ErrDocumentTooManySentences,
		},
		{name: "page count below one", id: testDocumentID, ext: DocumentExtraction{PageCount: 0, Sentences: validSentences(1)}, wantErr: ErrDocumentInvalidExtraction},
		{
			name:    "page count above the bound",
			id:      testDocumentID,
			ext:     DocumentExtraction{PageCount: maxDocumentPageCount + 1, Sentences: validSentences(1)},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "non-dense seq",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 1, Page: 1, Text: "x", Occurrence: 1},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "page outside document",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 2, Text: "x", Occurrence: 1},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "blank sentence text",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: "   ", Occurrence: 1},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "sentence text above the byte cap",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: strings.Repeat("a", maxDocumentSentenceBytes+1), Occurrence: 1},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "occurrence below one",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: "x", Occurrence: 0},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "duplicate text reusing occurrence 1",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: "Le budget augmente.", Occurrence: 1},
				{Seq: 1, Page: 1, Text: "Le budget augmente.", Occurrence: 1},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
		{
			name: "occurrence jumping ahead",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: "x", Occurrence: 2},
			}},
			wantErr: ErrDocumentInvalidExtraction,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
			svc := newTestDocumentService(t, store, media)
			if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
				t.Fatalf("RequestUpload: %v", err)
			}
			if tc.setup != nil {
				tc.setup(store, media)
			}
			if _, err := svc.IngestExtraction(t.Context(), tc.id, tc.ext); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(store.sentences) != 0 {
				t.Error("rejected extraction stored sentences")
			}
		})
	}
}

func TestDocumentIngestExtractionAutoStartsAnalysis(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	analyzer := &fakeAnalysisStarter{enabled: true}
	svc := newTestDocumentServiceWithAnalyzer(t, store, media, analyzer)
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	ext := DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}}}
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, ext); err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}
	if len(analyzer.started) != 1 || analyzer.started[0] != testDocumentID {
		t.Errorf("auto-start = %v, want the document analyzed once", analyzer.started)
	}

	// A retried (idempotent) extraction must not re-trigger analysis.
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, ext); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if len(analyzer.started) != 1 {
		t.Errorf("idempotent retry re-triggered analysis: %v", analyzer.started)
	}
}

func TestDocumentIngestExtractionDisabledAnalyzerDoesNotStart(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	analyzer := &fakeAnalysisStarter{enabled: false}
	svc := newTestDocumentServiceWithAnalyzer(t, store, media, analyzer)
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, DocumentExtraction{
		PageCount: 1, Sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}},
	}); err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}
	if len(analyzer.started) != 0 {
		t.Errorf("disabled analyzer was started: %v", analyzer.started)
	}
}

// TestDocumentIngestExtractionAutoStartFailureIsSwallowed proves a failed
// auto-start does not fail the extraction: the document is ready regardless.
func TestDocumentIngestExtractionAutoStartFailureIsSwallowed(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	analyzer := &fakeAnalysisStarter{enabled: true, startErr: errors.New("analyzer down")}
	svc := newTestDocumentServiceWithAnalyzer(t, store, media, analyzer)
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	doc, err := svc.IngestExtraction(t.Context(), testDocumentID, DocumentExtraction{
		PageCount: 1, Sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}},
	})
	if err != nil {
		t.Fatalf("IngestExtraction failed on a best-effort auto-start error: %v", err)
	}
	if doc.Status != domain.DocumentStatusReady {
		t.Errorf("status = %q, want ready despite the auto-start failure", doc.Status)
	}
}

// TestDocumentIngestExtractionIdempotentRetry proves a retried POST of the
// same extraction after a lost response returns the ready document instead of
// a conflict, mirroring VideoService.Confirm; an extraction with a different
// shape still conflicts.
func TestDocumentIngestExtractionIdempotentRetry(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	ext := DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Deux.", Occurrence: 1},
	}}
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, ext); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	storeCalls := store.extractCalls

	doc, err := svc.IngestExtraction(t.Context(), testDocumentID, ext)
	if err != nil {
		t.Fatalf("retried ingest = %v, want the ready document", err)
	}
	if doc.Status != domain.DocumentStatusReady {
		t.Errorf("retried ingest status = %q, want ready", doc.Status)
	}
	if store.extractCalls != storeCalls {
		t.Errorf("retry re-stored the extraction (%d calls, want %d)", store.extractCalls, storeCalls)
	}

	// A different sentence count is a conflict.
	fewer := DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Autre.", Occurrence: 1},
	}}
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, fewer); !errors.Is(err, domain.ErrDocumentNotPending) {
		t.Errorf("fewer-sentence extraction err = %v, want ErrDocumentNotPending", err)
	}

	// Same page count AND same sentence count but different text is still a
	// conflict: the retry is idempotent only when the content actually matches,
	// so a re-extraction is never silently discarded.
	sameShape := DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Trois.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Quatre.", Occurrence: 1},
	}}
	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, sameShape); !errors.Is(err, domain.ErrDocumentNotPending) {
		t.Errorf("same-shape different-content extraction err = %v, want ErrDocumentNotPending", err)
	}
}

// TestDocumentIngestExtractionValidatesBeforeStorage proves the shape checks
// run before the storage existence probe: malformed input never costs a network
// round trip.
func TestDocumentIngestExtractionValidatesBeforeStorage(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	if _, err := svc.IngestExtraction(t.Context(), testDocumentID, DocumentExtraction{PageCount: 1}); !errors.Is(err, ErrDocumentExtractionEmpty) {
		t.Fatalf("err = %v, want ErrDocumentExtractionEmpty", err)
	}
	if media.existsCalls != 0 {
		t.Errorf("existence probed %d times for malformed input, want 0", media.existsCalls)
	}
}

func TestDocumentList(t *testing.T) {
	t.Parallel()
	store := newFakeDocumentStore()
	svc := newTestDocumentService(t, store, &fakeMediaStore{})
	if _, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1}); err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	items, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != testDocumentID {
		t.Errorf("items = %+v, want the created document", items)
	}

	store.listErr = errors.New("boom")
	if _, err := svc.List(t.Context()); err == nil {
		t.Error("store failure swallowed")
	}
}

func TestDocumentGet(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	// Pending: the object may not exist yet, so no download is presigned.
	pending, err := svc.Get(t.Context(), ticket.Document.ID)
	if err != nil {
		t.Fatalf("Get (pending): %v", err)
	}
	if pending.Document.ID != ticket.Document.ID {
		t.Errorf("document = %+v, want the record", pending.Document)
	}
	if pending.PDF.URL != "" || media.downloadKey != "" {
		t.Errorf("pending pdf = %+v (signed %q), want no presigned URL before ready", pending.PDF, media.downloadKey)
	}

	if _, err := svc.IngestExtraction(t.Context(), ticket.Document.ID, DocumentExtraction{
		PageCount: 1,
		Sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}},
	}); err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}

	got, err := svc.Get(t.Context(), ticket.Document.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PDF.Method != "GET" || media.downloadKey != ticket.Document.ObjectKey {
		t.Errorf("pdf = %+v (signed %q), want presigned GET for the object key", got.PDF, media.downloadKey)
	}

	if _, err := svc.Get(t.Context(), "missing"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("absent id err = %v, want ErrDocumentNotFound", err)
	}
}

func TestDocumentClaims(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	if _, err := svc.IngestExtraction(t.Context(), ticket.Document.ID, DocumentExtraction{
		PageCount: 1,
		Sentences: []domain.DocumentSentence{
			{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
			{Seq: 1, Page: 1, Text: "Deux.", Occurrence: 1},
		},
	}); err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}
	store.claims[ticket.Document.ID] = []domain.DocumentClaim{
		{ID: "dc-1", SentenceSeq: 1, ClaimID: "c-1", Status: domain.DocumentClaimVerified, Verdict: "credible"},
	}

	analysis, err := svc.Claims(t.Context(), ticket.Document.ID)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if analysis.Document.ID != ticket.Document.ID {
		t.Errorf("document = %+v, want the record", analysis.Document)
	}
	if len(analysis.Sentences) != 2 {
		t.Fatalf("sentences = %d, want 2", len(analysis.Sentences))
	}
	if len(analysis.Sentences[0].Claims) != 0 {
		t.Errorf("sentence 0 claims = %+v, want none", analysis.Sentences[0].Claims)
	}
	got := analysis.Sentences[1].Claims
	if len(got) != 1 || got[0].ClaimID != "c-1" {
		t.Errorf("sentence 1 claims = %+v, want the joined claim", got)
	}

	if _, err := svc.Claims(t.Context(), "missing"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("absent id err = %v, want ErrDocumentNotFound", err)
	}
}

func TestDocumentDelete(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	// A pending document's write-once PUT may still be in flight, so the object
	// is deleted before the rows and swept once more after them: a PUT landing
	// between the two passes does not orphan an object.
	if err := svc.Delete(t.Context(), ticket.Document.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(media.deletedKeys) != 2 || media.deletedKeys[0] != ticket.Document.ObjectKey || media.deletedKeys[1] != ticket.Document.ObjectKey {
		t.Errorf("pending deleted keys = %v, want the object key deleted before and swept after the rows", media.deletedKeys)
	}
	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != ticket.Document.ID {
		t.Errorf("deleted ids = %v, want the record", store.deletedIDs)
	}

	if err := svc.Delete(t.Context(), "missing"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("absent id err = %v, want ErrDocumentNotFound", err)
	}
}

// TestDocumentDeleteReadyDocumentSweepsOnce proves a ready document is deleted
// with a single storage call: its write-once upload already completed, so no
// in-flight PUT can re-create the object and the extra sweep would be waste.
func TestDocumentDeleteReadyDocumentSweepsOnce(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{exists: true}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	if _, err := svc.IngestExtraction(t.Context(), ticket.Document.ID, DocumentExtraction{
		PageCount: 1,
		Sentences: []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}},
	}); err != nil {
		t.Fatalf("IngestExtraction: %v", err)
	}

	if err := svc.Delete(t.Context(), ticket.Document.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(media.deletedKeys) != 1 || media.deletedKeys[0] != ticket.Document.ObjectKey {
		t.Errorf("ready deleted keys = %v, want a single delete", media.deletedKeys)
	}
}

// TestDocumentDeleteObjectFirst proves the rows survive a failed object
// deletion: the record stays visible and the operator can retry, rather than
// being left with untracked storage garbage.
func TestDocumentDeleteObjectFirst(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{deleteErr: errors.New("storage down")}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	err = svc.Delete(t.Context(), ticket.Document.ID)
	if err == nil || !strings.Contains(err.Error(), "storage down") {
		t.Fatalf("err = %v, want the wrapped storage failure", err)
	}
	if len(store.deletedIDs) != 0 {
		t.Error("rows deleted despite the object deletion failing")
	}
	if _, getErr := svc.Get(t.Context(), ticket.Document.ID); getErr != nil {
		t.Errorf("record gone after failed delete: %v", getErr)
	}
}

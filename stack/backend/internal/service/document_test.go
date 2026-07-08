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
// deterministic document id so object-key assertions are stable.
func newTestDocumentService(t *testing.T, store domain.DocumentStore, media domain.MediaStore) *DocumentService {
	t.Helper()
	svc, err := NewDocumentService(store, media, DocumentConfig{
		MaxSizeBytes: testMaxDocumentBytes,
		MaxSentences: testMaxDocumentSentences,
	})
	if err != nil {
		t.Fatalf("NewDocumentService: %v", err)
	}
	svc.newDocumentID = func() string { return testDocumentID }
	return svc
}

func validSentences(n int) []domain.DocumentSentence {
	out := make([]domain.DocumentSentence, n)
	for i := range out {
		out[i] = domain.DocumentSentence{Seq: i, Page: 1, Text: "Une phrase vérifiable.", Occurrence: 1}
	}
	return out
}

func TestNewDocumentServiceValidation(t *testing.T) {
	t.Parallel()
	store, media := newFakeDocumentStore(), &fakeMediaStore{}
	if _, err := NewDocumentService(nil, media, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 1}); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := NewDocumentService(store, nil, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 1}); err == nil {
		t.Error("nil media accepted")
	}
	if _, err := NewDocumentService(store, media, DocumentConfig{MaxSizeBytes: 0, MaxSentences: 1}); err == nil {
		t.Error("non-positive size cap accepted")
	}
	if _, err := NewDocumentService(store, media, DocumentConfig{MaxSizeBytes: 1, MaxSentences: 0}); err == nil {
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
	if ticket.Upload.Method != "PUT" || media.uploadKey != wantKey {
		t.Errorf("presigned PUT not minted for the object key: %+v (signed %q)", ticket.Upload, media.uploadKey)
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
			name: "occurrence below one",
			id:   testDocumentID,
			ext: DocumentExtraction{PageCount: 1, Sentences: []domain.DocumentSentence{
				{Seq: 0, Page: 1, Text: "x", Occurrence: 0},
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
	store, media := newFakeDocumentStore(), &fakeMediaStore{}
	svc := newTestDocumentService(t, store, media)
	ticket, err := svc.RequestUpload(t.Context(), DocumentUploadRequest{Title: "t", ContentType: "application/pdf", SizeBytes: 1})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	got, err := svc.Get(t.Context(), ticket.Document.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Document.ID != ticket.Document.ID {
		t.Errorf("document = %+v, want the record", got.Document)
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

	if err := svc.Delete(t.Context(), ticket.Document.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(media.deletedKeys) != 1 || media.deletedKeys[0] != ticket.Document.ObjectKey {
		t.Errorf("deleted keys = %v, want the object key", media.deletedKeys)
	}
	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != ticket.Document.ID {
		t.Errorf("deleted ids = %v, want the record", store.deletedIDs)
	}

	if err := svc.Delete(t.Context(), "missing"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("absent id err = %v, want ErrDocumentNotFound", err)
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

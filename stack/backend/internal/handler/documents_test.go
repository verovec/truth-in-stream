package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeDocumentService is a handler.DocumentService stand-in. Each *Err field,
// when set, makes the matching method fail; the recorded inputs let tests
// assert the handler decoded and forwarded the request correctly.
type fakeDocumentService struct {
	ticket   service.DocumentUploadTicket
	ingested domain.Document
	list     []domain.DocumentListItem
	readable service.ReadableDocument
	analysis service.DocumentAnalysis

	requestErr error
	ingestErr  error
	listErr    error
	getErr     error
	claimsErr  error
	deleteErr  error

	lastUpload   service.DocumentUploadRequest
	lastIngestID string
	lastIngest   service.DocumentExtraction
	lastGetID    string
	lastClaimsID string
	lastDeleteID string
}

func (f *fakeDocumentService) RequestUpload(_ context.Context, req service.DocumentUploadRequest) (service.DocumentUploadTicket, error) {
	f.lastUpload = req
	if f.requestErr != nil {
		return service.DocumentUploadTicket{}, f.requestErr
	}
	return f.ticket, nil
}

func (f *fakeDocumentService) IngestExtraction(_ context.Context, id string, ext service.DocumentExtraction) (domain.Document, error) {
	f.lastIngestID = id
	f.lastIngest = ext
	if f.ingestErr != nil {
		return domain.Document{}, f.ingestErr
	}
	return f.ingested, nil
}

func (f *fakeDocumentService) List(_ context.Context) ([]domain.DocumentListItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func (f *fakeDocumentService) Get(_ context.Context, id string) (service.ReadableDocument, error) {
	f.lastGetID = id
	if f.getErr != nil {
		return service.ReadableDocument{}, f.getErr
	}
	return f.readable, nil
}

func (f *fakeDocumentService) Claims(_ context.Context, id string) (service.DocumentAnalysis, error) {
	f.lastClaimsID = id
	if f.claimsErr != nil {
		return service.DocumentAnalysis{}, f.claimsErr
	}
	return f.analysis, nil
}

func (f *fakeDocumentService) Delete(_ context.Context, id string) error {
	f.lastDeleteID = id
	return f.deleteErr
}

var _ DocumentService = (*fakeDocumentService)(nil)

// fakeDocumentAnalyzer is a handler.DocumentAnalyzerService stand-in.
type fakeDocumentAnalyzer struct {
	startErr    error
	lastStartID string
}

func (f *fakeDocumentAnalyzer) Start(_ context.Context, id string) error {
	f.lastStartID = id
	return f.startErr
}

var _ DocumentAnalyzerService = (*fakeDocumentAnalyzer)(nil)

func TestReanalyseDocumentHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "accepted", err: nil, wantCode: http.StatusAccepted},
		{name: "unknown", err: domain.ErrDocumentNotFound, wantCode: http.StatusNotFound},
		{name: "not ready", err: domain.ErrDocumentNotReady, wantCode: http.StatusConflict},
		{name: "already analysing", err: domain.ErrAnalysisInProgress, wantCode: http.StatusConflict},
		{name: "disabled", err: service.ErrAnalysisDisabled, wantCode: http.StatusServiceUnavailable},
		{name: "internal", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			analyzer := &fakeDocumentAnalyzer{startErr: tc.err}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/d1/reanalyse", nil)
			req.SetPathValue("id", "d1")
			reanalyseDocumentHandler(analyzer)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if analyzer.lastStartID != "d1" {
				t.Errorf("start id = %q, want d1", analyzer.lastStartID)
			}
		})
	}
}

func TestRequestDocumentUploadHandlerSuccess(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{ticket: service.DocumentUploadTicket{
		Document: domain.Document{ID: "doc-1", ObjectKey: "documents/doc-1/original.pdf", Status: domain.DocumentStatusPending},
		Upload: domain.PresignedRequest{
			URL: "https://put/documents/doc-1/original.pdf", Method: "PUT",
			SignedHeaders: map[string][]string{"Host": {"storage"}},
		},
		MaxSentences: 1500,
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/uploads",
		strings.NewReader(`{"title":"Rapport","content_type":"application/pdf","size_bytes":2048}`))
	requestDocumentUploadHandler(svc)(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got documentUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DocumentID != "doc-1" || got.ObjectKey != "documents/doc-1/original.pdf" || got.Status != "pending" {
		t.Errorf("response = %+v, want the created record", got)
	}
	if got.Upload.URL != "https://put/documents/doc-1/original.pdf" || got.Upload.Method != "PUT" {
		t.Errorf("upload = %+v, want presigned PUT", got.Upload)
	}
	if got.MaxSentences != 1500 {
		t.Errorf("max_sentences = %d, want the cap surfaced to the client", got.MaxSentences)
	}
	if svc.lastUpload != (service.DocumentUploadRequest{Title: "Rapport", ContentType: "application/pdf", SizeBytes: 2048}) {
		t.Errorf("forwarded request = %+v, want decoded body", svc.lastUpload)
	}
}

func TestRequestDocumentUploadHandlerErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "empty title", err: service.ErrDocumentInvalidTitle, wantCode: http.StatusBadRequest},
		{name: "bad type", err: service.ErrDocumentInvalidContentType, wantCode: http.StatusUnsupportedMediaType},
		{name: "bad size", err: service.ErrDocumentInvalidSize, wantCode: http.StatusBadRequest},
		{name: "internal", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeDocumentService{requestErr: tc.err}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/uploads",
				strings.NewReader(`{"title":"x","content_type":"application/pdf","size_bytes":1}`))
			requestDocumentUploadHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestDocumentExtractionHandlerSuccess(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{ingested: domain.Document{
		ID: "doc-1", Status: domain.DocumentStatusReady, PageCount: 2, SentencesTotal: 2,
		AnalysisStatus: domain.DocumentAnalysisNone,
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/doc-1/extraction",
		strings.NewReader(`{"page_count":2,"sentences":[
			{"seq":0,"page":1,"text":"Une.","occurrence":1},
			{"seq":1,"page":2,"text":"Deux.","occurrence":1}]}`))
	req.SetPathValue("id", "doc-1")
	documentExtractionHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastIngestID != "doc-1" {
		t.Errorf("ingest id = %q, want doc-1", svc.lastIngestID)
	}
	if svc.lastIngest.PageCount != 2 || len(svc.lastIngest.Sentences) != 2 {
		t.Fatalf("forwarded extraction = %+v, want decoded body", svc.lastIngest)
	}
	s := svc.lastIngest.Sentences[1]
	if s.Seq != 1 || s.Page != 2 || s.Text != "Deux." || s.Occurrence != 1 {
		t.Errorf("sentence = %+v, want decoded fields", s)
	}
	var got documentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "doc-1" || got.Status != "ready" || got.PageCount != 2 {
		t.Errorf("body = %+v, want the ready record", got)
	}
}

func TestDocumentExtractionHandlerErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "unknown document", err: domain.ErrDocumentNotFound, wantCode: http.StatusNotFound},
		{name: "not pending", err: domain.ErrDocumentNotPending, wantCode: http.StatusConflict},
		{name: "object missing", err: service.ErrDocumentObjectMissing, wantCode: http.StatusConflict},
		{name: "empty extraction", err: service.ErrDocumentExtractionEmpty, wantCode: http.StatusBadRequest},
		{name: "over the cap", err: service.ErrDocumentTooManySentences, wantCode: http.StatusBadRequest},
		{name: "malformed", err: service.ErrDocumentInvalidExtraction, wantCode: http.StatusBadRequest},
		{name: "internal", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeDocumentService{ingestErr: tc.err}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/doc-1/extraction",
				strings.NewReader(`{"page_count":1,"sentences":[{"seq":0,"page":1,"text":"x","occurrence":1}]}`))
			req.SetPathValue("id", "doc-1")
			documentExtractionHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestDocumentExtractionHandlerRejectsBadJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/documents/doc-1/extraction", strings.NewReader("not json"))
	req.SetPathValue("id", "doc-1")
	documentExtractionHandler(&fakeDocumentService{})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed JSON", rec.Code)
	}
}

func TestListDocumentsHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{list: []domain.DocumentListItem{
		{
			Document: domain.Document{
				ID: "d1", Title: "Rapport", Status: domain.DocumentStatusReady,
				AnalysisStatus: domain.DocumentAnalysisComplete, PageCount: 3,
			},
			CredibleClaims: 2,
			DisputedClaims: 1,
		},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents", nil)
	listDocumentsHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got listDocumentsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Documents) != 1 {
		t.Fatalf("got %d documents, want 1", len(got.Documents))
	}
	d := got.Documents[0]
	if d.ID != "d1" || d.AnalysisStatus != "complete" || d.PageCount != 3 {
		t.Errorf("document = %+v, want the record", d)
	}
	if d.CredibleClaims != 2 || d.DisputedClaims != 1 {
		t.Errorf("counts = %d/%d, want 2/1", d.CredibleClaims, d.DisputedClaims)
	}
}

func TestListDocumentsHandlerEmptyIsArray(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents", nil)
	listDocumentsHandler(&fakeDocumentService{list: nil})(rec, req)
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, `"documents":[]`) {
		t.Errorf("empty list body = %s, want documents: []", body)
	}
}

func TestGetDocumentHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{readable: service.ReadableDocument{
		Document: domain.Document{ID: "d1", Title: "Rapport", Status: domain.DocumentStatusReady},
		PDF:      domain.PresignedRequest{URL: "https://get/documents/d1/original.pdf", Method: "GET"},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents/d1", nil)
	req.SetPathValue("id", "d1")
	getDocumentHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got documentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "d1" || got.Title != "Rapport" {
		t.Errorf("metadata = %+v, want the record", got.documentJSON)
	}
	if got.PDF.URL != "https://get/documents/d1/original.pdf" || got.PDF.Method != "GET" {
		t.Errorf("pdf = %+v, want presigned GET", got.PDF)
	}
}

// TestGetDocumentHandlerPendingOmitsPDF pins the wire contract for a document
// whose object may not exist yet: no pdf key at all, rather than a URL that
// would 404 against storage.
func TestGetDocumentHandlerPendingOmitsPDF(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{readable: service.ReadableDocument{
		Document: domain.Document{ID: "d1", Title: "Rapport", Status: domain.DocumentStatusPending},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents/d1", nil)
	req.SetPathValue("id", "d1")
	getDocumentHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"pdf"`) {
		t.Errorf("pending document body carries a pdf key: %s", rec.Body.String())
	}
}

func TestGetDocumentHandlerNotFound(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents/missing", nil)
	req.SetPathValue("id", "missing")
	getDocumentHandler(&fakeDocumentService{getErr: domain.ErrDocumentNotFound})(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDocumentClaimsHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeDocumentService{analysis: service.DocumentAnalysis{
		Document: domain.Document{
			ID: "d1", Status: domain.DocumentStatusReady,
			AnalysisStatus: domain.DocumentAnalysisComplete, SentencesTotal: 2, SentencesProcessed: 2,
		},
		Sentences: []service.DocumentSentenceClaims{
			{
				Sentence: domain.DocumentSentence{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1, SkipReason: domain.SkipReasonNotAClaim},
				Claims:   []domain.DocumentClaim{},
			},
			{
				Sentence: domain.DocumentSentence{Seq: 1, Page: 1, Text: "Deux.", Occurrence: 1},
				Claims: []domain.DocumentClaim{{
					ID: "dc-1", SentenceSeq: 1, ClaimID: "c-1", Text: "Deux.",
					Status: domain.DocumentClaimVerified, Source: "verified", Verdict: "credible",
					Basis: "evidence", Flags: []string{}, Confidence: 0.9, Rationale: "solide",
					Citations: []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "X", Similarity: 0.9}},
				}},
			},
		},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents/d1/claims", nil)
	req.SetPathValue("id", "d1")
	documentClaimsHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got documentClaimsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Document.ID != "d1" || got.Document.AnalysisStatus != "complete" {
		t.Errorf("document = %+v, want analysis state", got.Document)
	}
	if len(got.Sentences) != 2 {
		t.Fatalf("sentences = %d, want 2", len(got.Sentences))
	}
	if got.Sentences[0].SkipReason != "not_a_claim" {
		t.Errorf("skip reason = %q, want not_a_claim", got.Sentences[0].SkipReason)
	}
	if len(got.Sentences[0].Claims) != 0 {
		t.Errorf("sentence 0 claims = %+v, want empty array", got.Sentences[0].Claims)
	}
	claims := got.Sentences[1].Claims
	if len(claims) != 1 || claims[0].ClaimID != "c-1" || claims[0].Verdict != "credible" {
		t.Fatalf("sentence 1 claims = %+v, want the verdict", claims)
	}
	if len(claims[0].Citations) != 1 || claims[0].Citations[0].Claim != "X" {
		t.Errorf("citations = %+v, want the live wire shape", claims[0].Citations)
	}
	// Empty claim lists must serialize as [] (never null) so the frontend can
	// consume them without null checks.
	if strings.Contains(rec.Body.String(), `"claims":null`) {
		t.Errorf("body carries null claims: %s", rec.Body.String())
	}
}

func TestDocumentClaimsHandlerNotFound(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/documents/missing/claims", nil)
	req.SetPathValue("id", "missing")
	documentClaimsHandler(&fakeDocumentService{claimsErr: domain.ErrDocumentNotFound})(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteDocumentHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "deleted", err: nil, wantCode: http.StatusNoContent},
		{name: "not found", err: domain.ErrDocumentNotFound, wantCode: http.StatusNotFound},
		{name: "internal", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeDocumentService{deleteErr: tc.err}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/documents/d1", nil)
			req.SetPathValue("id", "d1")
			deleteDocumentHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if svc.lastDeleteID != "d1" {
				t.Errorf("delete id = %q, want d1", svc.lastDeleteID)
			}
		})
	}
}

// newDocumentsTestServer builds the full router around a fake document service
// so the documents routes are exercised through the real identity and admin
// gates.
func newDocumentsTestServer(svc *fakeDocumentService) http.Handler {
	health := service.NewHealthChecker(fakePinger{})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewMux(health, &fakeVideoService{}, svc, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, stubLiveAnalyzer{}, nil, nil, nil, false, nil, "", globalTestAuth, logger)
}

// TestDocumentRoutesRoleGating proves the split the design fixes: mutating
// document routes require a verified admin claim (403 for a guest), read
// routes serve any authenticated user, and nothing is reachable anonymously.
func TestDocumentRoutesRoleGating(t *testing.T) {
	t.Parallel()
	uploadBody := `{"title":"x","content_type":"application/pdf","size_bytes":1}`
	extractionBody := `{"page_count":1,"sentences":[{"seq":0,"page":1,"text":"x","occurrence":1}]}`

	tests := []struct {
		name         string
		method, path string
		body         string
		bearer       string
		wantCode     int
	}{
		{name: "upload as admin", method: http.MethodPost, path: "/api/documents/uploads", body: uploadBody, bearer: testAdminToken, wantCode: http.StatusCreated},
		{name: "upload as guest", method: http.MethodPost, path: "/api/documents/uploads", body: uploadBody, bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "upload anonymous", method: http.MethodPost, path: "/api/documents/uploads", body: uploadBody, wantCode: http.StatusUnauthorized},
		{name: "extraction as admin", method: http.MethodPost, path: "/api/documents/d1/extraction", body: extractionBody, bearer: testAdminToken, wantCode: http.StatusOK},
		{name: "extraction as guest", method: http.MethodPost, path: "/api/documents/d1/extraction", body: extractionBody, bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "delete as admin", method: http.MethodDelete, path: "/api/documents/d1", bearer: testAdminToken, wantCode: http.StatusNoContent},
		{name: "delete as guest", method: http.MethodDelete, path: "/api/documents/d1", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "reanalyse as admin", method: http.MethodPost, path: "/api/documents/d1/reanalyse", bearer: testAdminToken, wantCode: http.StatusAccepted},
		{name: "reanalyse as guest", method: http.MethodPost, path: "/api/documents/d1/reanalyse", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "reanalyse anonymous", method: http.MethodPost, path: "/api/documents/d1/reanalyse", wantCode: http.StatusUnauthorized},
		{name: "list as guest", method: http.MethodGet, path: "/api/documents", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "get as guest", method: http.MethodGet, path: "/api/documents/d1", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "claims as guest", method: http.MethodGet, path: "/api/documents/d1/claims", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "list anonymous", method: http.MethodGet, path: "/api/documents", wantCode: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newDocumentsTestServer(&fakeDocumentService{})
			rec := httptest.NewRecorder()
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, body)
			if tc.bearer != "" {
				bearer(req, tc.bearer)
			}
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("%s %s = %d, want %d; body=%s", tc.method, tc.path, rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

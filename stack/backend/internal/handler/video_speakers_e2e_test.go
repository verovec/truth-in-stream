package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/speakername"
)

// speakerE2EStore is the persistence behind the end-to-end test: one stored
// analysis with its video record, and an in-memory speaker mapping honoring
// the store contract (a replace never touches operator-set rows).
type speakerE2EStore struct {
	storedAnalysisStub
	video domain.Video
	rows  map[string]domain.VideoSpeaker
}

func (s *speakerE2EStore) GetVideo(context.Context, string) (domain.Video, error) {
	return s.video, nil
}

func (s *speakerE2EStore) ListVideoSpeakers(context.Context, string) ([]domain.VideoSpeaker, error) {
	out := make([]domain.VideoSpeaker, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b domain.VideoSpeaker) int { return strings.Compare(a.Label, b.Label) })
	return out, nil
}

func (s *speakerE2EStore) ReplaceProposedVideoSpeakers(_ context.Context, _ string, speakers []domain.VideoSpeaker) error {
	for label, row := range s.rows {
		if !row.OperatorSet {
			delete(s.rows, label)
		}
	}
	for _, speaker := range speakers {
		if _, held := s.rows[speaker.Label]; !held {
			s.rows[speaker.Label] = speaker
		}
	}
	return nil
}

func (s *speakerE2EStore) SetOperatorVideoSpeaker(_ context.Context, videoID, label, name string, role domain.SpeakerRole) (domain.VideoSpeaker, error) {
	row := s.rows[label]
	row.VideoID, row.Label, row.Name, row.Role = videoID, label, name, role
	row.Confidence, row.Method, row.OperatorSet = 1, domain.SpeakerMethodOperator, true
	s.rows[label] = row
	return row, nil
}

// participantsOnlyModel lists the participants the transcript names and never
// settles a tie.
type participantsOnlyModel struct{ participants []speakername.Participant }

func (m participantsOnlyModel) Participants(context.Context, string, []speakername.Turn) ([]speakername.Participant, error) {
	return m.participants, nil
}

func (participantsOnlyModel) BreakTie(context.Context, string, []string, []speakername.Turn) (string, string, error) {
	return "", "", nil
}

// TestSpeakerNamingEndToEnd drives the feature through the real mux, the real
// naming service and the real stored-analysis reader: a stored two-voice
// analysis is named on demand, the names reach the analysis read and both
// exports, an operator correction replaces one, and a re-run keeps it.
func TestSpeakerNamingEndToEnd(t *testing.T) {
	t.Parallel()
	subtitle := func(speaker string, start, end int, text string) service.LiveEvent {
		return service.LiveEvent{Kind: service.LiveEventSubtitle, Segment: domain.Segment{
			Speaker: speaker, Start: time.Duration(start) * time.Second, End: time.Duration(end) * time.Second, Text: text,
		}}
	}
	events := []service.LiveEvent{
		subtitle("B", 0, 6, "Bonsoir, nous recevons ce soir Marine Durand."),
		subtitle("B", 6, 10, "Madame Durand, vous avez deux minutes."),
		subtitle("A", 10, 70, "Le chômage a baissé de deux points en un an."),
		{
			Kind: service.LiveEventResult, ClaimID: "c1", ClaimStatus: service.ClaimStatusUnchecked,
			Segment: domain.Segment{Speaker: "A", Start: 10 * time.Second, End: 70 * time.Second, Text: "Le chômage a baissé de deux points en un an."},
		},
		subtitle("B", 70, 74, "Une précision, Madame Durand, à vous."),
		subtitle("A", 74, 130, "Je maintiens ce chiffre, il est public."),
	}
	payload, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	video := domain.Video{ID: "vid-1", Title: "Le grand entretien", AnalysisStatus: domain.VideoAnalysisComplete}
	store := &speakerE2EStore{
		storedAnalysisStub: storedAnalysisStub{analysis: domain.VideoAnalysis{
			VideoID: "vid-1", SnapshotVersion: service.SnapshotVersion, Events: payload, Engine: []byte(`{}`),
		}},
		video: video,
		rows:  make(map[string]domain.VideoSpeaker),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reader, err := service.NewStoredAnalysisReader(store, store, logger)
	if err != nil {
		t.Fatalf("NewStoredAnalysisReader: %v", err)
	}
	model := participantsOnlyModel{participants: []speakername.Participant{
		{Name: "Marine Durand", Quote: "nous recevons ce soir Marine Durand"},
		{Name: "Jean Dupont", Quote: "Jean Dupont, the well-known host"},
	}}
	namer, err := service.NewSpeakerNamer(store, reader, store, model, logger)
	if err != nil {
		t.Fatalf("NewSpeakerNamer: %v", err)
	}
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: video}}
	srv := NewMux(service.NewHealthChecker(fakePinger{}), videos, reader, &fakeVideoAnalysisStarter{}, namer, &fakeVideoRecapService{}, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, &fakeTVChannelService{}, &fakeTVRecordingService{}, testTVHub(), stubLiveAnalyzer{}, nil, reader, nil, false, nil, "", globalTestAuth, logger)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		bearer(req, testAdminToken)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d; body=%s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	speakersOf := func(rec *httptest.ResponseRecorder) map[string]videoSpeakerJSON {
		t.Helper()
		var body videoSpeakersResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make(map[string]videoSpeakerJSON, len(body.Speakers))
		for _, s := range body.Speakers {
			out[s.Label] = s
		}
		return out
	}

	// Before the step runs, the analysis reads fine and every voice is a letter.
	if before := call(http.MethodGet, "/api/videos/vid-1/speakers", ""); strings.TrimSpace(before.Body.String()) != `{"speakers":[]}` {
		t.Fatalf("mapping before naming = %s", before.Body.String())
	}
	if srt := call(http.MethodGet, "/api/videos/vid-1/export/transcript.srt", "").Body.String(); !strings.Contains(srt, "A: Le chômage") {
		t.Fatalf("SRT before naming lacks the letter:\n%s", srt)
	}

	named := speakersOf(call(http.MethodPost, "/api/videos/vid-1/speakers/name", ""))
	if a := named["A"]; a.Name != "Marine Durand" || a.Role != "principal" || a.Method != "signals" || len(a.Evidence) == 0 {
		t.Errorf("label A after naming = %+v", a)
	}
	if b := named["B"]; b.Name != "" || b.Role != "moderator" {
		t.Errorf("label B after naming = %+v, want an unnamed moderator (the ungrounded name must not be used)", b)
	}

	var analysis struct {
		Speakers []videoSpeakerJSON `json:"speakers"`
		Frames   []map[string]any   `json:"frames"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/videos/vid-1/analysis", "").Body.Bytes(), &analysis); err != nil {
		t.Fatalf("decode analysis: %v", err)
	}
	if len(analysis.Speakers) != 2 || analysis.Speakers[0].Name != "Marine Durand" {
		t.Errorf("analysis speakers = %+v", analysis.Speakers)
	}
	if got := analysis.Frames[0]["speaker"]; got != "B" {
		t.Errorf("stored frames must keep the diarized letter, got %v", got)
	}

	if srt := call(http.MethodGet, "/api/videos/vid-1/export/transcript.srt", "").Body.String(); !strings.Contains(srt, "Marine Durand: Le chômage") || !strings.Contains(srt, "B: Bonsoir") {
		t.Errorf("SRT after naming:\n%s", srt)
	}
	if csv := call(http.MethodGet, "/api/videos/vid-1/export/claims.csv", "").Body.String(); !strings.HasSuffix(csv, ",Marine Durand\n") {
		t.Errorf("CSV after naming:\n%s", csv)
	}

	call(http.MethodPatch, "/api/videos/vid-1/speakers/B", `{"name":"Claire Martin","role":"moderator"}`)
	call(http.MethodPatch, "/api/videos/vid-1/speakers/A", `{"name":"Sophie Garnier","role":"guest"}`)

	rerun := speakersOf(call(http.MethodPost, "/api/videos/vid-1/speakers/name", ""))
	if a := rerun["A"]; a.Name != "Sophie Garnier" || a.Role != "guest" || !a.OperatorSet {
		t.Errorf("label A after the re-run = %+v, want the operator's decision kept", a)
	}
	if b := rerun["B"]; b.Name != "Claire Martin" || !b.OperatorSet {
		t.Errorf("label B after the re-run = %+v, want the operator's decision kept", b)
	}
	if srt := call(http.MethodGet, "/api/videos/vid-1/export/transcript.srt", "").Body.String(); !strings.Contains(srt, "Sophie Garnier: Le chômage") || !strings.Contains(srt, "Claire Martin: Bonsoir") {
		t.Errorf("SRT after the corrections:\n%s", srt)
	}
}

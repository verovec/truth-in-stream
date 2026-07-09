package handler

import (
	"bytes"
	"encoding/csv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// TestExportEndToEndOverRedis drives the whole export path through the real
// layers: a real store.RedisCache (over an in-memory Redis), the real
// service.SnapshotPersister that seeds a completed session under analysis:v1:{id},
// the real service.SnapshotReader the handler reads through, and the real NewMux
// router with the admin gate. It proves an admin downloads a valid SRT and a
// clean CSV (special characters intact) for a cached video, a guest is refused,
// and a video with no snapshot returns 404 - with the transcription/LLM pipeline
// never touched.
func TestExportEndToEndOverRedis(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := store.NewRedisCache(client)

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	persister, err := service.NewSnapshotPersister(cache, time.Hour, logger)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	reader, err := service.NewSnapshotReader(cache, logger)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	const cachedVideo = "cached-vid"
	seg := domain.Segment{Start: time.Second, End: 3*time.Second + 500*time.Millisecond, Speaker: "Le « Président »", Text: "Il a dit: \"oui, non\"."}
	cached := []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		// A non-terminal placeholder the verify path emits, then the terminal verdict
		// for the same claim: the CSV must keep exactly one row.
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusChecking},
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0",
			ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{
				Verdict: "disputed", Basis: "evidence", Confidence: 0.8,
				Rationale: "Comma, \"quote\" and accents é.", Literal: "inaccurate",
				Flags: []string{"missing-context", "cherry-picked"},
				Citations: []domain.SegmentMatch{{
					Kind: domain.MatchKindEvidence, Sources: []domain.Source{{Title: "INSEE", URL: "https://insee.fr/x"}},
					Similarity: 0.91, EvidenceID: "ev-1",
				}},
			},
		},
	}
	if err := persister.Persist(t.Context(), cachedVideo, cached); err != nil {
		t.Fatalf("seed persist: %v", err)
	}
	if _, err := mr.Get("analysis:v1:" + cachedVideo); err != nil {
		t.Fatalf("snapshot not under namespaced key: %v", err)
	}

	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: cachedVideo, Title: "Débat 2026"}}}
	health := service.NewHealthChecker(fakePinger{})
	srv := httptest.NewServer(NewMux(health, videos, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, stubLiveAnalyzer{}, persister, reader, nil, false, nil, "", globalTestAuth, logger))
	t.Cleanup(srv.Close)

	get := func(t *testing.T, path, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		return resp
	}

	t.Run("admin downloads a valid SRT", func(t *testing.T) {
		resp := get(t, "/api/videos/"+cachedVideo+"/export/transcript.srt", testAdminToken)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "debat-2026.srt") {
			t.Fatalf("Content-Disposition = %q", cd)
		}
		body, _ := io.ReadAll(resp.Body)
		assertValidSRT(t, body)
		if !bytes.Contains(body, []byte("Le « Président »: Il a dit:")) {
			t.Fatalf("speaker-prefixed accented text missing:\n%s", body)
		}
	})

	t.Run("admin downloads a clean CSV with escaped specials", func(t *testing.T) {
		resp := get(t, "/api/videos/"+cachedVideo+"/export/claims.csv", testAdminToken)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		rows, err := csv.NewReader(bytes.NewReader(mustRead(t, resp.Body))).ReadAll()
		if err != nil {
			t.Fatalf("CSV did not parse: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("want header + 1 terminal claim row, got %d rows: %v", len(rows), rows)
		}
		idx := map[string]int{}
		for i, name := range rows[0] {
			idx[name] = i
		}
		row := rows[1]
		if row[idx["statement"]] != "Il a dit: \"oui, non\"." {
			t.Fatalf("statement not round-tripped: %q", row[idx["statement"]])
		}
		if row[idx["manipulation_flags"]] != "missing-context | cherry-picked" {
			t.Fatalf("flags = %q", row[idx["manipulation_flags"]])
		}
		if row[idx["citations"]] != "INSEE <https://insee.fr/x> [ev-1] sim=0.91" {
			t.Fatalf("citations = %q", row[idx["citations"]])
		}
		if row[idx["claim_status"]] != "verified" {
			t.Fatalf("claim_status = %q (checking placeholder should be dropped)", row[idx["claim_status"]])
		}
	})

	t.Run("guest is refused with 403", func(t *testing.T) {
		resp := get(t, "/api/videos/"+cachedVideo+"/export/claims.csv", testGuestToken)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("missing snapshot returns 404", func(t *testing.T) {
		resp := get(t, "/api/videos/never-analyzed/export/transcript.srt", testAdminToken)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		if !strings.Contains(strings.ToLower(string(mustRead(t, resp.Body))), "re-run") {
			t.Fatal("404 body should tell the operator to re-run analysis")
		}
	})
}

var srtCueLine = regexp.MustCompile(`^\d{2}:\d{2}:\d{2},\d{3} --> \d{2}:\d{2}:\d{2},\d{3}$`)

// assertValidSRT checks the bytes are a well-formed SRT: a 1-based numeric index,
// a comma-decimal zero-padded cue range, and at least one text line per block.
func assertValidSRT(t *testing.T, body []byte) {
	t.Helper()
	blocks := strings.Split(strings.TrimRight(string(body), "\n"), "\n\n")
	if len(blocks) == 0 || blocks[0] == "" {
		t.Fatalf("empty SRT:\n%s", body)
	}
	for i, block := range blocks {
		lines := strings.Split(block, "\n")
		if len(lines) < 3 {
			t.Fatalf("block %d has too few lines: %q", i, block)
		}
		if lines[0] != itoa(i+1) {
			t.Fatalf("block %d index = %q, want %d", i, lines[0], i+1)
		}
		if !srtCueLine.MatchString(lines[1]) {
			t.Fatalf("block %d cue line malformed: %q", i, lines[1])
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func mustRead(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

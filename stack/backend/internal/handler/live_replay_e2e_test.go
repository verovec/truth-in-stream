package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// TestLiveReplayEndToEndOverRedis exercises the whole VER-146 path end to end
// through the real layers: a real store.RedisCache (over an in-memory Redis), the
// real service.SnapshotPersister that captures a finished session, and the real
// service.SnapshotReader that serves it back on a later open - all driven through
// the real liveHandler over a real WebSocket. It proves the two behaviors the
// card requires:
//
//   - re-opening a video whose analysis was persisted to analysis:v1:{id} replays
//     the full transcript and verdicts instantly, with the transcription/LLM
//     pipeline (the analyzer's Run) never invoked;
//   - opening a different video that has no cached snapshot falls through to the
//     live pipeline exactly as before.
func TestLiveReplayEndToEndOverRedis(t *testing.T) {
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

	// Seed the cache exactly as a completed live session would: persist an ordered
	// event sequence under the cached video's id, through the real persister and the
	// real cache. This is the analysis:v1:{id} entry the replay path reads back.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	claim := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "earth shape", Verdict: domain.Verdict("corroborates"), Sources: []domain.Source{}, Similarity: 0.9}}
	cached := []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, Matches: claim},
	}
	const cachedVideo = "cached-vid"
	if err := persister.Persist(t.Context(), cachedVideo, cached); err != nil {
		t.Fatalf("seed persist: %v", err)
	}
	// The card pins the key format to analysis:v1:{id}; confirm the snapshot landed
	// under exactly that namespaced key.
	const cachedKey = "analysis:v1:" + cachedVideo
	if _, err := mr.Get(cachedKey); err != nil {
		t.Fatalf("snapshot was not written under the namespaced key %q: %v", cachedKey, err)
	}

	live := &countingLive{}
	mux := http.NewServeMux()
	mux.Handle("GET /api/videos/{id}/live", middleware.Identity(stubVerifier{})(liveHandler(live, persister, reader, nil, false, logger)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/videos/"

	// 1) Cache hit: re-opening the cached video replays its full sequence and never
	//    touches the live pipeline.
	t.Run("cache hit replays without pipeline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, resp, err := websocket.Dial(ctx, wsBase+cachedVideo+"/live", nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = conn.CloseNow() }()

		subtitle := readFrame(ctx, t, conn)
		if subtitle.Type != "subtitle" || subtitle.ID != "0" || subtitle.Text != seg.Text {
			t.Fatalf("subtitle = %+v, want the cached transcript", subtitle)
		}
		result := readFrame(ctx, t, conn)
		if result.Type != "result" || result.ID != "0" || len(result.Matches) != 1 || result.Matches[0].Claim != claim[0].Claim {
			t.Fatalf("result = %+v, want the cached verdict", result)
		}
		if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Errorf("after replay the socket should close normally, got %v", err)
		}
		if live.runCount() != 0 {
			t.Errorf("transcription/LLM pipeline ran %d times on a cache hit, want 0", live.runCount())
		}
	})

	// 2) Cache miss: a different video has no snapshot, so the live pipeline runs.
	t.Run("uncached video runs the live pipeline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, resp, err := websocket.Dial(ctx, wsBase+"fresh-vid/live", nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = conn.CloseNow() }()

		if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
			t.Fatalf("write audio: %v", err)
		}
		frame := readFrame(ctx, t, conn)
		if frame.Type != "subtitle" || frame.ID != "live" {
			t.Fatalf("frame = %+v, want the live pipeline's output", frame)
		}
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			t.Fatalf("clean close: %v", err)
		}
		if live.runCount() != 1 {
			t.Errorf("live pipeline ran %d times for an uncached video, want 1", live.runCount())
		}
	})
}

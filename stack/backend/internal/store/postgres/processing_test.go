package postgres

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func testMatches() []domain.SegmentMatch {
	return []domain.SegmentMatch{{
		Claim:      "the sky is blue",
		Verdict:    domain.VerdictCorroborates,
		Sources:    []domain.Source{{Title: "Sky study", URL: "https://sky.example"}},
		Similarity: 0.92,
	}}
}

func TestSaveSegmentResultRejectsInvalidSkipReason(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	err := store.SaveSegmentResult(ctx, "vid-1", domain.SegmentResult{
		Segment:    domain.Segment{Start: 0, End: time.Second, Text: "bad"},
		Matches:    []domain.SegmentMatch{},
		SkipReason: domain.SkipReason("bogus"),
	})
	if err == nil {
		t.Fatal("SaveSegmentResult accepted an invalid skip reason, want rejection")
	}

	got, err := store.ListSegmentResults(ctx, "vid-1")
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("invalid skip reason was persisted (%d rows), want none", len(got))
	}
}

func TestSegmentResultsRoundTripOrderedByStart(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const videoID = "vid-1"

	results := []domain.SegmentResult{
		{Segment: domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "third"}, Matches: []domain.SegmentMatch{}},
		{Segment: domain.Segment{Start: 1500 * time.Millisecond, End: 2250 * time.Millisecond, Text: "second"}, Matches: testMatches()},
		{Segment: domain.Segment{Start: 0, End: time.Second, Text: "first"}, Matches: testMatches()},
	}
	for _, r := range results {
		if err := store.SaveSegmentResult(ctx, videoID, r); err != nil {
			t.Fatalf("SaveSegmentResult(%v): %v", r.Start, err)
		}
	}

	got, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	want := []domain.SegmentResult{results[2], results[1], results[0]}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("results mismatch (-want +got):\n%s", diff)
	}
}

func TestSaveSegmentResultUpsertsByStart(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const videoID = "vid-1"

	first := domain.SegmentResult{
		Segment: domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "first pass"},
		Matches: []domain.SegmentMatch{},
	}
	second := domain.SegmentResult{
		Segment: domain.Segment{Start: time.Second, End: 3 * time.Second, Text: "second pass"},
		Matches: testMatches(),
	}
	if err := store.SaveSegmentResult(ctx, videoID, first); err != nil {
		t.Fatalf("SaveSegmentResult first: %v", err)
	}
	if err := store.SaveSegmentResult(ctx, videoID, second); err != nil {
		t.Fatalf("SaveSegmentResult second: %v", err)
	}

	got, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if diff := cmp.Diff([]domain.SegmentResult{second}, got); diff != "" {
		t.Errorf("upsert mismatch (-want +got):\n%s", diff)
	}
}

func TestSkipReasonRoundTrips(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const videoID = "vid-1"

	results := []domain.SegmentResult{
		{Segment: domain.Segment{Start: 0, End: time.Second, Text: "checked claim"}, Matches: testMatches()},
		{Segment: domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "a question"}, Matches: []domain.SegmentMatch{}, SkipReason: domain.SkipReasonNotAClaim},
		{Segment: domain.Segment{Start: 2 * time.Second, End: 3 * time.Second, Text: "uncovered"}, Matches: []domain.SegmentMatch{}, SkipReason: domain.SkipReasonNotCovered},
	}
	for _, r := range results {
		if err := store.SaveSegmentResult(ctx, videoID, r); err != nil {
			t.Fatalf("SaveSegmentResult(%v): %v", r.Start, err)
		}
	}

	got, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if diff := cmp.Diff(results, got); diff != "" {
		t.Errorf("skip reasons did not round-trip (-want +got):\n%s", diff)
	}
}

func TestResultsAreScopedByVideo(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.SaveSegmentResult(ctx, "vid-a", domain.SegmentResult{
		Segment: domain.Segment{Start: 0, End: time.Second, Text: "a"},
		Matches: []domain.SegmentMatch{},
	}); err != nil {
		t.Fatalf("SaveSegmentResult: %v", err)
	}

	got, err := store.ListSegmentResults(ctx, "vid-b")
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("vid-b has %d results, want 0", len(got))
	}
}

func TestNilMatchesNormalizeToEmpty(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const videoID = "vid-1"

	if err := store.SaveSegmentResult(ctx, videoID, domain.SegmentResult{
		Segment: domain.Segment{Start: 0, End: time.Second, Text: "no matches"},
	}); err != nil {
		t.Fatalf("SaveSegmentResult: %v", err)
	}

	got, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Matches == nil {
		t.Error("matches round-tripped as nil, want empty slice")
	}
}

func TestDeleteSegmentResults(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	for _, seed := range []struct {
		videoID string
		start   time.Duration
		text    string
	}{
		{videoID: "vid-a", start: 0, text: "a1"},
		{videoID: "vid-a", start: time.Second, text: "a2"},
		{videoID: "vid-b", start: 0, text: "b1"},
	} {
		if err := store.SaveSegmentResult(ctx, seed.videoID, domain.SegmentResult{
			Segment: domain.Segment{Start: seed.start, End: seed.start + time.Second, Text: seed.text},
			Matches: []domain.SegmentMatch{},
		}); err != nil {
			t.Fatalf("SaveSegmentResult %s: %v", seed.text, err)
		}
	}

	if err := store.DeleteSegmentResults(ctx, "vid-a"); err != nil {
		t.Fatalf("DeleteSegmentResults: %v", err)
	}

	gotA, err := store.ListSegmentResults(ctx, "vid-a")
	if err != nil {
		t.Fatalf("ListSegmentResults vid-a: %v", err)
	}
	if len(gotA) != 0 {
		t.Errorf("vid-a has %d results after delete, want 0", len(gotA))
	}
	gotB, err := store.ListSegmentResults(ctx, "vid-b")
	if err != nil {
		t.Fatalf("ListSegmentResults vid-b: %v", err)
	}
	if len(gotB) != 1 {
		t.Errorf("vid-b has %d results, want 1 (delete must be scoped by video)", len(gotB))
	}
}

func TestProcessedSegmentCount(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const videoID = "vid-1"

	if _, processed, err := store.ProcessedSegmentCount(ctx, videoID); err != nil || processed {
		t.Fatalf("ProcessedSegmentCount before mark = (processed=%v, err=%v), want (false, nil)", processed, err)
	}

	if err := store.MarkVideoProcessed(ctx, videoID, 7); err != nil {
		t.Fatalf("MarkVideoProcessed: %v", err)
	}
	count, processed, err := store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		t.Fatalf("ProcessedSegmentCount: %v", err)
	}
	if !processed || count != 7 {
		t.Errorf("ProcessedSegmentCount = (%d, %v), want (7, true)", count, processed)
	}

	if err := store.MarkVideoProcessed(ctx, videoID, 9); err != nil {
		t.Fatalf("MarkVideoProcessed again: %v", err)
	}
	count, processed, err = store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		t.Fatalf("ProcessedSegmentCount after re-mark: %v", err)
	}
	if !processed || count != 9 {
		t.Errorf("ProcessedSegmentCount after re-mark = (%d, %v), want (9, true)", count, processed)
	}
}

// Wire-shape check that runs without a database.

func TestMarshalMatchesShape(t *testing.T) {
	t.Parallel()
	raw, err := marshalMatches(testMatches())
	if err != nil {
		t.Fatalf("marshalMatches: %v", err)
	}
	want := `[{"claim":"the sky is blue","verdict":"corroborates","sources":[{"title":"Sky study","url":"https://sky.example"}],"similarity":0.92}]`
	if got := string(raw); got != want {
		t.Errorf("encoding = %s, want %s", got, want)
	}
}

func TestMatchesEncodingRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []domain.SegmentMatch
		want []domain.SegmentMatch
	}{
		{name: "nil normalizes to empty", in: nil, want: []domain.SegmentMatch{}},
		{name: "empty stays empty", in: []domain.SegmentMatch{}, want: []domain.SegmentMatch{}},
		{name: "matches preserved in order", in: testMatches(), want: testMatches()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := marshalMatches(tc.in)
			if err != nil {
				t.Fatalf("marshalMatches: %v", err)
			}
			got, err := unmarshalMatches(raw)
			if err != nil {
				t.Fatalf("unmarshalMatches: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

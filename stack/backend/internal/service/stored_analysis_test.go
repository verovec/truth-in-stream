package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeAnalysisStore is a domain.VideoAnalysisStore stand-in that records the
// completed analysis it receives and serves a canned stored record.
type fakeAnalysisStore struct {
	stored      domain.VideoAnalysis
	getErr      error
	completeErr error
	lastPersist domain.VideoAnalysis
}

func (f *fakeAnalysisStore) CompleteVideoAnalysis(_ context.Context, a domain.VideoAnalysis) (domain.VideoAnalysis, error) {
	f.lastPersist = a
	if f.completeErr != nil {
		return domain.VideoAnalysis{}, f.completeErr
	}
	return a, nil
}

func (f *fakeAnalysisStore) GetVideoAnalysis(context.Context, string) (domain.VideoAnalysis, error) {
	if f.getErr != nil {
		return domain.VideoAnalysis{}, f.getErr
	}
	return f.stored, nil
}

// fakeVideoGetter is a videoRecordSource stand-in.
type fakeVideoGetter struct {
	video domain.Video
	err   error
}

func (f *fakeVideoGetter) GetVideo(context.Context, string) (domain.Video, error) {
	if f.err != nil {
		return domain.Video{}, f.err
	}
	return f.video, nil
}

// storedEvents is a small completed session: a subtitle, two announced claims,
// one credible verdict, one disputed verdict that a terminal-gate upgrade then
// moves to credible.
func storedEvents() []LiveEvent {
	return []LiveEvent{
		{Kind: LiveEventSubtitle, ID: "s1", Segment: domain.Segment{Start: time.Second, End: 3 * time.Second, Text: "bonjour"}},
		{Kind: LiveEventClaims, ID: "s1", Claims: []AtomicClaim{{ClaimID: "c1", Text: "one"}, {ClaimID: "c2", Text: "two"}}},
		{Kind: LiveEventResult, ID: "s1", ClaimID: "c1", ClaimStatus: ClaimStatusVerified, Verdict: &VerifiedVerdict{Verdict: VerdictCredible}},
		{Kind: LiveEventResult, ID: "s1", ClaimID: "c2", ClaimStatus: ClaimStatusVerified, Verdict: &VerifiedVerdict{Verdict: VerdictDisputed}},
		{Kind: LiveEventResult, ID: "s1", ClaimID: "c2", ClaimStatus: ClaimStatusVerified, Verdict: &VerifiedVerdict{Verdict: VerdictCredible}},
	}
}

func TestCountClaimVerdicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		events []LiveEvent
		want   [4]int
	}{
		{name: "empty stream", events: nil, want: [4]int{0, 0, 0, 0}},
		{
			name:   "gate upgrade moves the claim between buckets",
			events: storedEvents(),
			want:   [4]int{2, 2, 0, 0},
		},
		{
			name: "one claim per bucket",
			events: []LiveEvent{
				{Kind: LiveEventClaims, ID: "s1", Claims: []AtomicClaim{{ClaimID: "c1"}, {ClaimID: "c2"}, {ClaimID: "c3"}}},
				{Kind: LiveEventResult, ClaimID: "c1", Verdict: &VerifiedVerdict{Verdict: VerdictCredible}},
				{Kind: LiveEventResult, ClaimID: "c2", Verdict: &VerifiedVerdict{Verdict: VerdictDisputed}},
				{Kind: LiveEventResult, ClaimID: "c3", Verdict: &VerifiedVerdict{Verdict: VerdictUnverifiable}},
			},
			want: [4]int{3, 1, 1, 1},
		},
		{
			name: "unresolved claim counts in the total only",
			events: []LiveEvent{
				{Kind: LiveEventClaims, ID: "s1", Claims: []AtomicClaim{{ClaimID: "c1"}, {ClaimID: "c2"}}},
				{Kind: LiveEventResult, ClaimID: "c1", ClaimStatus: ClaimStatusUnchecked},
				{Kind: LiveEventResult, ClaimID: "c2", Verdict: &VerifiedVerdict{Verdict: VerdictCredible}},
			},
			want: [4]int{2, 1, 0, 0},
		},
		{
			name: "verdict without an announcing claims frame still counts",
			events: []LiveEvent{
				{Kind: LiveEventResult, ClaimID: "c1", Verdict: &VerifiedVerdict{Verdict: VerdictDisputed}},
			},
			want: [4]int{1, 0, 1, 0},
		},
		{
			name: "legacy segment results carry no claim id and count nothing",
			events: []LiveEvent{
				{Kind: LiveEventSubtitle, ID: "s1"},
				{Kind: LiveEventResult, ID: "s1"},
			},
			want: [4]int{0, 0, 0, 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			total, credible, disputed, unverifiable := countClaimVerdicts(tc.events)
			got := [4]int{total, credible, disputed, unverifiable}
			if got != tc.want {
				t.Errorf("counts = %v, want %v (total, credible, disputed, unverifiable)", got, tc.want)
			}
		})
	}
}

func TestStoredAnalysisPersisterPersist(t *testing.T) {
	t.Parallel()
	t.Run("assembles the completed record", func(t *testing.T) {
		t.Parallel()
		store := &fakeAnalysisStore{}
		p, err := NewStoredAnalysisPersister(store)
		if err != nil {
			t.Fatalf("NewStoredAnalysisPersister: %v", err)
		}
		events := storedEvents()
		stored, err := p.Persist(t.Context(), "v1", events, json.RawMessage(`{"verifier":"m1"}`))
		if err != nil {
			t.Fatalf("Persist: %v", err)
		}
		got := store.lastPersist
		if got.VideoID != "v1" || got.SnapshotVersion != SnapshotVersion {
			t.Errorf("persisted id/version = %q/%d, want v1/%d", got.VideoID, got.SnapshotVersion, SnapshotVersion)
		}
		if string(got.Engine) != `{"verifier":"m1"}` {
			t.Errorf("engine = %s, want the caller's fingerprint", got.Engine)
		}
		if got.ClaimsTotal != 2 || got.ClaimsCredible != 2 || got.ClaimsDisputed != 0 || got.ClaimsUnverifiable != 0 {
			t.Errorf("counters = %d/%d/%d/%d, want 2/2/0/0", got.ClaimsTotal, got.ClaimsCredible, got.ClaimsDisputed, got.ClaimsUnverifiable)
		}
		var decoded []LiveEvent
		if err := json.Unmarshal(got.Events, &decoded); err != nil {
			t.Fatalf("decode persisted events: %v", err)
		}
		if diff := cmp.Diff(events, decoded); diff != "" {
			t.Errorf("events did not round-trip (-want +got):\n%s", diff)
		}
		if stored.VideoID != "v1" {
			t.Errorf("returned record id = %q, want v1", stored.VideoID)
		}
	})

	t.Run("defaults an absent engine to an empty object", func(t *testing.T) {
		t.Parallel()
		store := &fakeAnalysisStore{}
		p, _ := NewStoredAnalysisPersister(store)
		if _, err := p.Persist(t.Context(), "v1", storedEvents(), nil); err != nil {
			t.Fatalf("Persist: %v", err)
		}
		if string(store.lastPersist.Engine) != "{}" {
			t.Errorf("engine = %s, want {}", store.lastPersist.Engine)
		}
	})

	errCases := []struct {
		name    string
		videoID string
		events  []LiveEvent
		engine  json.RawMessage
		wantMsg string
	}{
		{name: "empty video id", videoID: "", events: storedEvents(), wantMsg: "video id is required"},
		{name: "no events", videoID: "v1", events: nil, wantMsg: "no events to persist"},
		{name: "invalid engine json", videoID: "v1", events: storedEvents(), engine: json.RawMessage("{not json"), wantMsg: "engine is not valid JSON"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeAnalysisStore{}
			p, _ := NewStoredAnalysisPersister(store)
			_, err := p.Persist(t.Context(), tc.videoID, tc.events, tc.engine)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Persist error = %v, want %q", err, tc.wantMsg)
			}
			if store.lastPersist.VideoID != "" {
				t.Error("a rejected persist must not reach the store")
			}
		})
	}

	t.Run("wraps a store failure", func(t *testing.T) {
		t.Parallel()
		p, _ := NewStoredAnalysisPersister(&fakeAnalysisStore{completeErr: errors.New("boom")})
		if _, err := p.Persist(t.Context(), "v1", storedEvents(), nil); err == nil {
			t.Fatal("Persist should surface the store failure")
		}
	})

	t.Run("nil store is rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := NewStoredAnalysisPersister(nil); err == nil {
			t.Fatal("NewStoredAnalysisPersister(nil) should fail")
		}
	})
}

// validStored builds a stored record whose payload this build decodes.
func validStored(t *testing.T, events []LiveEvent) domain.VideoAnalysis {
	t.Helper()
	payload, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	return domain.VideoAnalysis{
		VideoID:         "v1",
		SnapshotVersion: SnapshotVersion,
		Events:          payload,
		Engine:          []byte(`{}`),
		ClaimsTotal:     2,
		ClaimsCredible:  2,
	}
}

func TestStoredAnalysisReaderSnapshot(t *testing.T) {
	t.Parallel()
	events := storedEvents()
	tests := []struct {
		name      string
		analyses  *fakeAnalysisStore
		videoID   string
		wantFound bool
	}{
		{name: "valid stored analysis is a hit", analyses: &fakeAnalysisStore{stored: validStored(t, events)}, videoID: "v1", wantFound: true},
		{name: "missing row is a clean miss", analyses: &fakeAnalysisStore{getErr: domain.ErrVideoAnalysisNotFound}, videoID: "v1"},
		{name: "backend failure degrades to a miss", analyses: &fakeAnalysisStore{getErr: errors.New("boom")}, videoID: "v1"},
		{name: "empty video id is a miss", analyses: &fakeAnalysisStore{stored: validStored(t, events)}, videoID: ""},
		{
			name: "unsupported snapshot version is a miss",
			analyses: &fakeAnalysisStore{stored: func() domain.VideoAnalysis {
				a := validStored(t, events)
				a.SnapshotVersion = SnapshotVersion + 1
				return a
			}()},
			videoID: "v1",
		},
		{
			name: "corrupt payload is a miss",
			analyses: &fakeAnalysisStore{stored: func() domain.VideoAnalysis {
				a := validStored(t, events)
				a.Events = []byte("{not json")
				return a
			}()},
			videoID: "v1",
		},
		{
			name: "empty event list is a miss",
			analyses: &fakeAnalysisStore{stored: func() domain.VideoAnalysis {
				a := validStored(t, events)
				a.Events = []byte("[]")
				return a
			}()},
			videoID: "v1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := NewStoredAnalysisReader(&fakeVideoGetter{}, tc.analyses, discardLogger())
			if err != nil {
				t.Fatalf("NewStoredAnalysisReader: %v", err)
			}
			got, found, err := r.Snapshot(t.Context(), tc.videoID)
			if err != nil {
				t.Fatalf("Snapshot must degrade, not fail: %v", err)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !tc.wantFound {
				return
			}
			if diff := cmp.Diff(events, got); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStoredAnalysisReaderGet(t *testing.T) {
	t.Parallel()
	events := storedEvents()

	t.Run("unknown video propagates not found", func(t *testing.T) {
		t.Parallel()
		r, _ := NewStoredAnalysisReader(&fakeVideoGetter{err: domain.ErrVideoNotFound}, &fakeAnalysisStore{}, discardLogger())
		_, err := r.Get(t.Context(), "v1")
		if !errors.Is(err, domain.ErrVideoNotFound) {
			t.Fatalf("err = %v, want ErrVideoNotFound", err)
		}
	})

	t.Run("incomplete lifecycle never touches the analysis store", func(t *testing.T) {
		t.Parallel()
		for _, status := range []domain.VideoAnalysisStatus{domain.VideoAnalysisNone, domain.VideoAnalysisAnalysing, domain.VideoAnalysisFailed} {
			analyses := &fakeAnalysisStore{getErr: errors.New("must not be called")}
			r, _ := NewStoredAnalysisReader(&fakeVideoGetter{video: domain.Video{ID: "v1", AnalysisStatus: status}}, analyses, discardLogger())
			view, err := r.Get(t.Context(), "v1")
			if err != nil {
				t.Fatalf("Get(%s): %v", status, err)
			}
			if view.Analysis != nil || view.Events != nil {
				t.Errorf("status %s should carry no stored result", status)
			}
		}
	})

	t.Run("complete lifecycle carries the result and decoded events", func(t *testing.T) {
		t.Parallel()
		stored := validStored(t, events)
		r, _ := NewStoredAnalysisReader(
			&fakeVideoGetter{video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisComplete}},
			&fakeAnalysisStore{stored: stored},
			discardLogger(),
		)
		view, err := r.Get(t.Context(), "v1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if view.Analysis == nil || view.Analysis.ClaimsTotal != 2 {
			t.Fatalf("analysis = %+v, want the stored record", view.Analysis)
		}
		if diff := cmp.Diff(events, view.Events); diff != "" {
			t.Errorf("events mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("complete lifecycle with an unreadable payload stays honest", func(t *testing.T) {
		t.Parallel()
		stored := validStored(t, events)
		stored.SnapshotVersion = SnapshotVersion + 1
		r, _ := NewStoredAnalysisReader(
			&fakeVideoGetter{video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisComplete}},
			&fakeAnalysisStore{stored: stored},
			discardLogger(),
		)
		view, err := r.Get(t.Context(), "v1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if view.Analysis == nil {
			t.Fatal("the stored metadata should still be reported")
		}
		if view.Events != nil {
			t.Error("an undecodable payload must not yield events")
		}
	})

	t.Run("complete lifecycle with a missing row degrades to lifecycle only", func(t *testing.T) {
		t.Parallel()
		r, _ := NewStoredAnalysisReader(
			&fakeVideoGetter{video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisComplete}},
			&fakeAnalysisStore{getErr: domain.ErrVideoAnalysisNotFound},
			discardLogger(),
		)
		view, err := r.Get(t.Context(), "v1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if view.Analysis != nil || view.Events != nil {
			t.Error("a missing stored row should degrade to lifecycle only")
		}
		if view.Video.AnalysisStatus != domain.VideoAnalysisComplete {
			t.Errorf("lifecycle = %q, want complete reported honestly", view.Video.AnalysisStatus)
		}
	})
}

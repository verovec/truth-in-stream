package report

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newLinearTestClient(srv *httptest.Server, apiKey string) *LinearClient {
	return &LinearClient{
		httpClient: srv.Client(),
		endpoint:   srv.URL,
		apiKey:     apiKey,
		project:    "Truth in Stream",
		now:        func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) },
	}
}

func TestLinearRecentMoves(t *testing.T) {
	t.Parallel()
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[
			{"identifier":"VER-1","title":"First","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"In Review"}},
			{"identifier":"VER-2","title":"Second","updatedAt":"2026-06-11T01:00:00.000Z","state":{"name":"Done"}}
		]}}}`)
	}))
	defer srv.Close()

	moves, err := newLinearTestClient(srv, "lin_secret").RecentMoves(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("RecentMoves: %v", err)
	}
	if len(moves) != 2 || moves[0].ID != "VER-1" || moves[0].State != "In Review" || moves[1].ID != "VER-2" {
		t.Fatalf("parsed moves wrong: %+v", moves)
	}
	if gotAuth != "lin_secret" {
		t.Errorf("Authorization = %q, want raw api key", gotAuth)
	}
	if !strings.Contains(gotBody, "updatedAt") {
		t.Errorf("request did not carry the updatedAt filter: %s", gotBody)
	}
	// The filter value must be an absolute RFC-3339 datetime, not a relative
	// duration like "-PT24H" that Linear's DateComparator does not accept.
	if strings.Contains(gotBody, "-PT") {
		t.Errorf("request sent relative duration instead of absolute datetime: %s", gotBody)
	}
	if !strings.Contains(gotBody, "2026-06-10T12:00:00Z") {
		t.Errorf("updatedAt cutoff is not the expected absolute datetime (24h before injected clock): %s", gotBody)
	}
}

func TestLinearInProgress(t *testing.T) {
	t.Parallel()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"identifier":"VER-9","title":"WIP","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"In Progress"}}]}}}`)
	}))
	defer srv.Close()

	moves, err := newLinearTestClient(srv, "k").InProgress(context.Background())
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if len(moves) != 1 || moves[0].ID != "VER-9" {
		t.Fatalf("parsed = %+v", moves)
	}
	if !strings.Contains(gotBody, "In Progress") {
		t.Errorf("request did not filter by In Progress state: %s", gotBody)
	}
}

func TestLinearAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"bad filter"}]}`)
	}))
	defer srv.Close()
	if _, err := newLinearTestClient(srv, "k").InProgress(context.Background()); err == nil || !strings.Contains(err.Error(), "bad filter") {
		t.Fatalf("want api error, got %v", err)
	}
}

func TestLinearNon200(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := newLinearTestClient(srv, "k").InProgress(context.Background()); err == nil {
		t.Fatal("want error on non-200, got nil")
	}
}

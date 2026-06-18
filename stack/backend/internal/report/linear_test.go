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
			{"identifier":"VER-1","title":"First","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"In Review","type":"started"}},
			{"identifier":"VER-2","title":"Second","updatedAt":"2026-06-11T01:00:00.000Z","state":{"name":"Done","type":"completed"}}
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
	if moves[1].StateType != "completed" {
		t.Errorf("VER-2 StateType = %q, want completed", moves[1].StateType)
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

func TestLinearRemaining(t *testing.T) {
	t.Parallel()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[
			{"identifier":"VER-5","title":"Todo card","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"Todo","type":"unstarted"}}
		]}}}`)
	}))
	defer srv.Close()

	moves, err := newLinearTestClient(srv, "k").Remaining(context.Background())
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if len(moves) != 1 || moves[0].ID != "VER-5" || moves[0].StateType != "unstarted" {
		t.Fatalf("parsed = %+v", moves)
	}
	// The filter must exclude finished and canceled cards by state category.
	for _, want := range []string{"completed", "canceled", "nin"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("remaining filter missing %q: %s", want, gotBody)
		}
	}
}

func TestLinearEpicChildren(t *testing.T) {
	t.Parallel()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[
			{"identifier":"VER-93","title":"Political fact-check","children":{"nodes":[
				{"identifier":"VER-94","title":"French STT","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"Done","type":"completed"}},
				{"identifier":"VER-95","title":"Evidence schema","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"In Progress","type":"started"}}
			]}}
		]}}}`)
	}))
	defer srv.Close()

	title, children, err := newLinearTestClient(srv, "k").EpicChildren(context.Background(), "VER-93")
	if err != nil {
		t.Fatalf("EpicChildren: %v", err)
	}
	if title != "Political fact-check" {
		t.Errorf("epic title = %q", title)
	}
	if len(children) != 2 || children[0].ID != "VER-94" || children[1].ID != "VER-95" {
		t.Fatalf("children = %+v", children)
	}
	if children[0].StateType != "completed" {
		t.Errorf("VER-94 StateType = %q, want completed", children[0].StateType)
	}
	// The epic is resolved by team key and number parsed from the identifier.
	for _, want := range []string{`"key"`, `"VER"`, `"number"`, "93"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("epic filter missing %q: %s", want, gotBody)
		}
	}
}

func TestLinearEpicChildrenNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[]}}}`)
	}))
	defer srv.Close()
	if _, _, err := newLinearTestClient(srv, "k").EpicChildren(context.Background(), "VER-404"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestLinearEpicChildrenInvalidID(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be called for an invalid id")
	}))
	defer srv.Close()
	if _, _, err := newLinearTestClient(srv, "k").EpicChildren(context.Background(), "not-a-number"); err == nil {
		t.Fatal("want error for an unparseable id, got nil")
	}
}

func TestLinearInProgress(t *testing.T) {
	t.Parallel()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"identifier":"VER-9","title":"WIP","updatedAt":"2026-06-11T00:00:00.000Z","state":{"name":"In Progress","type":"started"}}]}}}`)
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

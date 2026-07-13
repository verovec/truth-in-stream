package tvcapture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSourceCachesAndRefreshes(t *testing.T) {
	t.Parallel()

	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostFormValue("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q", got)
		}
		if got := r.PostFormValue("client_id"); got != "tv-capture" {
			t.Errorf("client_id = %q", got)
		}
		if got := r.PostFormValue("client_secret"); got != "s3cr3t" {
			t.Errorf("client_secret = %q", got)
		}
		n := fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-` + string(rune('0'+n)) + `","expires_in":300}`))
	}))
	defer srv.Close()

	now := time.Unix(1_000_000, 0)
	ts := newTokenSource(srv.Client(), srv.URL, "tv-capture", "s3cr3t")
	ts.now = func() time.Time { return now }

	ctx := context.Background()
	first, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("first token: %v", err)
	}
	if first != "tok-1" {
		t.Fatalf("first token = %q", first)
	}

	// Second call well within TTL must not refetch.
	second, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("second token: %v", err)
	}
	if second != "tok-1" || fetches.Load() != 1 {
		t.Fatalf("expected cached token, got %q after %d fetches", second, fetches.Load())
	}

	// Advance to within the refresh skew of expiry: must refetch.
	now = now.Add(280 * time.Second)
	third, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("third token: %v", err)
	}
	if third != "tok-2" || fetches.Load() != 2 {
		t.Fatalf("expected refresh, got %q after %d fetches", third, fetches.Load())
	}
}

func TestTokenSourceCachesWhenExpiresInMissing(t *testing.T) {
	t.Parallel()
	// A token endpoint that reports no (or a non-positive) expires_in must not
	// defeat caching: the fallback TTL has to exceed the refresh skew, or the
	// token would be treated as instantly expired and refetched on every call.
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"tok"}`)) // expires_in omitted -> 0
	}))
	defer srv.Close()

	now := time.Unix(2_000_000, 0)
	ts := newTokenSource(srv.Client(), srv.URL, "tv-capture", "s3cr3t")
	ts.now = func() time.Time { return now }

	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("first token: %v", err)
	}
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("second token: %v", err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("expected the token to be cached, got %d fetches", fetches.Load())
	}
}

func TestTokenSourceInvalidateForcesRefetch(t *testing.T) {
	t.Parallel()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := fetches.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"tok-` + string(rune('0'+n)) + `","expires_in":300}`))
	}))
	defer srv.Close()

	ts := newTokenSource(srv.Client(), srv.URL, "tv-capture", "s3cr3t")
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("first token: %v", err)
	}
	// Without Invalidate the cached token would be reused; Invalidate must force a
	// refetch even though the token has not reached its expiry.
	ts.Invalidate()
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("token after invalidate: %v", err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("expected a refetch after Invalidate, got %d fetches", fetches.Load())
	}
}

func TestTokenSourceErrorsOnNon200(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ts := newTokenSource(srv.Client(), srv.URL, "tv-capture", "s3cr3t")
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected error on non-200 token response")
	}
}

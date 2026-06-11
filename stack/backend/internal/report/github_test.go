package report

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newGitHubTestClient(srv *httptest.Server, token string) *GitHubClient {
	return &GitHubClient{httpClient: srv.Client(), baseURL: srv.URL, repo: "verovec/truth-in-stream", token: token}
}

func TestGitHubOpenPRs(t *testing.T) {
	t.Parallel()
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[
			{"number":26,"title":"VER-28","html_url":"https://gh/26","draft":false,"user":{"login":"alice"}},
			{"number":27,"title":"VER-33","html_url":"https://gh/27","draft":true,"user":{"login":"bob"}}
		]`))
	}))
	defer srv.Close()

	prs, err := newGitHubTestClient(srv, "tok").OpenPRs(context.Background())
	if err != nil {
		t.Fatalf("OpenPRs: %v", err)
	}
	if len(prs) != 2 || prs[0].Number != 26 || prs[0].Author != "alice" || prs[1].Draft != true {
		t.Fatalf("parsed PRs wrong: %+v", prs)
	}
	if gotPath != "/repos/verovec/truth-in-stream/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "state=open") {
		t.Errorf("query = %q, want state=open", gotQuery)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
}

func TestGitHubOpenPRsNoToken(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if _, err := newGitHubTestClient(srv, "").OpenPRs(context.Background()); err != nil {
		t.Fatalf("OpenPRs: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization sent without a token: %q", gotAuth)
	}
}

func TestGitHubNon200(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := newGitHubTestClient(srv, "").OpenPRs(context.Background()); err == nil {
		t.Fatal("want error on non-200, got nil")
	}
}

package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubVerifier struct{ err error }

func (s stubVerifier) Verify(string, time.Time) error { return s.err }

func TestAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cookie    *http.Cookie
		verifyErr error
		wantCode  int
	}{
		{
			name:     "valid session passes through",
			cookie:   &http.Cookie{Name: "session", Value: "token"},
			wantCode: http.StatusOK,
		},
		{
			name:     "missing cookie rejected",
			wantCode: http.StatusUnauthorized,
		},
		{
			name:      "invalid session rejected",
			cookie:    &http.Cookie{Name: "session", Value: "bad"},
			verifyErr: errors.New("invalid"),
			wantCode:  http.StatusUnauthorized,
		},
		{
			name:      "wrong cookie name rejected",
			cookie:    &http.Cookie{Name: "other", Value: "token"},
			verifyErr: errors.New("invalid"),
			wantCode:  http.StatusUnauthorized,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			h := Auth(stubVerifier{err: tc.verifyErr}, "session")(next)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/protected", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusUnauthorized {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("401 body is not JSON: %v", err)
				}
				if body["error"] == "" {
					t.Fatal("401 body has no error message")
				}
			}
		})
	}
}

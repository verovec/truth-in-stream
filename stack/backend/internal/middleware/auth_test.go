package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
)

// stubSessions is a SessionVerifier that accepts a single known token and
// rejects everything else, standing in for the legacy HMAC session verifier.
type stubSessions struct{ valid string }

func (s stubSessions) Verify(token string, _ time.Time) error {
	if token == s.valid {
		return nil
	}
	return errors.New("invalid session")
}

func TestSessionOrIdentity(t *testing.T) {
	t.Parallel()
	const goodSession = "good-session"
	adminID := auth.Identity{Subject: "a", Username: "admin", Roles: []string{"admin", "guest"}}

	tests := []struct {
		name        string
		cookie      *http.Cookie
		bearer      string
		verifier    auth.Verifier
		wantCode    int
		wantReached bool
		wantRole    auth.Role
	}{
		{
			name:        "valid session cookie passes as guest, no token needed",
			cookie:      &http.Cookie{Name: "session", Value: goodSession},
			verifier:    fakeVerifier{err: auth.ErrInvalidToken},
			wantCode:    http.StatusOK,
			wantReached: true,
			wantRole:    auth.RoleGuest,
		},
		{
			name:        "valid Keycloak token passes with its role even without a session",
			bearer:      "Bearer good",
			verifier:    fakeVerifier{id: adminID},
			wantCode:    http.StatusOK,
			wantReached: true,
			wantRole:    auth.RoleAdmin,
		},
		{
			name:        "neither credential is rejected with 401",
			verifier:    fakeVerifier{err: auth.ErrInvalidToken},
			wantCode:    http.StatusUnauthorized,
			wantReached: false,
		},
		{
			name:        "invalid session and invalid token is rejected with 401",
			cookie:      &http.Cookie{Name: "session", Value: "bad"},
			bearer:      "Bearer bad",
			verifier:    fakeVerifier{err: auth.ErrInvalidToken},
			wantCode:    http.StatusUnauthorized,
			wantReached: false,
		},
		{
			name:        "invalid session falls back to a valid token",
			cookie:      &http.Cookie{Name: "session", Value: "bad"},
			bearer:      "Bearer good",
			verifier:    fakeVerifier{id: adminID},
			wantCode:    http.StatusOK,
			wantReached: true,
			wantRole:    auth.RoleAdmin,
		},
		{
			name:        "session cookie caller is never admin even if a Keycloak admin token is also present",
			cookie:      &http.Cookie{Name: "session", Value: goodSession},
			bearer:      "Bearer good",
			verifier:    fakeVerifier{id: adminID},
			wantCode:    http.StatusOK,
			wantReached: true,
			wantRole:    auth.RoleGuest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reached := false
			var gotRole auth.Role
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				gotRole = IdentityFrom(r.Context()).Role()
				w.WriteHeader(http.StatusOK)
			})
			h := SessionOrIdentity(stubSessions{valid: goodSession}, "session", tc.verifier)(next)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/protected", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", tc.bearer)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if reached != tc.wantReached {
				t.Fatalf("handler reached = %v, want %v", reached, tc.wantReached)
			}
			if tc.wantReached && gotRole != tc.wantRole {
				t.Fatalf("role = %q, want %q", gotRole, tc.wantRole)
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

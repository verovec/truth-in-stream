package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
)

// fakeVerifier satisfies auth.Verifier with a canned identity or error.
type fakeVerifier struct {
	id  auth.Identity
	err error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (auth.Identity, error) {
	return f.id, f.err
}

func TestIdentityMiddleware(t *testing.T) {
	t.Parallel()
	adminID := auth.Identity{Subject: "a", Username: "admin", Roles: []string{"admin", "guest"}}
	guestID := auth.Identity{Subject: "g", Username: "guest", Roles: []string{"guest"}}

	tests := []struct {
		name       string
		header     string
		verifier   auth.Verifier
		wantStatus int
		wantRole   auth.Role
	}{
		{
			name:       "no header defaults to guest",
			header:     "",
			verifier:   fakeVerifier{},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleGuest,
		},
		{
			name:       "valid admin bearer attaches admin",
			header:     "Bearer good",
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleAdmin,
		},
		{
			name:       "valid guest bearer attaches guest",
			header:     "Bearer good",
			verifier:   fakeVerifier{id: guestID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleGuest,
		},
		{
			name:       "invalid bearer rejected with 401",
			header:     "Bearer bad",
			verifier:   fakeVerifier{err: auth.ErrInvalidToken},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed authorization header rejected",
			header:     "Basic abc",
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "lowercase bearer scheme is accepted",
			header:     "bearer good",
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleAdmin,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotRole auth.Role
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRole = IdentityFrom(r.Context()).Role()
				w.WriteHeader(http.StatusOK)
			})
			h := Identity(tc.verifier)(next)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/x", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && gotRole != tc.wantRole {
				t.Fatalf("role = %q, want %q", gotRole, tc.wantRole)
			}
		})
	}
}

func TestRequireIdentityMiddleware(t *testing.T) {
	t.Parallel()
	adminID := auth.Identity{Subject: "a", Username: "admin", Roles: []string{"admin", "guest"}}
	guestID := auth.Identity{Subject: "g", Username: "guest", Roles: []string{"guest"}}

	tests := []struct {
		name       string
		header     string
		query      string
		wsUpgrade  bool
		verifier   auth.Verifier
		wantStatus int
		wantRole   auth.Role
	}{
		{
			name:       "no credential is rejected with 401",
			verifier:   fakeVerifier{id: guestID},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid admin bearer attaches admin",
			header:     "Bearer good",
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleAdmin,
		},
		{
			name:       "valid guest bearer attaches guest",
			header:     "Bearer good",
			verifier:   fakeVerifier{id: guestID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleGuest,
		},
		{
			name:       "invalid bearer rejected with 401",
			header:     "Bearer bad",
			verifier:   fakeVerifier{err: auth.ErrInvalidToken},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "access_token query param is accepted on a websocket upgrade",
			query:      "access_token=good",
			wsUpgrade:  true,
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleAdmin,
		},
		{
			name:       "invalid access_token query param on a websocket upgrade rejected with 401",
			query:      "access_token=bad",
			wsUpgrade:  true,
			verifier:   fakeVerifier{err: auth.ErrInvalidToken},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty access_token query param is no credential, 401",
			query:      "access_token=",
			wsUpgrade:  true,
			verifier:   fakeVerifier{id: guestID},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "access_token query param is ignored on a non-upgrade request",
			query:      "access_token=good",
			wsUpgrade:  false,
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "bearer header is preferred over query param",
			header:     "Bearer good",
			query:      "access_token=ignored",
			wsUpgrade:  true,
			verifier:   fakeVerifier{id: adminID},
			wantStatus: http.StatusOK,
			wantRole:   auth.RoleAdmin,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotRole auth.Role
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				gotRole = IdentityFrom(r.Context()).Role()
				w.WriteHeader(http.StatusOK)
			})
			h := RequireIdentity(tc.verifier)(next)
			target := "/api/x"
			if tc.query != "" {
				target += "?" + tc.query
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if tc.wsUpgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				if reached {
					t.Fatal("a rejected request must not reach the protected handler")
				}
				return
			}
			if gotRole != tc.wantRole {
				t.Fatalf("role = %q, want %q", gotRole, tc.wantRole)
			}
		})
	}
}

func TestIdentityFromDefaultsGuest(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if id := IdentityFrom(req.Context()); id.IsAdmin() {
		t.Fatal("identity from a context with no value must not be admin")
	}
	if IdentityFrom(req.Context()).Role() != auth.RoleGuest {
		t.Fatal("identity from a context with no value must default to guest")
	}
}

func TestRequireAdminMiddleware(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		id         auth.Identity
		set        bool
		wantStatus int
	}{
		{name: "admin passes", id: auth.Identity{Roles: []string{"admin"}}, set: true, wantStatus: http.StatusOK},
		{name: "guest forbidden", id: auth.Identity{Roles: []string{"guest"}}, set: true, wantStatus: http.StatusForbidden},
		{name: "no identity forbidden", set: false, wantStatus: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
			h := RequireAdmin(next)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/debug/x", nil)
			if tc.set {
				req = req.WithContext(WithIdentity(req.Context(), tc.id))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK && reached {
				t.Fatal("forbidden request reached the protected handler")
			}
		})
	}
}

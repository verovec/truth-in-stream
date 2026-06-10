package service

import (
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestSessionsIssueAndVerify(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sessions := NewSessions(testSecret, time.Hour)

	token, err := sessions.Issue(now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" {
		t.Fatal("issued an empty token")
	}

	tests := []struct {
		name    string
		token   string
		at      time.Time
		wantErr bool
	}{
		{name: "valid token verifies", token: token, at: now.Add(30 * time.Minute)},
		{name: "valid at issue time", token: token, at: now},
		{name: "expired token rejected", token: token, at: now.Add(time.Hour + time.Second), wantErr: true},
		{name: "empty token rejected", token: "", at: now, wantErr: true},
		{name: "token without separator rejected", token: strings.ReplaceAll(token, ".", ""), at: now, wantErr: true},
		{name: "garbage token rejected", token: "not base64!.not base64!", at: now, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := sessions.Verify(tc.token, tc.at)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSessionsRejectsTampering(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sessions := NewSessions(testSecret, time.Hour)

	token, err := sessions.Issue(now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	payload, mac, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token %q has no separator", token)
	}

	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b)
	}

	tests := []struct {
		name  string
		token string
	}{
		{name: "tampered payload", token: flip(payload) + "." + mac},
		{name: "tampered signature", token: payload + "." + flip(mac)},
		{name: "signature from another secret", token: tokenFromOtherSecret(t, now)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := sessions.Verify(tc.token, now); err == nil {
				t.Fatal("expected tampered token to be rejected")
			}
		})
	}
}

func tokenFromOtherSecret(t *testing.T, now time.Time) string {
	t.Helper()
	other := NewSessions("ffffffffffffffffffffffffffffffff", time.Hour)
	token, err := other.Issue(now)
	if err != nil {
		t.Fatalf("issue with other secret: %v", err)
	}
	return token
}

func TestSessionsTokensAreUnique(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sessions := NewSessions(testSecret, time.Hour)

	a, err := sessions.Issue(now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	b, err := sessions.Issue(now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if a == b {
		t.Fatal("two issued tokens are identical; tokens must carry a random component")
	}
}

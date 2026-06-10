package service

import (
	"testing"

	"github.com/alexedwards/argon2id"
)

func TestNewCredentials(t *testing.T) {
	t.Parallel()
	hash, err := argon2id.CreateHash("s3cret", OperatorHashParams)
	if err != nil {
		t.Fatalf("creating fixture hash: %v", err)
	}

	tests := []struct {
		name    string
		hash    string
		wantErr bool
	}{
		{name: "valid encoded hash accepted", hash: hash},
		{name: "malformed hash rejected", hash: "not-a-hash", wantErr: true},
		{name: "empty hash rejected", hash: "", wantErr: true},
		{name: "bcrypt-style hash rejected", hash: "$2a$10$N9qo8uLOickgx2ZMRZoMye", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewCredentials("op@example.com", tc.hash)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCredentialsVerify(t *testing.T) {
	t.Parallel()
	hash, err := argon2id.CreateHash("correct-password", OperatorHashParams)
	if err != nil {
		t.Fatalf("creating fixture hash: %v", err)
	}
	creds, err := NewCredentials("op@example.com", hash)
	if err != nil {
		t.Fatalf("building credentials: %v", err)
	}

	tests := []struct {
		name     string
		email    string
		password string
		want     bool
	}{
		{name: "correct credentials accepted", email: "op@example.com", password: "correct-password", want: true},
		{name: "wrong password rejected", email: "op@example.com", password: "wrong-password"},
		{name: "wrong email rejected", email: "other@example.com", password: "correct-password"},
		{name: "both wrong rejected", email: "other@example.com", password: "wrong-password"},
		{name: "empty password rejected", email: "op@example.com", password: ""},
		{name: "empty email rejected", email: "", password: "correct-password"},
		{name: "email case mismatch rejected", email: "OP@example.com", password: "correct-password"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := creds.Verify(tc.email, tc.password); got != tc.want {
				t.Fatalf("Verify(%q, %q) = %v, want %v", tc.email, tc.password, got, tc.want)
			}
		})
	}
}

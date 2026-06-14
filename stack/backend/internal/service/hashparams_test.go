package service

import (
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"
)

func TestHashOperatorPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashOperatorPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashOperatorPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("output %q is not an encoded argon2id hash", hash)
	}

	match, err := argon2id.ComparePasswordAndHash("correct-horse", hash)
	if err != nil {
		t.Fatalf("comparing against generated hash: %v", err)
	}
	if !match {
		t.Fatal("generated hash does not verify the original password")
	}

	if _, err := NewCredentials("op@example.com", hash); err != nil {
		t.Fatalf("verifier rejected the generated hash: %v", err)
	}
}

func TestHashOperatorPasswordRejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := HashOperatorPassword(""); err == nil {
		t.Fatal("expected an error for an empty password")
	}
}

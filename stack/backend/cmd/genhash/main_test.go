package main

import (
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"
)

func TestGenerate(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := generate(strings.NewReader("s3cret-password\n"), &out); err != nil {
		t.Fatalf("generate: %v", err)
	}
	hash := strings.TrimSpace(out.String())
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("output %q is not an encoded argon2id hash", hash)
	}
	match, err := argon2id.ComparePasswordAndHash("s3cret-password", hash)
	if err != nil {
		t.Fatalf("comparing against generated hash: %v", err)
	}
	if !match {
		t.Fatal("generated hash does not verify the original password")
	}
}

func TestGenerateRejectsEmptyPassword(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := generate(strings.NewReader("\n"), &out); err == nil {
		t.Fatal("expected an error for an empty password")
	}
}

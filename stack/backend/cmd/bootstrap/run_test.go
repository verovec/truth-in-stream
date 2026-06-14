package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"
)

func TestRunCreatesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(exampleEnv), 0o644); err != nil {
		t.Fatalf("writing .env.example: %v", err)
	}
	envPath := filepath.Join(root, ".env")

	// Fresh checkout: no .env yet.
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("expected no .env, stat err = %v", err)
	}
	if err := run(root, inputs{email: "op@example.com", password: "hunter2"}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading generated .env: %v", err)
	}
	first := string(data)

	if strings.Contains(first, "hunter2") {
		t.Fatal("plaintext password leaked into .env")
	}
	hashRaw, _ := envValue(first, "AUTH_PASSWORD_HASH")
	if !strings.HasPrefix(hashRaw, "'") {
		t.Fatalf("hash not single-quoted: %q", hashRaw)
	}
	match, err := argon2id.ComparePasswordAndHash("hunter2", strings.Trim(hashRaw, "'"))
	if err != nil || !match {
		t.Fatalf("hash does not verify password (match=%v err=%v)", match, err)
	}

	// .env must be created with owner-only permissions (it holds the hash).
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".env mode = %o, want 600", perm)
	}

	// Re-running with different credentials must not change the file.
	if err := run(root, inputs{email: "other@example.com", password: "different"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	data, err = os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("re-reading .env: %v", err)
	}
	if string(data) != first {
		t.Fatalf("second run mutated .env:\nfirst:\n%s\nsecond:\n%s", first, string(data))
	}
}

func TestRunMissingExample(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := run(root, inputs{email: "op@example.com", password: "hunter2"}); err == nil {
		t.Fatal("expected an error when .env.example is absent")
	}
}

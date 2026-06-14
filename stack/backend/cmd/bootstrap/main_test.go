package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"
)

// exampleEnv is a trimmed stand-in for the repo .env.example: the three managed
// keys (email and secret empty, hash carrying the shipped placeholder), plus a
// comment and an unrelated key that bootstrap must never touch.
const exampleEnv = `# header comment
TRANSCRIPTION_API_KEY=
EMBEDDING_API_KEY=

AUTH_EMAIL=

# AUTH_PASSWORD_HASH='$argon2id$v=19$m=19456,t=2,p=1$placeholder'
AUTH_PASSWORD_HASH='$argon2id$v=19$m=19456,t=2,p=1$shBQcmQC9qrZBGi0S55dZw$8FQY60gGln/W4hIs42tz7yeJ2WEzZFNOCbIlhNdx0k4'

SESSION_SECRET=
`

func fixedSecret() (string, error) { return "deadbeefcafe", nil }

func TestEnvValue(t *testing.T) {
	t.Parallel()
	// The real assignment is returned, not the commented example line above it.
	got, present := envValue(exampleEnv, "AUTH_PASSWORD_HASH")
	if !present {
		t.Fatal("AUTH_PASSWORD_HASH not found")
	}
	if !strings.HasPrefix(got, "'$argon2id$") {
		t.Fatalf("got %q, want the uncommented assignment value", got)
	}
	if v, present := envValue(exampleEnv, "AUTH_EMAIL"); !present || v != "" {
		t.Fatalf("AUTH_EMAIL = (%q, %v), want (\"\", true)", v, present)
	}
	if _, present := envValue(exampleEnv, "NOPE"); present {
		t.Fatal("NOPE should be absent")
	}

	// A CRLF .env must not read an empty value as "\r".
	if v, present := envValue("AUTH_EMAIL=\r\nSESSION_SECRET=abc\r\n", "AUTH_EMAIL"); !present || v != "" {
		t.Fatalf("CRLF AUTH_EMAIL = (%q,%v), want empty", v, present)
	}
	if v, _ := envValue("SESSION_SECRET=abc\r\n", "SESSION_SECRET"); v != "abc" {
		t.Fatalf("CRLF SESSION_SECRET = %q, want abc", v)
	}
}

func TestEligible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current string
		def     string
		want    bool
	}{
		{name: "empty is eligible", current: "", def: "", want: true},
		{name: "equals template default is eligible", current: "x", def: "x", want: true},
		{name: "user-set differs from default", current: "real", def: "x", want: false},
		{name: "non-empty with empty default", current: "real", def: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := eligible(tc.current, tc.def); got != tc.want {
				t.Fatalf("eligible(%q,%q)=%v want %v", tc.current, tc.def, got, tc.want)
			}
		})
	}
}

func TestSetEnvValuePreservesEverythingElse(t *testing.T) {
	t.Parallel()
	got := setEnvValue(exampleEnv, "AUTH_EMAIL", "op@example.com")
	if !strings.Contains(got, "\nAUTH_EMAIL=op@example.com\n") {
		t.Fatalf("AUTH_EMAIL not set as expected:\n%s", got)
	}
	// Untouched lines survive verbatim.
	for _, want := range []string{"# header comment", "TRANSCRIPTION_API_KEY=", "SESSION_SECRET="} {
		if !strings.Contains(got, want) {
			t.Fatalf("line %q was lost:\n%s", want, got)
		}
	}
	// Only one AUTH_EMAIL assignment exists.
	if n := strings.Count(got, "AUTH_EMAIL="); n != 1 {
		t.Fatalf("AUTH_EMAIL appears %d times, want 1", n)
	}
}

func TestSetEnvValueAppendsWhenMissing(t *testing.T) {
	t.Parallel()
	got := setEnvValue("FOO=bar\n", "AUTH_EMAIL", "op@example.com")
	if got != "FOO=bar\nAUTH_EMAIL=op@example.com\n" {
		t.Fatalf("append result = %q", got)
	}
}

func TestMergeFillsFreshEnv(t *testing.T) {
	t.Parallel()
	res, err := merge(exampleEnv, exampleEnv, inputs{email: "op@example.com", password: "hunter2"}, fixedSecret)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// All three auth secrets are generated on a fresh checkout.
	if got := sortedKeys(res.filled); got != "AUTH_EMAIL,AUTH_PASSWORD_HASH,SESSION_SECRET" {
		t.Fatalf("filled = %s, want all three", got)
	}
	// Both provider keys get a demo placeholder so the offline demo can boot.
	if got := sortedKeys(res.placeholders); got != "EMBEDDING_API_KEY,TRANSCRIPTION_API_KEY" {
		t.Fatalf("placeholders = %s, want both provider keys", got)
	}
	for _, key := range []string{"TRANSCRIPTION_API_KEY", "EMBEDDING_API_KEY"} {
		if v, _ := envValue(res.content, key); v != demoKeyPlaceholder {
			t.Fatalf("%s = %q, want the demo placeholder %q", key, v, demoKeyPlaceholder)
		}
	}
	if len(res.kept) != 0 {
		t.Fatalf("kept = %v, want none", res.kept)
	}

	email, _ := envValue(res.content, "AUTH_EMAIL")
	if email != "op@example.com" {
		t.Fatalf("AUTH_EMAIL = %q", email)
	}
	secret, _ := envValue(res.content, "SESSION_SECRET")
	if secret != "deadbeefcafe" {
		t.Fatalf("SESSION_SECRET = %q", secret)
	}

	// The hash is single-quoted so Compose does not expand its $ signs.
	hashRaw, _ := envValue(res.content, "AUTH_PASSWORD_HASH")
	if !strings.HasPrefix(hashRaw, "'") || !strings.HasSuffix(hashRaw, "'") {
		t.Fatalf("AUTH_PASSWORD_HASH = %q, want single-quoted", hashRaw)
	}
	hash := strings.Trim(hashRaw, "'")
	match, err := argon2id.ComparePasswordAndHash("hunter2", hash)
	if err != nil || !match {
		t.Fatalf("generated hash does not verify the password (match=%v err=%v)", match, err)
	}

	// The plaintext password never lands in the file.
	if strings.Contains(res.content, "hunter2") {
		t.Fatal("plaintext password leaked into .env content")
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	t.Parallel()
	first, err := merge(exampleEnv, exampleEnv, inputs{email: "op@example.com", password: "hunter2"}, fixedSecret)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	// A second run with the filled content keeps every value and changes nothing,
	// even if a different secret/password is offered.
	second, err := merge(first.content, exampleEnv, inputs{email: "other@example.com", password: "different"}, func() (string, error) {
		return "0000", nil
	})
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if second.content != first.content {
		t.Fatalf("second run mutated the file:\nfirst:\n%s\nsecond:\n%s", first.content, second.content)
	}
	if len(second.filled) != 0 {
		t.Fatalf("second run filled %v, want none", second.filled)
	}
	if len(second.placeholders) != 0 {
		t.Fatalf("second run set placeholders %v, want none", second.placeholders)
	}
	if sortedKeys(second.kept) != "AUTH_EMAIL,AUTH_PASSWORD_HASH,EMBEDDING_API_KEY,SESSION_SECRET,TRANSCRIPTION_API_KEY" {
		t.Fatalf("second run kept = %v, want all five managed keys", second.kept)
	}
}

func TestMergeMissingPassword(t *testing.T) {
	t.Parallel()
	_, err := merge(exampleEnv, exampleEnv, inputs{email: "op@example.com"}, fixedSecret)
	if err == nil {
		t.Fatal("expected an error when the hash must be generated but no password is supplied")
	}
}

func TestMergeMissingEmail(t *testing.T) {
	t.Parallel()
	_, err := merge(exampleEnv, exampleEnv, inputs{password: "hunter2"}, fixedSecret)
	if err == nil {
		t.Fatal("expected an error when the email must be set but none is supplied")
	}
}

// TestExampleFileFormatMatches guards against the real .env.example drifting
// away from the line shapes bootstrap relies on (empty email/secret, a single
// quoted hash assignment).
func TestExampleFileFormatMatches(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		t.Fatalf("reading .env.example: %v", err)
	}
	content := string(data)

	for _, key := range []string{"AUTH_EMAIL", "SESSION_SECRET", "TRANSCRIPTION_API_KEY", "EMBEDDING_API_KEY"} {
		if v, present := envValue(content, key); !present || v != "" {
			t.Fatalf("%s in .env.example = (%q,%v), want empty assignment", key, v, present)
		}
	}
	hash, present := envValue(content, "AUTH_PASSWORD_HASH")
	if !present {
		t.Fatal("AUTH_PASSWORD_HASH missing from .env.example")
	}
	if !strings.HasPrefix(hash, "'") || !strings.HasSuffix(hash, "'") {
		t.Fatalf("AUTH_PASSWORD_HASH in .env.example = %q, want single-quoted", hash)
	}
}

func sortedKeys(ks []string) string {
	cp := append([]string(nil), ks...)
	// small, fixed set; insertion sort keeps the test dependency-free
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	return strings.Join(cp, ",")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Anchor on this source file's location rather than the working directory,
	// so the path holds regardless of where `go test` is invoked from.
	// cmd/bootstrap -> backend -> stack -> repo root.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

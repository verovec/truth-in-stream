// Command bootstrap makes a fresh checkout self-serve by generating the
// operator credentials a first `make up` needs. On a clean clone it copies
// .env.example to .env, then fills the three auth secrets that have no safe
// default - AUTH_EMAIL, AUTH_PASSWORD_HASH (an argon2id hash, never the
// plaintext), and SESSION_SECRET (32 random bytes) - writing the hash
// single-quoted so docker-compose does not expand its $ signs.
//
// It also writes a self-describing placeholder for the transcription and
// embedding API keys when they are empty: the backend requires them to be
// present to boot, but the offline demo never calls either provider, so a
// placeholder lets a fresh clone start and play the demo while making clear a
// real key is needed for live analysis.
//
// It is idempotent: a value is only generated while it is still empty or equal
// to the .env.example default, so re-running never clobbers credentials an
// operator has already set. The plaintext password is read from the
// BOOTSTRAP_PASSWORD environment variable and is only ever hashed, never
// written to disk.
//
// Inputs:
//
//	-root             repo root holding .env / .env.example (default ".")
//	-email / $BOOTSTRAP_EMAIL    operator login email
//	$BOOTSTRAP_PASSWORD          operator password (hashed, never stored)
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

const (
	keyEmail  = "AUTH_EMAIL"
	keyHash   = "AUTH_PASSWORD_HASH"
	keySecret = "SESSION_SECRET"
)

// providerKeys are the transcription and embedding API keys the backend
// requires to be present at boot. The offline demo never calls either provider
// (its results are precomputed and seeded), but the stack will not start with
// them empty, so bootstrap writes a clearly-labeled placeholder to let a fresh
// clone boot and play the demo. Live analysis needs real keys in their place.
var providerKeys = []string{"TRANSCRIPTION_API_KEY", "EMBEDDING_API_KEY"}

// demoKeyPlaceholder is the value bootstrap writes for an empty provider key:
// non-empty so the backend boots, and self-describing so an operator sees it is
// not a real credential.
const demoKeyPlaceholder = "set-a-real-key-for-live-analysis"

func main() {
	root := flag.String("root", ".", "repository root containing .env and .env.example")
	email := flag.String("email", os.Getenv("BOOTSTRAP_EMAIL"), "operator login email (or set BOOTSTRAP_EMAIL)")
	flag.Parse()

	if err := run(*root, inputs{email: *email, password: os.Getenv("BOOTSTRAP_PASSWORD")}); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err)
		os.Exit(1)
	}
}

// inputs carries the operator-supplied values bootstrap may need. password is
// hashed and discarded; it is never written to .env.
type inputs struct {
	email    string
	password string
}

// run loads .env (copying .env.example when absent), fills the eligible managed
// keys, and writes the result back atomically with 0600 permissions.
func run(root string, in inputs) error {
	examplePath := filepath.Join(root, ".env.example")
	envPath := filepath.Join(root, ".env")

	exampleData, err := os.ReadFile(examplePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", examplePath, err)
	}
	example := string(exampleData)

	current := example
	created := false
	if data, err := os.ReadFile(envPath); err == nil {
		current = string(data)
	} else if errors.Is(err, os.ErrNotExist) {
		created = true
	} else {
		return fmt.Errorf("reading %s: %w", envPath, err)
	}

	res, err := merge(current, example, in, generateSecret)
	if err != nil {
		return err
	}

	if err := writeFileAtomic(envPath, []byte(res.content), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", envPath, err)
	}

	if created {
		fmt.Fprintf(os.Stderr, "created .env from .env.example\n")
	}
	if len(res.filled) > 0 {
		fmt.Fprintf(os.Stderr, "generated: %s\n", strings.Join(res.filled, ", "))
	}
	if len(res.placeholders) > 0 {
		fmt.Fprintf(os.Stderr, "set demo placeholder (no real key needed for the offline demo; set real keys for live analysis): %s\n", strings.Join(res.placeholders, ", "))
	}
	if len(res.kept) > 0 {
		fmt.Fprintf(os.Stderr, "kept existing: %s\n", strings.Join(res.kept, ", "))
	}
	fmt.Fprintf(os.Stderr, ".env is ready; run 'make up' next.\n")
	return nil
}

// mergeResult is the rewritten file plus a record of which managed keys were
// generated this run, set to a demo placeholder, or left as already-configured.
type mergeResult struct {
	content      string
	filled       []string
	placeholders []string
	kept         []string
}

// merge fills the managed auth keys in current that are still eligible (empty
// or equal to the .env.example default), drawing on the supplied inputs and a
// secret generator. It returns an error if a key must be generated but the
// required operator input is missing. It never alters any other line.
func merge(current, example string, in inputs, secret func() (string, error)) (mergeResult, error) {
	content := current
	var filled, placeholders, kept []string

	emailDefault, _ := envValue(example, keyEmail)
	hashDefault, _ := envValue(example, keyHash)
	secretDefault, _ := envValue(example, keySecret)

	emailCur, _ := envValue(content, keyEmail)
	if eligible(emailCur, emailDefault) {
		if in.email == "" {
			return mergeResult{}, errors.New("AUTH_EMAIL is unset and no operator email was supplied (set BOOTSTRAP_EMAIL or pass -email)")
		}
		content = setEnvValue(content, keyEmail, in.email)
		filled = append(filled, keyEmail)
	} else {
		kept = append(kept, keyEmail)
	}

	hashCur, _ := envValue(content, keyHash)
	if eligible(hashCur, hashDefault) {
		if in.password == "" {
			return mergeResult{}, errors.New("AUTH_PASSWORD_HASH is unset and no operator password was supplied (set BOOTSTRAP_PASSWORD)")
		}
		hash, err := service.HashOperatorPassword(in.password)
		if err != nil {
			return mergeResult{}, err
		}
		content = setEnvValue(content, keyHash, "'"+hash+"'")
		filled = append(filled, keyHash)
	} else {
		kept = append(kept, keyHash)
	}

	secretCur, _ := envValue(content, keySecret)
	if eligible(secretCur, secretDefault) {
		value, err := secret()
		if err != nil {
			return mergeResult{}, fmt.Errorf("generating session secret: %w", err)
		}
		content = setEnvValue(content, keySecret, value)
		filled = append(filled, keySecret)
	} else {
		kept = append(kept, keySecret)
	}

	for _, key := range providerKeys {
		def, _ := envValue(example, key)
		cur, _ := envValue(content, key)
		if eligible(cur, def) {
			content = setEnvValue(content, key, demoKeyPlaceholder)
			placeholders = append(placeholders, key)
		} else {
			kept = append(kept, key)
		}
	}

	sort.Strings(filled)
	sort.Strings(placeholders)
	sort.Strings(kept)
	return mergeResult{content: content, filled: filled, placeholders: placeholders, kept: kept}, nil
}

// eligible reports whether a managed key should be generated: its current value
// is empty or still identical to the .env.example default, meaning no operator
// has configured it yet.
func eligible(current, templateDefault string) bool {
	return current == "" || current == templateDefault
}

// envValue returns the raw value (everything after the first '=') of the first
// real KEY= assignment line, ignoring leading whitespace and commented lines. A
// trailing carriage return is stripped so a CRLF .env does not read an "empty"
// value as "\r" (which would defeat the eligibility check).
func envValue(content, key string) (string, bool) {
	prefix := key + "="
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSuffix(trimmed[len(prefix):], "\r"), true
		}
	}
	return "", false
}

// setEnvValue replaces the first KEY= assignment's value with rawValue, which
// the caller has already formatted and quoted. Every other line is preserved
// verbatim. When no assignment exists the key is appended with a single
// trailing newline.
func setEnvValue(content, key, rawValue string) string {
	prefix := key + "="
	replacement := prefix + rawValue
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), prefix) {
			lines[i] = replacement
			return strings.Join(lines, "\n")
		}
	}
	if content == "" {
		return replacement + "\n"
	}
	if strings.HasSuffix(content, "\n") {
		lines[len(lines)-1] = replacement
		return strings.Join(lines, "\n") + "\n"
	}
	return content + "\n" + replacement + "\n"
}

// generateSecret returns 32 cryptographically random bytes as a 64-char hex
// string, suitable for SESSION_SECRET.
func generateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// writeFileAtomic writes data to a sibling temp file and renames it over path,
// so a reader never sees a half-written .env.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".env.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

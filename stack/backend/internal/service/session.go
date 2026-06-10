package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSession marks a session token that is missing, malformed,
// tampered with, or expired. Callers must not distinguish between those
// cases; they all mean "not signed in".
var ErrInvalidSession = errors.New("invalid session")

// Sessions issues and verifies stateless HMAC-signed session tokens. A token
// is "<expiry-unix>.<base64url nonce>.<base64url HMAC-SHA256>", signed over
// the first two parts; with a single operator there is no session state to
// store server-side, so possession of an untampered, unexpired token is the
// whole proof.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// NewSessions builds a session manager signing with secret and issuing
// tokens valid for ttl.
func NewSessions(secret string, ttl time.Duration) *Sessions {
	return &Sessions{secret: []byte(secret), ttl: ttl}
}

// TTL is the lifetime of issued tokens; the cookie carrying a token must not
// outlive it.
func (s *Sessions) TTL() time.Duration {
	return s.ttl
}

// Issue creates a signed token that expires ttl after now. The random nonce
// makes every token unique so issuing twice never yields the same value.
func (s *Sessions) Issue(now time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating session nonce: %w", err)
	}
	payload := strconv.FormatInt(now.Add(s.ttl).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	return payload + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload)), nil
}

// Verify checks that token carries a valid signature and has not expired at
// instant now. Any failure is ErrInvalidSession; the reason is deliberately
// not exposed.
func (s *Sessions) Verify(token string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrInvalidSession
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ErrInvalidSession
	}
	if !hmac.Equal(mac, s.sign(parts[0]+"."+parts[1])) {
		return ErrInvalidSession
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return ErrInvalidSession
	}
	if now.Unix() > exp {
		return ErrInvalidSession
	}
	return nil
}

func (s *Sessions) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"

	"github.com/alexedwards/argon2id"
)

// Credentials verifies the single operator credential pair against the
// env-provisioned argon2id hash. There is exactly one user, so there is no
// user store; the email and hash are fixed at construction.
type Credentials struct {
	emailDigest  [sha256.Size]byte
	passwordHash string
}

// NewCredentials builds a verifier for the operator identified by email and
// the encoded argon2id hash of their password. A hash that does not parse is
// a fatal misconfiguration surfaced at wiring time, not at first login.
func NewCredentials(email, passwordHash string) (*Credentials, error) {
	if _, _, _, err := argon2id.DecodeHash(passwordHash); err != nil {
		return nil, fmt.Errorf("parsing operator password hash: %w", err)
	}
	return &Credentials{
		emailDigest:  sha256.Sum256([]byte(email)),
		passwordHash: passwordHash,
	}, nil
}

// Verify reports whether the submitted email and password match the operator
// credential. Both checks always run so a wrong email costs the same time as
// a wrong password; the email compares as fixed-size digests so length leaks
// nothing either.
func (c *Credentials) Verify(email, password string) bool {
	digest := sha256.Sum256([]byte(email))
	emailOK := subtle.ConstantTimeCompare(digest[:], c.emailDigest[:]) == 1
	passwordOK, err := argon2id.ComparePasswordAndHash(password, c.passwordHash)
	if err != nil {
		return false
	}
	return emailOK && passwordOK
}

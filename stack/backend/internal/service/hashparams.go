package service

import (
	"errors"
	"fmt"

	"github.com/alexedwards/argon2id"
)

// OperatorHashParams is the argon2id parameter set for the operator password
// hash, following the OWASP password-storage recommendation (verified
// 2026-06): m=19456 KiB, t=2, p=1, 16-byte salt, 32-byte key. cmd/genhash
// generates with it and tests build fixtures from it; the verifier itself
// accepts any encoded argon2id hash, so changing it never invalidates
// existing credentials.
var OperatorHashParams = &argon2id.Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// HashOperatorPassword returns the encoded argon2id hash of the operator
// password under OperatorHashParams. It is the single place that turns a
// plaintext operator password into a storable hash, shared by cmd/genhash and
// cmd/bootstrap so both produce credentials the verifier accepts.
func HashOperatorPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	hash, err := argon2id.CreateHash(password, OperatorHashParams)
	if err != nil {
		return "", fmt.Errorf("hashing operator password: %w", err)
	}
	return hash, nil
}

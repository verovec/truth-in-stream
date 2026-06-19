package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "http://localhost:8081/realms/truth-in-stream"
	testClientID = "truth-in-stream-web"
	testKID      = "test-key-1"
)

// signingKey is the RSA key the test JWKS publishes and the test tokens are
// signed with. A second key models a rotated/foreign signer.
type signingKey struct {
	priv *rsa.PrivateKey
	kid  string
}

func newSigningKey(t *testing.T, kid string) signingKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	return signingKey{priv: priv, kid: kid}
}

// jwksJSON builds a JWK Set document advertising the public halves of the given
// signing keys, the same shape Keycloak serves at /certs.
func jwksJSON(t *testing.T, keys ...signingKey) json.RawMessage {
	t.Helper()
	type jwk struct {
		Kty string `json:"kty"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	set := struct {
		Keys []jwk `json:"keys"`
	}{}
	for _, k := range keys {
		pub := k.priv.Public().(*rsa.PublicKey)
		eBytes := big.NewInt(int64(pub.E)).Bytes()
		set.Keys = append(set.Keys, jwk{
			Kty: "RSA",
			Use: "sig",
			Alg: "RS256",
			Kid: k.kid,
			N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(eBytes),
		})
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshaling JWKS: %v", err)
	}
	return raw
}

type tokenClaims struct {
	issuer string
	azp    string
	aud    any
	roles  []string
	sub    string
	user   string
	expiry time.Time
	noExp  bool
}

func signToken(t *testing.T, key signingKey, c tokenClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": c.issuer,
		"sub": c.sub,
		"azp": c.azp,
	}
	if c.aud != nil {
		claims["aud"] = c.aud
	}
	if c.user != "" {
		claims["preferred_username"] = c.user
	}
	if c.roles != nil {
		claims["realm_access"] = map[string]any{"roles": c.roles}
	}
	if !c.noExp {
		claims["exp"] = jwt.NewNumericDate(c.expiry)
		claims["iat"] = jwt.NewNumericDate(c.expiry.Add(-time.Minute))
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = key.kid
	signed, err := tok.SignedString(key.priv)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

func newTestVerifier(t *testing.T, keys ...signingKey) *KeycloakVerifier {
	t.Helper()
	kf, err := keyfunc.NewJWKSetJSON(jwksJSON(t, keys...))
	if err != nil {
		t.Fatalf("building keyfunc: %v", err)
	}
	v, err := NewVerifier(kf, Config{Issuer: testIssuer, ClientID: testClientID})
	if err != nil {
		t.Fatalf("building verifier: %v", err)
	}
	return v
}

func TestVerify(t *testing.T) {
	t.Parallel()
	signer := newSigningKey(t, testKID)
	foreign := newSigningKey(t, "foreign-key")

	base := func() tokenClaims {
		return tokenClaims{
			issuer: testIssuer,
			azp:    testClientID,
			aud:    "account",
			roles:  []string{"guest"},
			sub:    "user-uuid",
			user:   "guest",
			expiry: time.Now().Add(time.Hour),
		}
	}

	tests := []struct {
		name      string
		token     func() string
		wantErr   error
		wantRole  Role
		wantAdmin bool
	}{
		{
			name:     "valid guest token",
			token:    func() string { return signToken(t, signer, base()) },
			wantRole: RoleGuest,
		},
		{
			name: "valid admin token",
			token: func() string {
				c := base()
				c.roles = []string{"admin", "guest"}
				c.user = "admin"
				return signToken(t, signer, c)
			},
			wantRole:  RoleAdmin,
			wantAdmin: true,
		},
		{
			name: "expired token rejected",
			token: func() string {
				c := base()
				c.expiry = time.Now().Add(-time.Minute)
				return signToken(t, signer, c)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "missing expiry rejected",
			token: func() string {
				c := base()
				c.noExp = true
				return signToken(t, signer, c)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "wrong issuer rejected",
			token: func() string {
				c := base()
				c.issuer = "http://evil.example/realms/truth-in-stream"
				return signToken(t, signer, c)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "wrong authorized party rejected",
			token: func() string {
				c := base()
				c.azp = "some-other-client"
				return signToken(t, signer, c)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name:    "foreign signing key rejected",
			token:   func() string { return signToken(t, foreign, base()) },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "garbage token rejected",
			token:   func() string { return "not-a-jwt" },
			wantErr: ErrInvalidToken,
		},
		{
			name: "no roles defaults to guest",
			token: func() string {
				c := base()
				c.roles = []string{}
				return signToken(t, signer, c)
			},
			wantRole: RoleGuest,
		},
		{
			name: "unrecognized roles default to guest",
			token: func() string {
				c := base()
				c.roles = []string{"some-other-role"}
				return signToken(t, signer, c)
			},
			wantRole: RoleGuest,
		},
	}

	v := newTestVerifier(t, signer)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := v.Verify(t.Context(), tc.token())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Verify error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify unexpected error: %v", err)
			}
			if got := id.Role(); got != tc.wantRole {
				t.Fatalf("Role() = %q, want %q", got, tc.wantRole)
			}
			if got := id.IsAdmin(); got != tc.wantAdmin {
				t.Fatalf("IsAdmin() = %v, want %v", got, tc.wantAdmin)
			}
		})
	}
}

func TestVerifyExtractsIdentity(t *testing.T) {
	t.Parallel()
	signer := newSigningKey(t, testKID)
	v := newTestVerifier(t, signer)
	token := signToken(t, signer, tokenClaims{
		issuer: testIssuer,
		azp:    testClientID,
		aud:    "account",
		roles:  []string{"admin", "guest"},
		sub:    "the-subject",
		user:   "alice",
		expiry: time.Now().Add(time.Hour),
	})
	id, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "the-subject" {
		t.Errorf("Subject = %q, want %q", id.Subject, "the-subject")
	}
	if id.Username != "alice" {
		t.Errorf("Username = %q, want %q", id.Username, "alice")
	}
	if !id.HasRole("admin") || !id.HasRole("guest") {
		t.Errorf("HasRole missing expected roles: %v", id.Roles)
	}
	if id.HasRole("nonexistent") {
		t.Errorf("HasRole reported a role not present")
	}
}

func TestRequireAdmin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      Identity
		wantErr error
	}{
		{name: "admin allowed", id: Identity{Roles: []string{"admin"}}},
		{name: "admin with guest allowed", id: Identity{Roles: []string{"guest", "admin"}}},
		{name: "guest forbidden", id: Identity{Roles: []string{"guest"}}, wantErr: ErrForbidden},
		{name: "anonymous forbidden", id: Identity{}, wantErr: ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := RequireAdmin(tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RequireAdmin = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestGuestIdentityIsNotAdmin(t *testing.T) {
	t.Parallel()
	if GuestIdentity().IsAdmin() {
		t.Fatal("GuestIdentity must never be admin")
	}
	if GuestIdentity().Role() != RoleGuest {
		t.Fatalf("GuestIdentity role = %q, want %q", GuestIdentity().Role(), RoleGuest)
	}
}

func TestVerifyAcceptsECDSASignedToken(t *testing.T) {
	t.Parallel()
	// A realm configured to sign with ES256 rather than the RS256 default must
	// still validate, so the asymmetric allow-list is not silently RSA-only.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating ECDSA key: %v", err)
	}
	pub := priv.Public().(*ecdsa.PublicKey)
	// Bytes() returns the uncompressed point 0x04 || X || Y; split it into the
	// 32-byte coordinates the JWK x/y fields carry, without touching the
	// deprecated big.Int coordinate accessors.
	point, err := pub.Bytes()
	if err != nil {
		t.Fatalf("encoding EC public key: %v", err)
	}
	const coordLen = 32
	x, y := point[1:1+coordLen], point[1+coordLen:]
	jwks := struct {
		Keys []map[string]string `json:"keys"`
	}{Keys: []map[string]string{{
		"kty": "EC",
		"use": "sig",
		"alg": "ES256",
		"crv": "P-256",
		"kid": "ec-key",
		"x":   base64.RawURLEncoding.EncodeToString(x),
		"y":   base64.RawURLEncoding.EncodeToString(y),
	}}}
	raw, err := json.Marshal(jwks)
	if err != nil {
		t.Fatalf("marshaling EC JWKS: %v", err)
	}
	kf, err := keyfunc.NewJWKSetJSON(raw)
	if err != nil {
		t.Fatalf("building keyfunc: %v", err)
	}
	v, err := NewVerifier(kf, Config{Issuer: testIssuer, ClientID: testClientID})
	if err != nil {
		t.Fatalf("building verifier: %v", err)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":          testIssuer,
		"sub":          "ec-subject",
		"azp":          testClientID,
		"exp":          jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"realm_access": map[string]any{"roles": []string{"admin", "guest"}},
	})
	tok.Header["kid"] = "ec-key"
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("signing ES256 token: %v", err)
	}
	id, err := v.Verify(t.Context(), signed)
	if err != nil {
		t.Fatalf("Verify ES256 token: %v", err)
	}
	if !id.IsAdmin() {
		t.Fatalf("expected admin from ES256 token, got roles %v", id.Roles)
	}
}

func TestDenyVerifierRejectsEverything(t *testing.T) {
	t.Parallel()
	if _, err := (DenyVerifier{}).Verify(t.Context(), "anything"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("DenyVerifier.Verify = %v, want ErrInvalidToken", err)
	}
}

func TestNewVerifierValidatesConfig(t *testing.T) {
	t.Parallel()
	signer := newSigningKey(t, testKID)
	kf, err := keyfunc.NewJWKSetJSON(jwksJSON(t, signer))
	if err != nil {
		t.Fatalf("keyfunc: %v", err)
	}
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing issuer", cfg: Config{ClientID: testClientID}},
		{name: "missing client id", cfg: Config{Issuer: testIssuer}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewVerifier(kf, tc.cfg); err == nil {
				t.Fatal("NewVerifier accepted an invalid config")
			}
		})
	}
}

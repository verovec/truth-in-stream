// Package auth validates Keycloak OIDC access tokens against a cached JWKS and
// derives the caller's realm role. It is pure authentication and authorization
// logic with no HTTP types: the handler layer owns the request and the middleware
// that calls into here. The Verifier checks the token signature against the
// rotating JWK set, the issuer, and the authorized party, then extracts the
// realm_access roles; RequireAdmin is the server-side authorization gate that no
// client-supplied flag can satisfy.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Role is a coarse caller role derived from the realm roles on a validated
// token. Only two roles drive backend authorization: admin (debug tools and
// admin-gated routes) and guest (the default for everyone else, including
// callers with no valid token).
type Role string

const (
	// RoleAdmin is granted only to a caller whose validated token carries the
	// realm admin role. It is the single gate for every debug behavior.
	RoleAdmin Role = "admin"
	// RoleGuest is the default role for any caller without a verified admin
	// claim, mirroring the realm's default role.
	RoleGuest Role = "guest"
)

// realmRoleAdmin is the Keycloak realm role name that maps to RoleAdmin. It is
// the realm-side string, kept distinct from the derived Role so a realm rename
// is a one-line change here.
const realmRoleAdmin = "admin"

// realmRoleTVCapture is the Keycloak realm role carried by the tvcapture worker's
// service account. It is deliberately narrower than admin: it authorizes only the
// TV capture write-path (the feed socket and the recording archive endpoints), so
// a leaked worker credential cannot reach unrelated admin routes.
const realmRoleTVCapture = "tv-capture"

// ErrInvalidToken is returned when a token fails signature, issuer, authorized
// party, or expiry validation. The reason is wrapped for logs but the sentinel
// is what callers match on; the HTTP layer renders every case as an
// indistinguishable 401 so a probe learns nothing from the response.
var ErrInvalidToken = errors.New("auth: invalid token")

// ErrForbidden is returned by RequireAdmin when a validated identity lacks the
// admin role. The HTTP layer renders it as 403.
var ErrForbidden = errors.New("auth: forbidden")

// Identity is the validated caller extracted from a Keycloak access token: the
// subject, the preferred username (for logs), and the realm roles. A zero
// Identity is an anonymous guest, never an admin, so a missing or unverifiable
// token degrades to guest rather than to a privileged default.
type Identity struct {
	Subject  string
	Username string
	Roles    []string
}

// GuestIdentity is the identity attributed to a caller with no verified admin
// claim. It is the explicit default the middleware attaches so handlers always
// read a concrete role.
func GuestIdentity() Identity {
	return Identity{Roles: []string{string(RoleGuest)}}
}

// HasRole reports whether the validated identity carries the given realm role.
func (i Identity) HasRole(role string) bool {
	return slices.Contains(i.Roles, role)
}

// IsAdmin reports whether the identity carries the realm admin role. This is the
// only thing any debug gate may consult; it cannot be set by a client.
func (i Identity) IsAdmin() bool {
	return i.HasRole(realmRoleAdmin)
}

// Role collapses the realm roles to the coarse backend role: admin when the
// admin realm role is present, guest otherwise.
func (i Identity) Role() Role {
	if i.IsAdmin() {
		return RoleAdmin
	}
	return RoleGuest
}

// RequireAdmin is the authorization helper gating admin-only behavior. It
// returns ErrForbidden for any identity without the verified admin role, and nil
// for an admin. Authorization lives here, in the service layer, so the gate is
// testable without an HTTP request and cannot drift between call sites.
func RequireAdmin(id Identity) error {
	if !id.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// RequireCaptureService gates the TV capture write-path (the publisher feed
// socket and the recording archive endpoints). It admits an operator (the admin
// role) or the dedicated tvcapture service account (the tv-capture role), and
// rejects everyone else with ErrForbidden. Scoping these routes to their own
// role - rather than reusing the blanket admin gate - keeps a compromised worker
// credential confined to capture, unable to reach unrelated admin routes.
func RequireCaptureService(id Identity) error {
	if id.IsAdmin() || id.HasRole(realmRoleTVCapture) {
		return nil
	}
	return ErrForbidden
}

// Config holds the Keycloak validation parameters. Issuer is the exact issuer
// string the realm advertises (and that tokens carry in iss); ClientID is the
// authorized party (azp) the public web client sets. Both are required.
// AdditionalClientIDs is an optional allow-list of extra authorized parties the
// verifier also accepts: service-account clients (Keycloak client-credentials
// grants) carry their own azp, not the web client's, so the tvcapture worker's
// token would otherwise be rejected. Every entry is a full client identifier the
// realm issues; an empty list keeps the verifier single-client.
type Config struct {
	Issuer              string
	ClientID            string
	AdditionalClientIDs []string
}

// Verifier validates a raw access token and returns the caller identity.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Identity, error)
}

// DenyVerifier rejects every token with ErrInvalidToken. It is the fail-closed
// default substituted when no real verifier is wired, so a misconfiguration
// degrades to "every caller is a guest, nobody is admin" rather than panicking
// on a nil verifier or, worse, skipping validation.
type DenyVerifier struct{}

// Verify always reports the token as invalid.
func (DenyVerifier) Verify(context.Context, string) (Identity, error) {
	return Identity{}, ErrInvalidToken
}

// asymmetricMethods is the allow-list of JWT signing algorithms the verifier
// accepts. It is restricted to the asymmetric families Keycloak can issue (RSA,
// RSA-PSS, ECDSA) so a token signed with a symmetric "alg" (HS*) or "none" can
// never be accepted against the public JWKS - the classic algorithm-confusion
// bypass. It spans every asymmetric algorithm a realm might be configured with,
// so a realm that signs with ES256 or PS256 rather than the RS256 default is not
// silently locked out.
var asymmetricMethods = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
}

// KeycloakVerifier validates Keycloak access tokens against a cached JWK set.
// The keyfunc owns JWKS fetching, caching, and rotation; this type adds the
// Keycloak-specific issuer and authorized-party checks and the role extraction.
type KeycloakVerifier struct {
	keys      keyfunc.Keyfunc
	issuer    string
	clientIDs map[string]struct{}
}

// keycloakClaims are the access-token claims this service consults. The
// embedded RegisteredClaims give the parser exp and iss validation; azp and
// realm_access are Keycloak-specific.
type keycloakClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty   string `json:"azp"`
	PreferredUsername string `json:"preferred_username"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// NewVerifier builds a KeycloakVerifier over the supplied keyfunc. The keyfunc
// carries the JWK set (a live, refreshing remote set in production; a static set
// in tests), so this constructor stays transport-free and the caller owns the
// JWKS lifecycle. Issuer and ClientID are required: validating against an empty
// issuer or authorized party would accept tokens from anywhere.
func NewVerifier(kf keyfunc.Keyfunc, cfg Config) (*KeycloakVerifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth: issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("auth: client id is required")
	}
	clientIDs := map[string]struct{}{cfg.ClientID: {}}
	for _, id := range cfg.AdditionalClientIDs {
		if id != "" {
			clientIDs[id] = struct{}{}
		}
	}
	return &KeycloakVerifier{keys: kf, issuer: cfg.Issuer, clientIDs: clientIDs}, nil
}

// Verify parses and validates the token: the signature against the cached JWKS,
// the issuer, a mandatory expiry, and the authorized party. The signing method
// is constrained to the asymmetric families, so a token forged with a symmetric
// alg or "none" cannot pass against the public keys. Keycloak access tokens set
// aud to "account" (or a resource-server list) rather than this client, so azp
// is the trustworthy single-client check, not aud; if a resource-server audience
// is ever added in Keycloak this is where a jwt.WithAudience check would join.
// The request context is threaded into the keyfunc so a JWKS cache-miss fetch
// honors the caller's deadline and cancellation. On success it returns the caller
// identity with the realm roles; on any failure it returns ErrInvalidToken with
// the underlying reason wrapped for logs.
func (v *KeycloakVerifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	claims := &keycloakClaims{}
	token, err := jwt.ParseWithClaims(
		rawToken, claims, v.keys.KeyfuncCtx(ctx),
		jwt.WithValidMethods(asymmetricMethods),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if !token.Valid {
		return Identity{}, ErrInvalidToken
	}
	if _, ok := v.clientIDs[claims.AuthorizedParty]; !ok {
		return Identity{}, fmt.Errorf("%w: azp %q is not an accepted client", ErrInvalidToken, claims.AuthorizedParty)
	}
	return Identity{
		Subject:  claims.Subject,
		Username: claims.PreferredUsername,
		Roles:    claims.RealmAccess.Roles,
	}, nil
}

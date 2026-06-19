// Pure access-token helpers. The frontend never verifies the Keycloak token
// signature - the Go backend is the cryptographic authority on every /api call
// and rejects a forged or expired token with 401. The frontend only decodes the
// unverified payload to learn the caller's role (to reveal the admin-only debug
// surface) and the expiry (to decide when to refresh). A forged role here only
// reveals a UI toggle whose backing endpoints the backend still gates, so the
// decode is intentionally unverified.

// Role is the coarse caller role the UI gates on, mirroring the backend's two
// realm roles: admin reveals the debug surface, guest is everyone else.
export type Role = "admin" | "guest";

// REALM_ROLE_ADMIN is the Keycloak realm role name that maps to the admin Role.
// It matches the backend's realmRoleAdmin so the two surfaces agree on who is an
// admin.
const REALM_ROLE_ADMIN = "admin";

// AccessTokenClaims are the access-token fields the frontend consults: the realm
// roles for the role gate and the expiry for refresh timing. Everything else in
// the token is ignored.
export interface AccessTokenClaims {
  exp?: number;
  realm_access?: { roles?: string[] };
}

// decodeAccessToken parses the unverified JWT payload, returning null for any
// token that is not a well-formed three-segment JWT with a JSON payload. It does
// not validate the signature: the backend does that.
export function decodeAccessToken(token: string): AccessTokenClaims | null {
  const segments = token.split(".");
  if (segments.length !== 3) {
    return null;
  }
  try {
    const payload = base64UrlDecode(segments[1]);
    const parsed: unknown = JSON.parse(payload);
    if (typeof parsed !== "object" || parsed === null) {
      return null;
    }
    return parsed as AccessTokenClaims;
  } catch {
    return null;
  }
}

// roleFromClaims collapses the realm roles to the coarse UI role: admin when the
// admin realm role is present, guest otherwise. A null or roleless token is a
// guest, so a malformed token never reveals the admin surface.
export function roleFromClaims(claims: AccessTokenClaims | null): Role {
  const roles = claims?.realm_access?.roles ?? [];
  return roles.includes(REALM_ROLE_ADMIN) ? "admin" : "guest";
}

// roleFromToken decodes the token and derives the role in one step, defaulting
// to guest for any token that does not decode.
export function roleFromToken(token: string): Role {
  return roleFromClaims(decodeAccessToken(token));
}

// isExpired reports whether the token's exp is at or within bufferSeconds of now,
// so a caller refreshes slightly ahead of the hard expiry rather than racing it.
// A token with no exp or one that does not decode is treated as expired, failing
// closed to a refresh attempt rather than forwarding a dead token.
export function isExpired(
  token: string,
  nowSeconds: number = Date.now() / 1000,
  bufferSeconds = 30,
): boolean {
  const exp = decodeAccessToken(token)?.exp;
  if (typeof exp !== "number") {
    return true;
  }
  return nowSeconds + bufferSeconds >= exp;
}

// MIN_ACCESS_COOKIE_SECONDS is the floor for the access-token cookie's lifetime.
// A cookie written with maxAge 0 is deleted immediately, which - if the server
// clock runs even slightly ahead of the token's exp - would drop the just-set
// session cookie and bounce the user straight back to login. Flooring the lifetime
// keeps the cookie present long enough for the page to load and a refresh to run.
export const MIN_ACCESS_COOKIE_SECONDS = 30;

// secondsUntilExpiry returns how many whole seconds remain before exp, floored at
// MIN_ACCESS_COOKIE_SECONDS, for use as a cookie max-age so the access-token cookie
// never outlives the token it carries yet is never written already-deleted. A
// token with no decodable exp gets a short conservative lifetime so a refresh is
// attempted soon.
export function secondsUntilExpiry(
  token: string,
  nowSeconds: number = Date.now() / 1000,
  fallbackSeconds = 60,
): number {
  const exp = decodeAccessToken(token)?.exp;
  if (typeof exp !== "number") {
    return fallbackSeconds;
  }
  return Math.max(MIN_ACCESS_COOKIE_SECONDS, Math.floor(exp - nowSeconds));
}

function base64UrlDecode(segment: string): string {
  const normalized = segment.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(
    normalized.length + ((4 - (normalized.length % 4)) % 4),
    "=",
  );
  if (typeof atob === "function") {
    return decodeURIComponent(
      atob(padded)
        .split("")
        .map((c) => "%" + c.charCodeAt(0).toString(16).padStart(2, "0"))
        .join(""),
    );
  }
  return Buffer.from(padded, "base64").toString("utf8");
}

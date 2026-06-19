// Names and options for the auth cookies. The session cookies are httpOnly so
// the Keycloak tokens never reach client JavaScript: the role surface reads them
// server-side and forwards the access token to the backend as a Bearer header.
// All four names live here so a rename is one edit and the proxy gate, the route
// handlers, and the session reader cannot drift apart.

// ACCESS_TOKEN_COOKIE holds the Keycloak access token forwarded to the backend
// as the Bearer credential and decoded server-side for the caller's role.
export const ACCESS_TOKEN_COOKIE = "kc_access";
// REFRESH_TOKEN_COOKIE holds the Keycloak refresh token used to mint a fresh
// access token without a new interactive login.
export const REFRESH_TOKEN_COOKIE = "kc_refresh";
// ID_TOKEN_COOKIE holds the Keycloak id token, used only as the logout hint.
export const ID_TOKEN_COOKIE = "kc_id";
// PKCE_VERIFIER_COOKIE and OAUTH_STATE_COOKIE are the short-lived transaction
// cookies that carry the PKCE verifier and CSRF state across the redirect to
// Keycloak and back to the callback.
export const PKCE_VERIFIER_COOKIE = "kc_pkce_verifier";
export const OAUTH_STATE_COOKIE = "kc_oauth_state";

// TRANSACTION_MAX_AGE bounds the login transaction cookies: a user has ten
// minutes to complete the redirect to Keycloak and back before the verifier and
// state expire and the login must be restarted.
export const TRANSACTION_MAX_AGE = 60 * 10;

// REFRESH_COOKIE_MAX_AGE bounds the refresh-token and id-token cookies. Thirty
// days matches a typical Keycloak SSO session max; the refresh token itself may
// expire sooner, in which case the next refresh fails and the user re-logs in.
export const REFRESH_COOKIE_MAX_AGE = 60 * 60 * 24 * 30;

// SessionCookieOptions is the serializable shape the route handlers spread into
// the Next.js cookie store. secure is environment-driven: off for local http dev,
// on for deployed https so the cookie is never sent in the clear.
export interface SessionCookieOptions {
  httpOnly: true;
  secure: boolean;
  sameSite: "lax";
  path: string;
  maxAge: number;
}

// sessionCookieOptions builds the options for a long-lived session cookie.
// SameSite is lax so the cookie rides the top-level GET redirect back from
// Keycloak (a strict cookie would be withheld on that cross-site return and break
// the callback).
export function sessionCookieOptions(
  secure: boolean,
  maxAge: number,
): SessionCookieOptions {
  return { httpOnly: true, secure, sameSite: "lax", path: "/", maxAge };
}

// transactionCookieOptions builds the options for a short-lived login-transaction
// cookie, scoped to the whole site so both the login route (which sets it) and
// the callback route (which reads it) can reach it.
export function transactionCookieOptions(
  secure: boolean,
): SessionCookieOptions {
  return {
    httpOnly: true,
    secure,
    sameSite: "lax",
    path: "/",
    maxAge: TRANSACTION_MAX_AGE,
  };
}

// cookieSecure reports whether the session cookies should carry the Secure
// attribute, derived from the deployment environment. Local http dev opts out via
// AUTH_INSECURE_COOKIE so the cookie is accepted over http://localhost.
export function cookieSecure(
  env: { NODE_ENV?: string; AUTH_INSECURE_COOKIE?: string } = process.env,
): boolean {
  if (env.AUTH_INSECURE_COOKIE === "true") {
    return false;
  }
  return env.NODE_ENV === "production";
}

// CookieWriter is the minimal slice of the Next.js cookie store persistTokenSet
// needs. Taking the interface (not the store type) keeps this module free of a
// next/headers import so it stays importable from the proxy and client leaves.
export interface CookieWriter {
  set(name: string, value: string, options: SessionCookieOptions): void;
}

// SessionTokens is the persisted slice of a Keycloak token response: the access
// token (backend bearer and role source) plus the optional refresh and id tokens.
export interface SessionTokens {
  accessToken: string;
  refreshToken?: string;
  idToken?: string;
}

// persistTokenSet writes the session cookies for a freshly issued or refreshed
// token set, so the initial-login and refresh paths cannot drift in how they
// persist tokens. The access cookie's lifetime tracks the token's own expiry
// (floored so it is never written already-deleted); the refresh and id cookies
// outlive it so a short access token can be refreshed silently. accessMaxAge is
// supplied by the caller (computed from the token's exp) to keep this module free
// of the token-decoding dependency.
export function persistTokenSet(
  store: CookieWriter,
  tokens: SessionTokens,
  secure: boolean,
  accessMaxAge: number,
): void {
  store.set(
    ACCESS_TOKEN_COOKIE,
    tokens.accessToken,
    sessionCookieOptions(secure, accessMaxAge),
  );
  if (tokens.refreshToken) {
    store.set(
      REFRESH_TOKEN_COOKIE,
      tokens.refreshToken,
      sessionCookieOptions(secure, REFRESH_COOKIE_MAX_AGE),
    );
  }
  if (tokens.idToken) {
    store.set(
      ID_TOKEN_COOKIE,
      tokens.idToken,
      sessionCookieOptions(secure, REFRESH_COOKIE_MAX_AGE),
    );
  }
}

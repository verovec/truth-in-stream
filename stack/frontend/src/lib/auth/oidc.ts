import "server-only";

import * as oauth from "oauth4webapi";
import { readOidcConfig } from "./config";

// Thin wrapper over oauth4webapi for the Keycloak public client. oauth4webapi is
// the zero-dependency, Web-Crypto/Fetch-native OIDC library that Auth.js v5 and
// openid-client are built on; it is the lowest appropriate level here because the
// session IS the Keycloak token pair and the backend already validates the
// bearer, so the framework session models add nothing. Verified current 2026-06:
// oauth4webapi 3.8.6, Next.js 16.2.7. The client is public (PKCE S256, no
// secret), so client authentication is None.

// PUBLIC_CLIENT_AUTH is the no-secret client authentication method for the public
// PKCE client.
const PUBLIC_CLIENT_AUTH = oauth.None();

// insecureOption permits plain-HTTP requests to the issuer when (and only when)
// the issuer itself is an http:// URL - i.e. the local-dev Keycloak on
// http://localhost:8081. oauth4webapi refuses non-HTTPS endpoints by default; a
// deployed https issuer never gets the flag, so production stays HTTPS-only. The
// flag rides every request option (discovery, token, refresh) so each leg honors
// the same dev allowance.
export function insecureOption(issuer: string): {
  [oauth.allowInsecureRequests]?: true;
} {
  return issuer.startsWith("http://")
    ? { [oauth.allowInsecureRequests]: true }
    : {};
}

// authServerCache memoizes the discovered authorization-server metadata per
// issuer for the lifetime of the server process. Discovery is a network round
// trip whose result (endpoints, supported algorithms) is stable, so caching it
// keeps every login from re-fetching the well-known document.
const authServerCache = new Map<string, Promise<oauth.AuthorizationServer>>();

// authServer discovers and caches the Keycloak authorization-server metadata for
// the configured issuer.
export async function authServer(): Promise<oauth.AuthorizationServer> {
  const { issuer } = readOidcConfig();
  const cached = authServerCache.get(issuer);
  if (cached) {
    return cached;
  }
  const discovery = (async () => {
    const issuerUrl = new URL(issuer);
    const response = await oauth.discoveryRequest(issuerUrl, {
      algorithm: "oidc",
      ...insecureOption(issuer),
    });
    return oauth.processDiscoveryResponse(issuerUrl, response);
  })();
  authServerCache.set(issuer, discovery);
  return discovery;
}

// oidcClient returns the oauth4webapi client descriptor for the public web
// client.
export function oidcClient(): oauth.Client {
  return { client_id: readOidcConfig().clientId };
}

// AuthorizationRequest carries everything the login route needs to redirect the
// browser to Keycloak while preserving the PKCE verifier and CSRF state for the
// callback.
export interface AuthorizationRequest {
  url: URL;
  codeVerifier: string;
  state: string;
}

// buildAuthorizationRequest generates the PKCE verifier and challenge and the
// CSRF state, then assembles the Keycloak authorize URL. The verifier and state
// are returned (not stored) so the route handler owns persisting them in the
// transaction cookies.
export async function buildAuthorizationRequest(): Promise<AuthorizationRequest> {
  const as = await authServer();
  const { clientId, redirectUri } = readOidcConfig();
  const codeVerifier = oauth.generateRandomCodeVerifier();
  const codeChallenge = await oauth.calculatePKCECodeChallenge(codeVerifier);
  const state = oauth.generateRandomState();

  const url = new URL(as.authorization_endpoint!);
  url.searchParams.set("client_id", clientId);
  url.searchParams.set("redirect_uri", redirectUri);
  url.searchParams.set("response_type", "code");
  url.searchParams.set("scope", "openid email profile");
  url.searchParams.set("code_challenge", codeChallenge);
  url.searchParams.set("code_challenge_method", "S256");
  url.searchParams.set("state", state);

  return { url, codeVerifier, state };
}

// TokenSet is the subset of a Keycloak token response the frontend persists: the
// access token (the backend bearer and role source), the refresh token, and the
// id token (the logout hint).
export interface TokenSet {
  accessToken: string;
  refreshToken?: string;
  idToken?: string;
}

// exchangeCode validates the callback parameters against the expected state and
// exchanges the authorization code for tokens using the stored PKCE verifier.
export async function exchangeCode(
  currentUrl: URL,
  expectedState: string,
  codeVerifier: string,
): Promise<TokenSet> {
  const as = await authServer();
  const client = oidcClient();
  const { issuer, redirectUri } = readOidcConfig();

  const params = oauth.validateAuthResponse(
    as,
    client,
    currentUrl,
    expectedState,
  );
  const response = await oauth.authorizationCodeGrantRequest(
    as,
    client,
    PUBLIC_CLIENT_AUTH,
    params,
    redirectUri,
    codeVerifier,
    insecureOption(issuer),
  );
  const result = await oauth.processAuthorizationCodeResponse(
    as,
    client,
    response,
  );
  return {
    accessToken: result.access_token,
    refreshToken: result.refresh_token,
    idToken: typeof result.id_token === "string" ? result.id_token : undefined,
  };
}

// refreshTokens exchanges a refresh token for a fresh access token (and possibly
// a rotated refresh token) at the Keycloak token endpoint.
export async function refreshTokens(refreshToken: string): Promise<TokenSet> {
  const as = await authServer();
  const client = oidcClient();
  const { issuer } = readOidcConfig();
  const response = await oauth.refreshTokenGrantRequest(
    as,
    client,
    PUBLIC_CLIENT_AUTH,
    refreshToken,
    insecureOption(issuer),
  );
  const result = await oauth.processRefreshTokenResponse(as, client, response);
  return {
    accessToken: result.access_token,
    refreshToken: result.refresh_token,
    idToken: typeof result.id_token === "string" ? result.id_token : undefined,
  };
}

// buildLogoutUrl assembles the Keycloak RP-initiated logout (end-session) URL,
// passing the client id and, when available, the id-token hint so Keycloak can
// honor the post-logout redirect for the public client.
export async function buildLogoutUrl(idToken?: string): Promise<URL> {
  const as = await authServer();
  const { clientId, postLogoutRedirectUri } = readOidcConfig();
  const url = new URL(as.end_session_endpoint!);
  url.searchParams.set("client_id", clientId);
  url.searchParams.set("post_logout_redirect_uri", postLogoutRedirectUri);
  if (idToken) {
    url.searchParams.set("id_token_hint", idToken);
  }
  return url;
}

import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import {
  cookieSecure,
  OAUTH_STATE_COOKIE,
  PKCE_VERIFIER_COOKIE,
  transactionCookieOptions,
} from "@/lib/auth/cookies";
import { buildAuthorizationRequest } from "@/lib/auth/oidc";

// Starts the Keycloak authorization-code + PKCE login: it mints the PKCE
// verifier and CSRF state, stashes them in short-lived httpOnly transaction
// cookies, and redirects the browser to Keycloak's authorize endpoint. The
// verifier never leaves the server, so the token exchange in the callback cannot
// be replayed by a third party who only sees the redirect.
export async function GET() {
  const { url, codeVerifier, state } = await buildAuthorizationRequest();
  const store = await cookies();
  const options = transactionCookieOptions(cookieSecure());
  store.set(PKCE_VERIFIER_COOKIE, codeVerifier, options);
  store.set(OAUTH_STATE_COOKIE, state, options);
  return NextResponse.redirect(url);
}

import { type NextRequest, NextResponse } from "next/server";
import { cookies } from "next/headers";
import {
  cookieSecure,
  OAUTH_STATE_COOKIE,
  persistTokenSet,
  PKCE_VERIFIER_COOKIE,
} from "@/lib/auth/cookies";
import { exchangeCode } from "@/lib/auth/oidc";
import { secondsUntilExpiry } from "@/lib/auth/token";

// Completes the Keycloak login: it reads the PKCE verifier and CSRF state from
// the transaction cookies, exchanges the authorization code for tokens, persists
// them in httpOnly session cookies, and lands the user in the app. A missing
// verifier/state (a direct hit or an expired transaction) bounces back to login
// rather than attempting an exchange that would fail.
export async function GET(request: NextRequest) {
  const store = await cookies();
  const codeVerifier = store.get(PKCE_VERIFIER_COOKIE)?.value;
  const expectedState = store.get(OAUTH_STATE_COOKIE)?.value;

  store.delete(PKCE_VERIFIER_COOKIE);
  store.delete(OAUTH_STATE_COOKIE);

  if (!codeVerifier || !expectedState) {
    return NextResponse.redirect(new URL("/login?error=session", request.url));
  }

  let tokens;
  try {
    tokens = await exchangeCode(
      new URL(request.url),
      expectedState,
      codeVerifier,
    );
  } catch {
    return NextResponse.redirect(new URL("/login?error=exchange", request.url));
  }

  persistTokenSet(
    store,
    tokens,
    cookieSecure(),
    secondsUntilExpiry(tokens.accessToken),
  );

  return NextResponse.redirect(new URL("/app", request.url));
}

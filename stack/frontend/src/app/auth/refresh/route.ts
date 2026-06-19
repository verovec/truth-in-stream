import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import {
  ACCESS_TOKEN_COOKIE,
  cookieSecure,
  ID_TOKEN_COOKIE,
  persistTokenSet,
  REFRESH_TOKEN_COOKIE,
} from "@/lib/auth/cookies";
import { refreshTokens } from "@/lib/auth/oidc";
import { secondsUntilExpiry } from "@/lib/auth/token";

// Refreshes the Keycloak access token using the stored refresh token and re-sets
// the session cookies, keeping the httpOnly invariant: the refresh runs entirely
// server-side so the tokens never touch client JavaScript. A missing or rejected
// refresh token clears the dead session and reports 401 so the caller re-logs in.
// When Keycloak omits the id token on a refresh, the existing id-token cookie is
// left in place (persistTokenSet only rewrites the cookies present in the
// response) so the logout hint survives a silent refresh.
export async function POST() {
  const store = await cookies();
  const refreshToken = store.get(REFRESH_TOKEN_COOKIE)?.value;
  if (!refreshToken) {
    return NextResponse.json({ error: "no session" }, { status: 401 });
  }

  let tokens;
  try {
    tokens = await refreshTokens(refreshToken);
  } catch {
    store.delete(ACCESS_TOKEN_COOKIE);
    store.delete(REFRESH_TOKEN_COOKIE);
    store.delete(ID_TOKEN_COOKIE);
    return NextResponse.json({ error: "refresh failed" }, { status: 401 });
  }

  persistTokenSet(
    store,
    tokens,
    cookieSecure(),
    secondsUntilExpiry(tokens.accessToken),
  );

  return NextResponse.json({ ok: true });
}

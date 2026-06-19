import { type NextRequest, NextResponse } from "next/server";
import { cookies } from "next/headers";
import {
  ACCESS_TOKEN_COOKIE,
  ID_TOKEN_COOKIE,
  REFRESH_TOKEN_COOKIE,
} from "@/lib/auth/cookies";
import { buildLogoutUrl } from "@/lib/auth/oidc";

// Ends the session: it clears the local httpOnly session cookies and redirects
// the browser to Keycloak's RP-initiated logout (end-session) endpoint so the
// Keycloak SSO session is terminated too, not just the local cookies. The
// id-token hint is passed when present so Keycloak can honor the post-logout
// redirect for the public client. If the end-session URL cannot be built (e.g.
// Keycloak unreachable), the cookies are still cleared and the user lands on the
// landing page, so logout never leaves a half-open local session.
export async function GET(request: NextRequest) {
  const store = await cookies();
  const idToken = store.get(ID_TOKEN_COOKIE)?.value;

  store.delete(ACCESS_TOKEN_COOKIE);
  store.delete(REFRESH_TOKEN_COOKIE);
  store.delete(ID_TOKEN_COOKIE);

  try {
    const logoutUrl = await buildLogoutUrl(idToken);
    return NextResponse.redirect(logoutUrl);
  } catch {
    return NextResponse.redirect(new URL("/", request.url));
  }
}

import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

import { ACCESS_TOKEN_COOKIE } from "@/lib/auth/cookies";
import { locales } from "@/lib/i18n/config";

const LOGIN_PATH = "/login";

// Must match sessionCookieName in the backend handler package
// (stack/backend/internal/handler/auth.go); rename both together. The backend
// `session` cookie still gates the /api subtree, so the optimistic navigation
// gate treats it as a valid session alongside the Keycloak access-token cookie:
// either one means the caller has signed in.
const SESSION_COOKIE = "session";

// Routes reachable without a session: the public marketing landing (the bare
// root, which redirects to a locale, and each localised landing page under
// `/fr` and `/en`), the login page itself, and the OIDC route handlers (login
// start, callback, logout) which run the sign-in transaction before any session
// cookie exists. The login page stays reachable even with a cookie present so an
// invalid or stale cookie can never lock a user out of signing in. The auth
// handlers are listed explicitly rather than allowed by a /auth/ prefix, so a
// future authenticated page added under /auth/ is not silently served without a
// session check.
const PUBLIC_PATHS = new Set<string>([
  "/",
  ...locales.map((locale) => `/${locale}`),
  LOGIN_PATH,
  "/auth/login",
  "/auth/callback",
  "/auth/logout",
]);

// A path ending in a file extension (e.g. /og.png, /robots.txt) is a static
// marketing asset, served publicly. The matcher already excludes the framework
// asset prefixes; this covers files served from the public directory at the
// root.
const STATIC_ASSET = /\.[a-z0-9]+$/i;

function isPublic(pathname: string): boolean {
  return PUBLIC_PATHS.has(pathname) || STATIC_ASSET.test(pathname);
}

// proxy runs on every matched request in the Node runtime. It does two things:
//
//  - For /api/* it promotes the httpOnly Keycloak access-token cookie to an
//    Authorization: Bearer header before the rewrite forwards the request to the
//    Go backend. A browser fetch cannot set that header and the same-origin
//    rewrite forwards only cookies, so without this promotion the backend's
//    Identity middleware (which reads the header, never a cookie) would see every
//    caller as an anonymous guest. Promoting at the proxy boundary keeps the token
//    httpOnly end to end: it never reaches client JavaScript.
//  - For product pages it is an optimistic gate: the presence of either session
//    cookie (the Keycloak access token or the backend `session` cookie) decides
//    routing, while the backend cryptographically verifies the credential on every
//    request. A forged cookie gets past this redirect but every data call still
//    fails verification. Product routes redirect to login when no session cookie
//    is present.
export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  const accessToken = request.cookies.get(ACCESS_TOKEN_COOKIE)?.value;
  const hasSession =
    accessToken !== undefined || request.cookies.has(SESSION_COOKIE);

  if (pathname.startsWith("/api/")) {
    if (!accessToken) {
      return NextResponse.next();
    }
    const headers = new Headers(request.headers);
    headers.set("Authorization", `Bearer ${accessToken}`);
    return NextResponse.next({ request: { headers } });
  }

  if (!hasSession && !isPublic(pathname)) {
    return NextResponse.redirect(new URL(LOGIN_PATH, request.url));
  }
  return NextResponse.next();
}

// The matcher now includes /api/* (so the Bearer promotion above runs) while
// still excluding the framework asset prefixes and the favicon.
export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};

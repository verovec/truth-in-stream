import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

// Must match sessionCookieName in the backend handler package
// (stack/backend/internal/handler/auth.go); rename both together.
const SESSION_COOKIE = "session";
const LOGIN_PATH = "/login";

// Routes reachable without a session: the public marketing landing page and the
// login page itself. The login page stays reachable even with a cookie present
// so an invalid or stale cookie can never lock the operator out of signing in.
const PUBLIC_PATHS = new Set(["/", LOGIN_PATH]);

// A path ending in a file extension (e.g. /og.png, /robots.txt) is a static
// marketing asset, served publicly. The matcher already excludes the framework
// asset prefixes; this covers files served from the public directory at the
// root.
const STATIC_ASSET = /\.[a-z0-9]+$/i;

function isPublic(pathname: string): boolean {
  return PUBLIC_PATHS.has(pathname) || STATIC_ASSET.test(pathname);
}

// Optimistic gate only: cookie presence decides routing, while the Go backend
// cryptographically verifies the session on every API request. A forged cookie
// gets past this redirect but every data call still returns 401. Product routes
// such as /app redirect to the login page when no session cookie is present.
export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  const hasSession = request.cookies.has(SESSION_COOKIE);

  if (!hasSession && !isPublic(pathname)) {
    return NextResponse.redirect(new URL(LOGIN_PATH, request.url));
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api/|_next/static|_next/image|favicon.ico).*)"],
};

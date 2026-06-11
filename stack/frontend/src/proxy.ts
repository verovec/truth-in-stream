import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

// Must match sessionCookieName in the backend handler package
// (stack/backend/internal/handler/auth.go); rename both together.
const SESSION_COOKIE = "session";
const LOGIN_PATH = "/login";

// Optimistic gate only: cookie presence decides routing, while the Go backend
// cryptographically verifies the session on every API request. A forged
// cookie gets past this redirect but every data call still returns 401. The
// login page stays reachable even with a cookie present so an invalid or
// stale cookie can never lock the operator out of signing in again.
export function proxy(request: NextRequest) {
  const hasSession = request.cookies.has(SESSION_COOKIE);
  const isLoginPage = request.nextUrl.pathname === LOGIN_PATH;

  if (!hasSession && !isLoginPage) {
    return NextResponse.redirect(new URL(LOGIN_PATH, request.url));
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api/|_next/static|_next/image|favicon.ico).*)"],
};

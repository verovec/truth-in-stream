import "server-only";

import { cookies } from "next/headers";
import { ACCESS_TOKEN_COOKIE } from "./cookies";
import { isExpired, type Role, roleFromToken } from "./token";

// Session is the resolved caller for a server render: whether a Keycloak session
// is present and the coarse role the UI gates on. authenticated is false when no
// (or no live) access token cookie is set, so the app shell can fall the caller
// back to guest without revealing the admin surface.
export interface Session {
  authenticated: boolean;
  role: Role;
}

// GUEST_SESSION is the fail-closed default: an unauthenticated guest. Any code
// path that cannot resolve a token gets this, so a missing or dead token never
// yields an admin session.
export const GUEST_SESSION: Session = { authenticated: false, role: "guest" };

// resolveSession derives the session from a raw access-token cookie value. It is
// pure so the role gate is unit-testable without a request: a missing token is a
// guest, a live token yields its role, and an expired token is treated as no
// session (the caller must refresh or re-login before the role is trusted again).
export function resolveSession(
  accessToken: string | undefined,
  nowSeconds: number = Date.now() / 1000,
): Session {
  if (!accessToken || isExpired(accessToken, nowSeconds, 0)) {
    return GUEST_SESSION;
  }
  return { authenticated: true, role: roleFromToken(accessToken) };
}

// getSession reads the access-token cookie and resolves the caller's session for
// a Server Component or route handler. It never throws on a missing cookie; the
// caller always gets a concrete session, guest by default.
export async function getSession(): Promise<Session> {
  const store = await cookies();
  return resolveSession(store.get(ACCESS_TOKEN_COOKIE)?.value);
}

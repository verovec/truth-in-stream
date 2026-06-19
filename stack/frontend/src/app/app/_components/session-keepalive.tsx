"use client";

// Keeps the Keycloak session alive while the app is open. The access token is
// short-lived (Keycloak's default is minutes); without periodic refresh the
// access-token cookie would expire and the next navigation would bounce the user
// to login despite a valid 30-day refresh token. This leaf POSTs to the
// server-side /auth/refresh route on an interval, which rotates the tokens and
// re-sets the httpOnly cookies entirely server-side, so the tokens never reach
// client JavaScript. It renders nothing.
import { useEffect } from "react";

// DEFAULT_INTERVAL_MS refreshes every four minutes, comfortably inside Keycloak's
// default five-minute access-token lifetime so the cookie is renewed before it
// can lapse. Configurable for tests.
const DEFAULT_INTERVAL_MS = 4 * 60 * 1000;

export function SessionKeepalive({
  intervalMs = DEFAULT_INTERVAL_MS,
}: {
  intervalMs?: number;
}) {
  useEffect(() => {
    const refresh = () => {
      // Fire-and-forget: a failed refresh (expired refresh token) clears the
      // session server-side and the next navigation lands on login, so there is
      // nothing to handle here. keepalive lets the request outlive a tab close.
      void fetch("/auth/refresh", { method: "POST", keepalive: true }).catch(
        () => {},
      );
    };
    const timer = setInterval(refresh, intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);

  return null;
}

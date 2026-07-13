// Builds backend WebSocket URLs. In production the frontend and API share an
// origin (the ALB serves /api/* same-origin), so the socket derives from the
// page origin; in local dev NEXT_PUBLIC_API_URL points at the backend container
// and is converted from http(s) to ws(s).
import { API_BASE } from "@/lib/http";

const WS_SCHEME: Record<string, string> = {
  "http:": "ws:",
  "https:": "wss:",
};

export type SocketUrlOptions = {
  apiBase?: string;
  origin?: string;
};

// apiWsUrl resolves a backend path to a ws(s) URL. apiBase and origin are
// injectable for tests; they default to the configured API base and the current
// page origin. Every backend socket URL is built here so they share one
// scheme-conversion rule.
function apiWsUrl(pathname: string, options: SocketUrlOptions): string {
  const apiBase = options.apiBase ?? API_BASE;
  const origin = options.origin ?? window.location.origin;
  const url = new URL(apiBase || origin);
  url.protocol = WS_SCHEME[url.protocol] ?? url.protocol;
  url.pathname = pathname;
  return url.toString();
}

/**
 * Returns the ws(s) URL for a video's live analysis stream. The video id is
 * path-encoded.
 */
export function liveSocketUrl(
  videoId: string,
  options: SocketUrlOptions = {},
): string {
  return apiWsUrl(`/api/videos/${encodeURIComponent(videoId)}/live`, options);
}

/**
 * Returns the ws(s) URL for a TV channel's read-only viewer stream. The channel
 * id is path-encoded. Like the video live socket, the browser rides the
 * same-origin proxy so the SameSite session cookie authenticates the upgrade -
 * the token is never placed in the URL.
 */
export function channelLiveSocketUrl(
  channelId: string,
  options: SocketUrlOptions = {},
): string {
  return apiWsUrl(
    `/api/tv/channels/${encodeURIComponent(channelId)}/live`,
    options,
  );
}

/**
 * Returns the ws(s) URL for the developer wiki-search probe (dev only). The
 * route exists on the backend only when the debug flag is on.
 */
export function debugSearchUrl(options: SocketUrlOptions = {}): string {
  return apiWsUrl("/api/debug/wiki-search", options);
}

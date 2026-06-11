// Builds the live fact-check WebSocket URL. In production the frontend and API
// share an origin (the ALB serves /api/* same-origin), so the socket derives
// from the page origin; in local dev NEXT_PUBLIC_API_URL points at the backend
// container and is converted from http(s) to ws(s).
import { API_BASE } from "@/lib/http";

const WS_SCHEME: Record<string, string> = {
  "http:": "ws:",
  "https:": "wss:",
};

type SocketUrlOptions = {
  apiBase?: string;
  origin?: string;
};

/**
 * Returns the ws(s) URL for a video's live analysis stream. The video id is
 * path-encoded. apiBase and origin are injectable for tests; they default to
 * the configured API base and the current page origin.
 */
export function liveSocketUrl(
  videoId: string,
  options: SocketUrlOptions = {},
): string {
  const apiBase = options.apiBase ?? API_BASE;
  const origin = options.origin ?? window.location.origin;
  const url = new URL(apiBase || origin);
  url.protocol = WS_SCHEME[url.protocol] ?? url.protocol;
  url.pathname = `/api/videos/${encodeURIComponent(videoId)}/live`;
  return url.toString();
}

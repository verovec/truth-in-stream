// Typed client for the durable video pre-analysis API (stack/backend
// internal/handler/video_analysis.go): the analysis lifecycle read used for
// polling and hydration, the admin-only trigger, and the library listing
// carrying each video's analysis state. This module is the canonical analysis
// client; the backoffice keeps a local copy this wave and unifies on it later.
import { API_BASE, toApiError } from "@/lib/http";
import { type LiveFrame, parseLiveFrameValue } from "@/lib/live/frames";
import type { LibraryVideo, VideoKind, VideoStatus } from "@/lib/video/api";

// VideoAnalysisStatus is the pre-analysis lifecycle: never analysed, a headless
// run in flight, a stored result available, or the last run failed (re-runnable).
export type VideoAnalysisStatus = "none" | "analysing" | "complete" | "failed";

const ANALYSIS_STATUSES: ReadonlySet<string> = new Set([
  "none",
  "analysing",
  "complete",
  "failed",
]);

// normalizeAnalysisStatus keeps the closed vocabulary honest on the wire: an
// unknown future status renders as "none" (no affordances, no badge) rather
// than crashing or mis-styling a chip.
function normalizeAnalysisStatus(value: unknown): VideoAnalysisStatus {
  return typeof value === "string" && ANALYSIS_STATUSES.has(value)
    ? (value as VideoAnalysisStatus)
    : "none";
}

// AnalysisCounters is the stored result's denormalized claim tally, for badges
// and summaries without decoding the frames.
export type AnalysisCounters = {
  total: number;
  credible: number;
  disputed: number;
  unverifiable: number;
};

// VideoAnalysis is one video's analysis state as served by
// GET /api/videos/{id}/analysis. frames is non-null only when the backend sent
// a frame list (a completed, decodable run): hydration keys on frames presence,
// not on status alone, because a re-analysis poll reports "analysing" without
// frames while the previous result must stay on screen. Each frame is the
// exact wire shape the live WebSocket emits, with absolute video-time
// timestamps, validated through the shared live-frame parser.
export type VideoAnalysis = {
  analysisStatus: VideoAnalysisStatus;
  analysisError: string | null;
  analyzedAt: string | null;
  analysisRuns: number;
  analysisProgressMs: number;
  engine: unknown;
  counters: AnalysisCounters | null;
  frames: LiveFrame[] | null;
};

// AnalysedLibraryVideo is a library row plus its analysis state from the list
// payload, so tiles can badge analysed videos and the player can gate analysed
// playback without a per-video call.
export type AnalysedLibraryVideo = LibraryVideo & {
  analysisStatus: VideoAnalysisStatus;
  analyzedAt: string | null;
};

type VideoWire = {
  id: string;
  title: string;
  status: VideoStatus;
  kind: VideoKind;
  content_type: string;
  size_bytes: number;
  created_at: string;
  updated_at: string;
  analysis_status?: string;
  analyzed_at?: string;
};

type ListWire = { videos?: VideoWire[] };

type AnalysisWire = {
  analysis_status?: string;
  analysis_error?: string;
  analyzed_at?: string;
  analysis_runs?: number;
  analysis_progress_ms?: number;
  engine?: unknown;
  counters?: {
    total?: number;
    credible?: number;
    disputed?: number;
    unverifiable?: number;
  };
  frames?: unknown[];
};

function normalizeAnalysedVideo(wire: VideoWire): AnalysedLibraryVideo {
  return {
    id: wire.id,
    title: wire.title,
    status: wire.status,
    kind: wire.kind,
    contentType: wire.content_type,
    sizeBytes: wire.size_bytes,
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
    analysisStatus: normalizeAnalysisStatus(wire.analysis_status),
    analyzedAt: wire.analyzed_at ?? null,
  };
}

function finiteOrZero(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

// listVideosWithAnalysis lists the library with each video's analysis state.
// It reads the same GET /api/videos the plain listVideos client does; this
// variant keeps the analysis_status / analyzed_at fields the older normalizer
// drops.
export async function listVideosWithAnalysis(
  signal?: AbortSignal,
): Promise<AnalysedLibraryVideo[]> {
  const response = await fetch(`${API_BASE}/api/videos`, { signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ListWire;
  return (wire.videos ?? []).map(normalizeAnalysedVideo);
}

// getVideoAnalysis fetches one video's analysis lifecycle and, when a stored
// result is readable, its full frame list. A malformed frame is dropped
// individually - exactly as the socket path skips a stray frame - so one bad
// entry never costs the rest of the session.
export async function getVideoAnalysis(
  id: string,
  signal?: AbortSignal,
): Promise<VideoAnalysis> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}/analysis`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as AnalysisWire;
  return {
    analysisStatus: normalizeAnalysisStatus(wire.analysis_status),
    analysisError: wire.analysis_error ?? null,
    analyzedAt: wire.analyzed_at ?? null,
    analysisRuns: finiteOrZero(wire.analysis_runs),
    analysisProgressMs: finiteOrZero(wire.analysis_progress_ms),
    engine: wire.engine ?? null,
    counters: wire.counters
      ? {
          total: finiteOrZero(wire.counters.total),
          credible: finiteOrZero(wire.counters.credible),
          disputed: finiteOrZero(wire.counters.disputed),
          unverifiable: finiteOrZero(wire.counters.unverifiable),
        }
      : null,
    frames: Array.isArray(wire.frames)
      ? wire.frames
          .map(parseLiveFrameValue)
          .filter((frame): frame is LiveFrame => frame !== null)
      : null,
  };
}

// startVideoAnalysis asks the backend to start (or re-run) the headless
// pre-analysis of a video. Admin-gated on the backend; it answers 202 with no
// body on accept, 409 while a run is already in progress, 422 when the video
// is not ready, 404 for an unknown id. Nothing is parsed on success.
export async function startVideoAnalysis(
  id: string,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}/analyse`,
    { method: "POST", signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
}

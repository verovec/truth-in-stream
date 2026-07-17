// Local client for the video pre-analysis API; the epic's close-out card
// unifies it with lib/video/analysis.ts once the player-side card lands.
//
// The backoffice lists videos through this module rather than lib/video/api
// because the list payload now carries the analysis lifecycle fields
// (analysis_status, analyzed_at, duration_ms) that the shared normalizer -
// frozen this wave - drops. The per-id analysis endpoint is only read for
// what the list cannot carry: progress while analysing, the error once
// failed, and the claim counters once complete. Its `frames` field is
// deliberately not modeled: it can be megabytes and the backoffice never
// renders it.
import { API_BASE, toApiError } from "@/lib/http";
import type { LibraryVideo, VideoKind, VideoStatus } from "@/lib/video/api";

export type VideoAnalysisStatus = "none" | "analysing" | "complete" | "failed";

// BackofficeVideo is one management row: a library video plus its
// pre-analysis lifecycle. analyzedAt is null until a completed run exists;
// durationMs is null for videos ingested without a known duration (raw
// uploads), which makes the analysing progress indeterminate.
export type BackofficeVideo = LibraryVideo & {
  analysisStatus: VideoAnalysisStatus;
  analyzedAt: string | null;
  durationMs: number | null;
};

export type AnalysisCounters = {
  total: number;
  credible: number;
  disputed: number;
  unverifiable: number;
};

// VideoAnalysisDetail is the per-id read: lifecycle fields are always
// present; counters only once a completed run's stored result is readable.
export type VideoAnalysisDetail = {
  analysisStatus: VideoAnalysisStatus;
  analysisError: string | null;
  analyzedAt: string | null;
  analysisRuns: number;
  analysisProgressMs: number;
  counters: AnalysisCounters | null;
};

type BackofficeVideoWire = {
  id: string;
  title: string;
  status: VideoStatus;
  kind: VideoKind;
  content_type: string;
  size_bytes: number;
  created_at: string;
  updated_at: string;
  duration_ms?: number;
  analysis_status: VideoAnalysisStatus;
  analyzed_at?: string;
};

type ListWire = { videos?: BackofficeVideoWire[] };

type AnalysisDetailWire = {
  analysis_status: VideoAnalysisStatus;
  analysis_error?: string;
  analyzed_at?: string;
  analysis_runs: number;
  analysis_progress_ms: number;
  counters?: AnalysisCounters;
};

function normalizeBackofficeVideo(wire: BackofficeVideoWire): BackofficeVideo {
  return {
    id: wire.id,
    title: wire.title,
    status: wire.status,
    kind: wire.kind,
    contentType: wire.content_type,
    sizeBytes: wire.size_bytes,
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
    durationMs: wire.duration_ms ?? null,
    analysisStatus: wire.analysis_status,
    analyzedAt: wire.analyzed_at ?? null,
  };
}

export async function listBackofficeVideos(
  signal?: AbortSignal,
): Promise<BackofficeVideo[]> {
  const response = await fetch(`${API_BASE}/api/videos`, { signal });
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as ListWire;
  return (wire.videos ?? []).map(normalizeBackofficeVideo);
}

export async function getVideoAnalysis(
  id: string,
  signal?: AbortSignal,
): Promise<VideoAnalysisDetail> {
  const response = await fetch(
    `${API_BASE}/api/videos/${encodeURIComponent(id)}/analysis`,
    { signal },
  );
  if (!response.ok) {
    throw await toApiError(response);
  }
  const wire = (await response.json()) as AnalysisDetailWire;
  return {
    analysisStatus: wire.analysis_status,
    analysisError: wire.analysis_error ?? null,
    analyzedAt: wire.analyzed_at ?? null,
    analysisRuns: wire.analysis_runs,
    analysisProgressMs: wire.analysis_progress_ms,
    counters: wire.counters ?? null,
  };
}

// startVideoAnalysis triggers a first run or a re-run (one endpoint for
// both). The backend answers 202 with no body on accept; 409 while a run is
// already in progress and 422 while the video is not ready, both of which
// callers branch on via ApiError.status.
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

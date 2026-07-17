"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError } from "@/lib/http";
import type { LiveFrame } from "@/lib/live/frames";
import {
  getVideoAnalysis,
  startVideoAnalysis,
  type VideoAnalysis,
  type VideoAnalysisStatus,
} from "@/lib/video/analysis";

// DEFAULT_POLL_MS is the cadence the player re-checks a running pre-analysis,
// matching the documents pattern: the run is server-driven and persisted, so
// progress is observed by polling the stored truth, which keeps the chip
// refresh-safe with no realtime plumbing.
const DEFAULT_POLL_MS = 2000;

// TrackedVideo is the slice of the selected library row the tracker needs. The
// status is the canonical one the library list holds - the tracker reports
// observed transitions upward through onStatusChange rather than keeping a
// second copy that could drift.
export type TrackedVideo = {
  id: string;
  analysisStatus: VideoAnalysisStatus;
};

// StartAnalysisError classifies a failed trigger for the control's copy:
// another run already in flight (409), the video not ready (422), the caller
// not an admin (401/403), or anything else.
export type StartAnalysisError =
  | "conflict"
  | "notReady"
  | "forbidden"
  | "failed";

export type VideoAnalysisTrack = {
  // The analysed position of the running (or last observed) run, driving the
  // progress readout.
  progressMs: number;
  // The backend's failure reason once a failed run has been observed, null
  // until then (the list payload carries only the status).
  analysisError: string | null;
  // The stored session's frames once fetched; null until then. Kept across a
  // re-analysis poll that reports "analysing" without frames, so the previous
  // result never vanishes mid-run.
  frames: LiveFrame[] | null;
  // True when the analysis of a complete video could not be fetched (or
  // carried no decodable frames), so the control can offer a reload.
  loadFailed: boolean;
  starting: boolean;
  startError: StartAnalysisError | null;
  start: () => void;
  retryLoad: () => void;
};

function classifyStartError(err: unknown): StartAnalysisError {
  if (err instanceof ApiError) {
    if (err.status === 409) {
      return "conflict";
    }
    if (err.status === 422) {
      return "notReady";
    }
    if (err.status === 401 || err.status === 403) {
      return "forbidden";
    }
  }
  return "failed";
}

/**
 * Tracks the selected video's pre-analysis: fetches the stored result once for
 * a complete video (the hydration payload), polls every pollIntervalMs while a
 * run is analysing, and exposes the admin trigger. Observed status transitions
 * are reported through onStatusChange so the library list stays the single
 * source of the status; the hook re-arms off the status it is handed back.
 * loadAnalysis and startAnalysis are injection seams with production defaults.
 */
export function useVideoAnalysis({
  video,
  onStatusChange,
  loadAnalysis = getVideoAnalysis,
  startAnalysis = startVideoAnalysis,
  pollIntervalMs = DEFAULT_POLL_MS,
}: {
  video: TrackedVideo | null;
  onStatusChange: (
    videoId: string,
    status: VideoAnalysisStatus,
    analyzedAt?: string | null,
  ) => void;
  loadAnalysis?: (id: string, signal?: AbortSignal) => Promise<VideoAnalysis>;
  startAnalysis?: (id: string, signal?: AbortSignal) => Promise<void>;
  pollIntervalMs?: number;
}): VideoAnalysisTrack {
  const [progressMs, setProgressMs] = useState(0);
  const [analysisError, setAnalysisError] = useState<string | null>(null);
  const [frames, setFrames] = useState<LiveFrame[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState<StartAnalysisError | null>(null);
  const [loadNonce, setLoadNonce] = useState(0);

  const videoId = video?.id ?? null;
  const status = video?.analysisStatus ?? null;

  // A different video resets the track during render (the sanctioned
  // derive-state-from-a-changed-prop pattern), so a stale result can never
  // hydrate or badge the newly selected video.
  const [trackedId, setTrackedId] = useState(videoId);
  if (trackedId !== videoId) {
    setTrackedId(videoId);
    setProgressMs(0);
    setAnalysisError(null);
    setFrames(null);
    setLoadFailed(false);
    setStarting(false);
    setStartError(null);
  }

  // Seams and the report-upward callback live in refs so a changing identity
  // never restarts the fetch/poll effect mid-cycle.
  const loadAnalysisRef = useRef(loadAnalysis);
  const startAnalysisRef = useRef(startAnalysis);
  const onStatusChangeRef = useRef(onStatusChange);
  useEffect(() => {
    loadAnalysisRef.current = loadAnalysis;
    startAnalysisRef.current = startAnalysis;
    onStatusChangeRef.current = onStatusChange;
  });

  // hasFramesRef mirrors frames-presence for the poll closure, which must obey
  // "never discard hydrated frames on an analysing poll" without re-arming the
  // effect every time frames land.
  const hasFramesRef = useRef(false);
  useEffect(() => {
    hasFramesRef.current = frames !== null;
  }, [frames]);

  useEffect(() => {
    if (videoId === null || status === null) {
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    let handle: ReturnType<typeof setTimeout> | undefined;

    const applyAnalysis = (analysis: VideoAnalysis) => {
      setProgressMs(analysis.analysisProgressMs);
      if (analysis.analysisError !== null) {
        setAnalysisError(analysis.analysisError);
      }
      if (analysis.frames !== null) {
        setFrames(analysis.frames);
        setLoadFailed(false);
      }
      if (analysis.analysisStatus !== status) {
        onStatusChangeRef.current(
          videoId,
          analysis.analysisStatus,
          analysis.analyzedAt,
        );
      }
    };

    const fetchOnce = async () => {
      try {
        const analysis = await loadAnalysisRef.current(
          videoId,
          controller.signal,
        );
        if (cancelled) {
          return;
        }
        applyAnalysis(analysis);
        // A complete video whose response carried no decodable frames cannot
        // hydrate; surface it so the control offers a reload instead of the
        // player sitting silently empty.
        if (status === "complete" && analysis.frames === null) {
          setLoadFailed(true);
        }
      } catch {
        if (cancelled || controller.signal.aborted) {
          return;
        }
        if (status === "complete") {
          setLoadFailed(true);
        }
      }
    };

    const poll = async () => {
      try {
        const analysis = await loadAnalysisRef.current(
          videoId,
          controller.signal,
        );
        if (cancelled) {
          return;
        }
        applyAnalysis(analysis);
        if (analysis.analysisStatus === "analysing") {
          handle = setTimeout(() => void poll(), pollIntervalMs);
        }
      } catch (err) {
        if (cancelled || controller.signal.aborted) {
          return;
        }
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
          // The video was deleted or access revoked: retrying the same request
          // cannot heal it, so the poll stops rather than looping forever.
          return;
        }
        // A transient failure never abandons a running analysis: keep the
        // cadence and re-check once the backend recovers.
        handle = setTimeout(() => void poll(), pollIntervalMs);
      }
    };

    if (status === "analysing") {
      void poll();
    } else if (
      (status === "complete" && !hasFramesRef.current) ||
      (status === "failed" && analysisError === null)
    ) {
      // complete: fetch the stored frames for hydration (skipped when a
      // completing poll already delivered them). failed: fetch once for the
      // backend's failure reason, which the list payload does not carry.
      void fetchOnce();
    }

    return () => {
      cancelled = true;
      controller.abort();
      if (handle) {
        clearTimeout(handle);
      }
    };
    // analysisError is read only to trigger the one-shot failed-reason fetch;
    // re-running when it lands is a no-op (the condition turns false).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [videoId, status, loadNonce, pollIntervalMs]);

  // startingRef backs the double-fire guard: state alone lags a synchronous
  // double click.
  const startingRef = useRef(false);
  const videoIdRef = useRef(videoId);
  useEffect(() => {
    videoIdRef.current = videoId;
  }, [videoId]);

  const start = useCallback(() => {
    const id = videoIdRef.current;
    if (id === null || startingRef.current) {
      return;
    }
    startingRef.current = true;
    setStarting(true);
    setStartError(null);
    void (async () => {
      try {
        await startAnalysisRef.current(id);
        if (videoIdRef.current === id) {
          // The run replaces progress from zero; previously hydrated frames
          // are kept so a re-analysis never blanks the current result.
          setProgressMs(0);
          setAnalysisError(null);
          onStatusChangeRef.current(id, "analysing");
        }
      } catch (err) {
        if (videoIdRef.current !== id) {
          return;
        }
        const classified = classifyStartError(err);
        setStartError(classified);
        if (classified === "conflict") {
          // Another operator's run is already in flight: adopt it and let the
          // poll surface its progress.
          onStatusChangeRef.current(id, "analysing");
        }
      } finally {
        startingRef.current = false;
        if (videoIdRef.current === id) {
          setStarting(false);
        }
      }
    })();
  }, []);

  const retryLoad = useCallback(() => {
    setLoadFailed(false);
    setLoadNonce((nonce) => nonce + 1);
  }, []);

  return {
    progressMs,
    analysisError,
    frames,
    loadFailed,
    starting,
    startError,
    start,
    retryLoad,
  };
}

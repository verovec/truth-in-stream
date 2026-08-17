"use client";

import { useEffect, useRef, useState } from "react";
import {
  getVideoAnalysis,
  type VideoAnalysis,
  type VideoAnalysisStatus,
} from "@/lib/video/analysis";

// useVideoAnalysisDetail supplies one management row's per-id analysis
// detail: live progress while the row is analysing (polled at the section's
// rhythm), the error once it failed, and the claim counters once complete.
// The list payload stays the status source of truth; this hook only fills
// what the list cannot carry, and fetches nothing for un-analysed rows so an
// idle catalog costs zero extra requests.
export function useVideoAnalysisDetail({
  videoId,
  analysisStatus,
  analyzedAt,
  loadAnalysis = getVideoAnalysis,
  pollIntervalMs,
}: {
  videoId: string;
  analysisStatus: VideoAnalysisStatus;
  analyzedAt: string | null;
  loadAnalysis?: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<VideoAnalysis>;
  pollIntervalMs: number;
}): VideoAnalysis | null {
  const [detail, setDetail] = useState<VideoAnalysis | null>(null);

  // A lifecycle change (a re-analyse starting, a run completing) drops the
  // previous run's detail during render - the supported adjust-on-prop-change
  // pattern - so a re-analysed row never shows the finished run's progress or
  // counters while the fresh fetch is in flight.
  const lifecycle = `${videoId} ${analysisStatus} ${analyzedAt ?? ""}`;
  const [trackedLifecycle, setTrackedLifecycle] = useState(lifecycle);
  if (trackedLifecycle !== lifecycle) {
    setTrackedLifecycle(lifecycle);
    setDetail(null);
  }

  // The seam is read through a ref so an inline loadAnalysis prop cannot
  // restart the poll loop on every render.
  const loadAnalysisRef = useRef(loadAnalysis);
  useEffect(() => {
    loadAnalysisRef.current = loadAnalysis;
  });

  useEffect(() => {
    if (analysisStatus === "none") {
      return;
    }
    const controller = new AbortController();
    const tick = () => {
      loadAnalysisRef
        .current(videoId, controller.signal)
        .then((fresh) => {
          if (!controller.signal.aborted) {
            setDetail(fresh);
          }
        })
        .catch(() => {
          // A transient failure keeps the last detail: while analysing the
          // next tick retries; a terminal row simply renders without the
          // extras until its lifecycle moves again.
        });
    };
    tick();
    if (analysisStatus !== "analysing") {
      return () => controller.abort();
    }
    const handle = setInterval(tick, pollIntervalMs);
    return () => {
      controller.abort();
      clearInterval(handle);
    };
  }, [videoId, analysisStatus, analyzedAt, pollIntervalMs]);

  return detail;
}

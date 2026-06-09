"use client";

import { useEffect, useState } from "react";
import {
  fetchVideoResults,
  fetchVideoStatus,
  submitVideo,
  type FactCheckSegment,
} from "@/lib/fact-check/api";

export type FactCheckState =
  | { status: "loading" }
  | { status: "processing"; segmentsDone: number; segmentsTotal: number }
  | { status: "ready"; segments: FactCheckSegment[] }
  | { status: "error"; message: string };

const DEFAULT_POLL_INTERVAL_MS = 2000;

/**
 * Submits the video source for processing and resolves to its
 * timestamp-indexed fact-check results, polling status while the backend
 * pipeline runs. Already-processed sources short-circuit to results.
 */
export function useFactCheck(
  source: string,
  pollIntervalMs: number = DEFAULT_POLL_INTERVAL_MS,
): FactCheckState {
  const [state, setState] = useState<FactCheckState>({ status: "loading" });
  const [trackedSource, setTrackedSource] = useState(source);
  if (trackedSource !== source) {
    setTrackedSource(source);
    setState({ status: "loading" });
  }

  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const fail = (cause: unknown) => {
      if (signal.aborted) {
        return;
      }
      setState({
        status: "error",
        message: cause instanceof Error ? cause.message : "request failed",
      });
    };

    const schedulePoll = (videoId: string) => {
      timer = setTimeout(() => {
        poll(videoId).catch(fail);
      }, pollIntervalMs);
    };

    async function loadResults(videoId: string): Promise<void> {
      const outcome = await fetchVideoResults(videoId, signal);
      if (signal.aborted) {
        return;
      }
      if (outcome.kind === "pending") {
        schedulePoll(videoId);
        return;
      }
      setState({ status: "ready", segments: outcome.segments });
    }

    async function poll(videoId: string): Promise<void> {
      const progress = await fetchVideoStatus(videoId, signal);
      if (signal.aborted) {
        return;
      }
      if (progress.status === "complete") {
        await loadResults(videoId);
        return;
      }
      if (progress.status === "failed") {
        setState({
          status: "error",
          message: progress.error ?? "processing failed",
        });
        return;
      }
      setState({
        status: "processing",
        segmentsDone: progress.segmentsDone,
        segmentsTotal: progress.segmentsTotal,
      });
      schedulePoll(videoId);
    }

    submitVideo(source, signal)
      .then((submission) => {
        if (signal.aborted) {
          return;
        }
        return submission.status === "complete"
          ? loadResults(submission.videoId)
          : poll(submission.videoId);
      })
      .catch(fail);

    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [source, pollIntervalMs]);

  return state;
}

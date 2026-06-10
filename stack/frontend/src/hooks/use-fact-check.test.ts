import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import {
  json,
  resultsRoute,
  statusRoute,
  stubBackend,
  submitRoute,
} from "@/test/fact-check";
import { useFactCheck } from "./use-fact-check";

const SEGMENTS = [
  { start: 0, end: 4, text: "hello", matches: [] },
  { start: 4, end: 9, text: "world", matches: [] },
];

describe("useFactCheck", () => {
  test("starts in the loading state", () => {
    stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "processing",
          segments_total: 0,
          segments_done: 0,
        }),
      ),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 60_000));

    expect(result.current).toEqual({ status: "loading" });
  });

  test("loads results directly for an already-processed video", async () => {
    stubBackend([
      submitRoute(json(200, { video_id: "v1", status: "complete" })),
      resultsRoute(json(200, { video_id: "v1", segments: SEGMENTS })),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({ status: "ready", segments: SEGMENTS }),
    );
  });

  test("polls status with progress until complete, then loads results", async () => {
    let releaseCompletion!: () => void;
    const completionGate = new Promise<void>((resolve) => {
      releaseCompletion = resolve;
    });
    stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "processing",
          segments_total: 10,
          segments_done: 3,
        }),
        () =>
          completionGate.then(
            json(200, {
              video_id: "v1",
              status: "complete",
              segments_total: 10,
              segments_done: 10,
            }),
          ),
      ),
      resultsRoute(json(200, { video_id: "v1", segments: SEGMENTS })),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({
        status: "processing",
        segmentsDone: 3,
        segmentsTotal: 10,
      }),
    );

    releaseCompletion();
    await waitFor(() =>
      expect(result.current).toEqual({ status: "ready", segments: SEGMENTS }),
    );
  });

  test("keeps polling when results race a 409 and recovers", async () => {
    stubBackend([
      submitRoute(json(200, { video_id: "v1", status: "complete" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "complete",
          segments_total: 2,
          segments_done: 2,
        }),
      ),
      resultsRoute(
        json(409, { error: "processing has not completed" }),
        json(200, { video_id: "v1", segments: SEGMENTS }),
      ),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({ status: "ready", segments: SEGMENTS }),
    );
  });

  test("surfaces a failed run with its reason", async () => {
    stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "failed",
          segments_total: 10,
          segments_done: 4,
          error: "transcription unavailable",
        }),
      ),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({
        status: "error",
        message: "transcription unavailable",
      }),
    );
  });

  test("surfaces backend rejections as errors", async () => {
    stubBackend([
      submitRoute(json(503, { error: "processing queue is full, retry later" })),
    ]);

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({
        status: "error",
        message: "processing queue is full, retry later",
      }),
    );
  });

  test("surfaces network failures as errors", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(
      new TypeError("fetch failed"),
    );

    const { result } = renderHook(() => useFactCheck("src", 0));

    await waitFor(() =>
      expect(result.current).toEqual({
        status: "error",
        message: "fetch failed",
      }),
    );
  });

  test("resets to loading and refetches when the source changes", async () => {
    const otherSegments = [{ start: 0, end: 2, text: "other", matches: [] }];
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos") &&
          init?.method === "POST" &&
          init.body === JSON.stringify({ source: "first" }),
        responses: [json(200, { video_id: "v1", status: "complete" })],
      },
      {
        match: (url, init) =>
          url.endsWith("/api/videos") &&
          init?.method === "POST" &&
          init.body === JSON.stringify({ source: "second" }),
        responses: [json(200, { video_id: "v2", status: "complete" })],
      },
      {
        match: (url) => url.endsWith("/v1/results"),
        responses: [json(200, { video_id: "v1", segments: SEGMENTS })],
      },
      {
        match: (url) => url.endsWith("/v2/results"),
        responses: [json(200, { video_id: "v2", segments: otherSegments })],
      },
    ]);

    const { result, rerender } = renderHook(
      ({ source }) => useFactCheck(source, 0),
      { initialProps: { source: "first" } },
    );
    await waitFor(() =>
      expect(result.current).toEqual({ status: "ready", segments: SEGMENTS }),
    );

    rerender({ source: "second" });

    expect(result.current).toEqual({ status: "loading" });
    await waitFor(() =>
      expect(result.current).toEqual({
        status: "ready",
        segments: otherSegments,
      }),
    );
  });

  test("stops polling on unmount", async () => {
    const fetchSpy = stubBackend([
      submitRoute(json(202, { video_id: "v1", status: "processing" })),
      statusRoute(
        json(200, {
          video_id: "v1",
          status: "processing",
          segments_total: 10,
          segments_done: 1,
        }),
      ),
    ]);

    const { result, unmount } = renderHook(() => useFactCheck("src", 0));
    await waitFor(() =>
      expect(result.current).toMatchObject({ status: "processing" }),
    );

    unmount();
    const callsAfterUnmount = fetchSpy.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 25));

    expect(fetchSpy.mock.calls.length).toBe(callsAfterUnmount);
  });
});

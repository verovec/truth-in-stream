import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import { json, stubBackend } from "@/test/fact-check";
import {
  getVideoAnalysis,
  listVideosWithAnalysis,
  startVideoAnalysis,
} from "./analysis";

afterEach(() => {
  vi.restoreAllMocks();
});

const videoWire = (overrides: Record<string, unknown> = {}) => ({
  id: "vid-1",
  title: "Common Myths",
  status: "ready",
  kind: "sample",
  content_type: "video/mp4",
  size_bytes: 0,
  created_at: "2026-06-10T18:00:00Z",
  updated_at: "2026-06-10T18:00:00Z",
  analysis_status: "none",
  ...overrides,
});

describe("listVideosWithAnalysis", () => {
  test("keeps the analysis fields the list payload carries", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos"),
        responses: [
          json(200, {
            videos: [
              videoWire({
                analysis_status: "complete",
                analyzed_at: "2026-07-17T09:00:00Z",
              }),
              videoWire({ id: "vid-2", analysis_status: "analysing" }),
            ],
          }),
        ],
      },
    ]);

    const videos = await listVideosWithAnalysis();

    expect(videos).toHaveLength(2);
    expect(videos[0]).toMatchObject({
      id: "vid-1",
      contentType: "video/mp4",
      analysisStatus: "complete",
      analyzedAt: "2026-07-17T09:00:00Z",
    });
    expect(videos[1]).toMatchObject({
      id: "vid-2",
      analysisStatus: "analysing",
      analyzedAt: null,
    });
  });

  test("normalizes an unknown or missing analysis status to none", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos"),
        responses: [
          json(200, {
            videos: [
              videoWire({ analysis_status: "future-state" }),
              videoWire({ id: "vid-2", analysis_status: undefined }),
            ],
          }),
        ],
      },
    ]);

    const videos = await listVideosWithAnalysis();
    expect(videos[0].analysisStatus).toBe("none");
    expect(videos[1].analysisStatus).toBe("none");
  });

  test("surfaces a backend failure as an ApiError", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos"),
        responses: [json(500, { error: "boom" })],
      },
    ]);

    await expect(listVideosWithAnalysis()).rejects.toMatchObject({
      status: 500,
      message: "boom",
    });
  });
});

describe("getVideoAnalysis", () => {
  test("normalizes a completed analysis and parses its frames through the shared live parser", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "complete",
            analyzed_at: "2026-07-17T09:00:00Z",
            analysis_runs: 2,
            analysis_progress_ms: 61_000,
            engine: { transcriber: "assemblyai-u3" },
            counters: { total: 3, credible: 1, disputed: 1, unverifiable: 1 },
            frames: [
              { type: "subtitle", id: "s1", start: 1, end: 2, text: "hello" },
              { type: "bogus-type" },
              "not-even-an-object",
            ],
          }),
        ],
      },
    ]);

    const analysis = await getVideoAnalysis("vid-1");

    expect(analysis).toMatchObject({
      analysisStatus: "complete",
      analysisError: null,
      analyzedAt: "2026-07-17T09:00:00Z",
      analysisRuns: 2,
      analysisProgressMs: 61_000,
      counters: { total: 3, credible: 1, disputed: 1, unverifiable: 1 },
    });
    // The malformed entries are dropped individually, exactly as the socket
    // path skips a stray frame; the good subtitle survives.
    expect(analysis.frames).toEqual([
      { type: "subtitle", id: "s1", start: 1, end: 2, text: "hello" },
    ]);
  });

  test("keeps frames null when the response carries none (analysing poll or degraded result)", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "analysing",
            analysis_runs: 1,
            analysis_progress_ms: 12_000,
          }),
        ],
      },
    ]);

    const analysis = await getVideoAnalysis("vid-1");
    expect(analysis.frames).toBeNull();
    expect(analysis.counters).toBeNull();
    expect(analysis.analysisProgressMs).toBe(12_000);
  });

  test("carries the failure reason of a failed run", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "failed",
            analysis_error: "backend restarted mid-run",
            analysis_runs: 1,
            analysis_progress_ms: 0,
          }),
        ],
      },
    ]);

    const analysis = await getVideoAnalysis("vid-1");
    expect(analysis.analysisStatus).toBe("failed");
    expect(analysis.analysisError).toBe("backend restarted mid-run");
  });

  test("rejects with the backend's error for an unknown video", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/nope/analysis"),
        responses: [json(404, { error: "unknown video" })],
      },
    ]);

    await expect(getVideoAnalysis("nope")).rejects.toMatchObject({
      status: 404,
      message: "unknown video",
    });
  });
});

describe("startVideoAnalysis", () => {
  test("POSTs the analyse trigger and resolves on 202", async () => {
    const fetchMock = stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/analyse") && init?.method === "POST",
        responses: [() => new Response(null, { status: 202 })],
      },
    ]);

    await expect(startVideoAnalysis("vid-1")).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test.each([
    [409, "analysis is already in progress"],
    [422, "video is not ready for analysis"],
    [404, "unknown video"],
  ])("surfaces a %i as a typed ApiError", async (status, message) => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analyse"),
        responses: [json(status, { error: message })],
      },
    ]);

    const err = await startVideoAnalysis("vid-1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status, message });
  });
});

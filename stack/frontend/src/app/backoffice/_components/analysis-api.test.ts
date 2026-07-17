import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { ApiError } from "@/lib/http";
import {
  getVideoAnalysis,
  listBackofficeVideos,
  startVideoAnalysis,
} from "./analysis-api";

afterEach(() => vi.restoreAllMocks());

const wireVideo = {
  id: "vid-1",
  title: "Common Myths",
  status: "ready",
  kind: "sample",
  content_type: "video/mp4",
  size_bytes: 10,
  created_at: "2026-06-10T18:00:00Z",
  updated_at: "2026-06-10T18:00:00Z",
};

describe("listBackofficeVideos", () => {
  test("carries the analysis lifecycle fields the shared client drops", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos") && init?.method === undefined,
        responses: [
          json(200, {
            videos: [
              {
                ...wireVideo,
                duration_ms: 120000,
                analysis_status: "complete",
                analyzed_at: "2026-07-16T09:00:00Z",
              },
              {
                ...wireVideo,
                id: "vid-2",
                analysis_status: "none",
              },
            ],
          }),
        ],
      },
    ]);

    const videos = await listBackofficeVideos();
    expect(videos).toHaveLength(2);
    expect(videos[0]).toMatchObject({
      id: "vid-1",
      contentType: "video/mp4",
      durationMs: 120000,
      analysisStatus: "complete",
      analyzedAt: "2026-07-16T09:00:00Z",
    });
    // Omitted optional fields normalize to null, not undefined.
    expect(videos[1].durationMs).toBeNull();
    expect(videos[1].analyzedAt).toBeNull();
    expect(videos[1].analysisStatus).toBe("none");
  });

  test("throws an ApiError on a failed list", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos"),
        responses: [json(500, { error: "internal error" })],
      },
    ]);
    await expect(listBackofficeVideos()).rejects.toMatchObject({
      status: 500,
      message: "internal error",
    });
  });
});

describe("getVideoAnalysis", () => {
  test("parses lifecycle, progress, and counters, ignoring frames", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "complete",
            analyzed_at: "2026-07-16T09:00:00Z",
            analysis_runs: 2,
            analysis_progress_ms: 120000,
            counters: { total: 8, credible: 4, disputed: 3, unverifiable: 1 },
            frames: [{ type: "claim_result" }],
          }),
        ],
      },
    ]);

    const detail = await getVideoAnalysis("vid-1");
    expect(detail).toEqual({
      analysisStatus: "complete",
      analysisError: null,
      analyzedAt: "2026-07-16T09:00:00Z",
      analysisRuns: 2,
      analysisProgressMs: 120000,
      counters: { total: 8, credible: 4, disputed: 3, unverifiable: 1 },
    });
    expect(detail).not.toHaveProperty("frames");
  });

  test("maps an in-flight run's sparse payload to nulls", async () => {
    stubBackend([
      {
        match: (url) => url.endsWith("/api/videos/vid-1/analysis"),
        responses: [
          json(200, {
            analysis_status: "analysing",
            analysis_runs: 0,
            analysis_progress_ms: 5000,
          }),
        ],
      },
    ]);

    const detail = await getVideoAnalysis("vid-1");
    expect(detail).toEqual({
      analysisStatus: "analysing",
      analysisError: null,
      analyzedAt: null,
      analysisRuns: 0,
      analysisProgressMs: 5000,
      counters: null,
    });
  });
});

describe("startVideoAnalysis", () => {
  test("resolves on a 202 accept with an empty body", async () => {
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/analyse") && init?.method === "POST",
        responses: [() => new Response(null, { status: 202 })],
      },
    ]);
    await expect(startVideoAnalysis("vid-1")).resolves.toBeUndefined();
  });

  test.each([
    [409, "analysis is already in progress"],
    [422, "video is not ready for analysis"],
    [404, "unknown video"],
  ])("surfaces a %i as an ApiError with the backend message", async (status, message) => {
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

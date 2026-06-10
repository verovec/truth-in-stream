import { describe, expect, test, vi } from "vitest";
import {
  ApiError,
  fetchVideoResults,
  fetchVideoStatus,
  submitVideo,
} from "./api";

function mockFetch(status: number, body: unknown) {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

describe("submitVideo", () => {
  test("posts the source and returns the submission", async () => {
    const fetchSpy = mockFetch(202, {
      video_id: "abc123",
      status: "processing",
    });

    const submission = await submitVideo("https://example.com/v.mp4");

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source: "https://example.com/v.mp4" }),
      signal: undefined,
    });
    expect(submission).toEqual({ videoId: "abc123", status: "processing" });
  });

  test("returns a complete submission for cached videos", async () => {
    mockFetch(200, { video_id: "abc123", status: "complete" });

    const submission = await submitVideo("https://example.com/v.mp4");

    expect(submission).toEqual({ videoId: "abc123", status: "complete" });
  });

  test("throws an ApiError carrying the backend message", async () => {
    mockFetch(503, { error: "processing queue is full, retry later" });

    await expect(submitVideo("https://example.com/v.mp4")).rejects.toThrow(
      new ApiError("processing queue is full, retry later", 503),
    );
  });
});

describe("fetchVideoStatus", () => {
  test("returns mapped progress", async () => {
    const fetchSpy = mockFetch(200, {
      video_id: "abc123",
      status: "processing",
      segments_total: 12,
      segments_done: 5,
    });

    const status = await fetchVideoStatus("abc123");

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/abc123/status", {
      signal: undefined,
    });
    expect(status).toEqual({
      videoId: "abc123",
      status: "processing",
      segmentsTotal: 12,
      segmentsDone: 5,
      error: undefined,
    });
  });

  test("carries the failure reason for failed runs", async () => {
    mockFetch(200, {
      video_id: "abc123",
      status: "failed",
      segments_total: 12,
      segments_done: 5,
      error: "transcription unavailable",
    });

    const status = await fetchVideoStatus("abc123");

    expect(status.status).toBe("failed");
    expect(status.error).toBe("transcription unavailable");
  });

  test("throws an ApiError for an unknown video", async () => {
    mockFetch(404, { error: "unknown video" });

    await expect(fetchVideoStatus("nope")).rejects.toThrow(
      new ApiError("unknown video", 404),
    );
  });
});

describe("fetchVideoResults", () => {
  test("returns complete segments ordered as served", async () => {
    const fetchSpy = mockFetch(200, {
      video_id: "abc123",
      segments: [
        {
          start: 0,
          end: 4.5,
          text: "hello world",
          matches: [
            {
              kind: "claim",
              claim: "the world exists",
              verdict: "corroborates",
              sources: [{ title: "Source", url: "https://example.com" }],
              similarity: 0.92,
            },
          ],
        },
        { start: 4.5, end: 9, text: "quiet part", matches: [] },
      ],
    });

    const outcome = await fetchVideoResults("abc123");

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/abc123/results", {
      signal: undefined,
    });
    expect(outcome).toEqual({
      kind: "complete",
      segments: [
        {
          start: 0,
          end: 4.5,
          text: "hello world",
          matches: [
            {
              kind: "claim",
              claim: "the world exists",
              verdict: "corroborates",
              sources: [{ title: "Source", url: "https://example.com" }],
              similarity: 0.92,
            },
          ],
        },
        { start: 4.5, end: 9, text: "quiet part", matches: [] },
      ],
    });
  });

  test("normalizes an evidence match into an excerpt with attribution", async () => {
    mockFetch(200, {
      video_id: "abc123",
      segments: [
        {
          start: 0,
          end: 4,
          text: "the great wall",
          matches: [
            {
              kind: "evidence",
              claim: "The Great Wall of China is a series of fortifications.",
              sources: [],
              similarity: 0.74,
              article: {
                title: "Great Wall of China",
                url: "https://en.wikipedia.org/wiki/Great_Wall_of_China",
              },
            },
          ],
        },
      ],
    });

    const outcome = await fetchVideoResults("abc123");

    expect(outcome).toEqual({
      kind: "complete",
      segments: [
        {
          start: 0,
          end: 4,
          text: "the great wall",
          matches: [
            {
              kind: "evidence",
              excerpt:
                "The Great Wall of China is a series of fortifications.",
              article: {
                title: "Great Wall of China",
                url: "https://en.wikipedia.org/wiki/Great_Wall_of_China",
              },
              similarity: 0.74,
            },
          ],
        },
      ],
    });
  });

  test("keeps a malformed evidence match as evidence, never a fabricated verdict", async () => {
    mockFetch(200, {
      video_id: "abc123",
      segments: [
        {
          start: 0,
          end: 4,
          text: "the great wall",
          matches: [
            {
              kind: "evidence",
              claim: "The Great Wall of China is a series of fortifications.",
              sources: [],
              similarity: 0.74,
            },
          ],
        },
      ],
    });

    const outcome = await fetchVideoResults("abc123");

    expect(outcome).toEqual({
      kind: "complete",
      segments: [
        {
          start: 0,
          end: 4,
          text: "the great wall",
          matches: [
            {
              kind: "evidence",
              excerpt:
                "The Great Wall of China is a series of fortifications.",
              article: {
                title: "Wikipedia",
                url: "https://www.wikipedia.org",
              },
              similarity: 0.74,
            },
          ],
        },
      ],
    });
  });

  test("reads a legacy match without a kind as a claim", async () => {
    mockFetch(200, {
      video_id: "abc123",
      segments: [
        {
          start: 0,
          end: 4,
          text: "legacy",
          matches: [
            {
              claim: "stored before evidence existed",
              verdict: "unclear",
              sources: [{ title: "Old", url: "https://old.example" }],
              similarity: 0.5,
            },
          ],
        },
      ],
    });

    const outcome = await fetchVideoResults("abc123");

    expect(outcome).toEqual({
      kind: "complete",
      segments: [
        {
          start: 0,
          end: 4,
          text: "legacy",
          matches: [
            {
              kind: "claim",
              claim: "stored before evidence existed",
              verdict: "unclear",
              sources: [{ title: "Old", url: "https://old.example" }],
              similarity: 0.5,
            },
          ],
        },
      ],
    });
  });

  test("maps 409 to a pending outcome", async () => {
    mockFetch(409, { error: "processing has not completed" });

    await expect(fetchVideoResults("abc123")).resolves.toEqual({
      kind: "pending",
    });
  });

  test("maps 404 to a pending outcome", async () => {
    mockFetch(404, { error: "unknown video" });

    await expect(fetchVideoResults("abc123")).resolves.toEqual({
      kind: "pending",
    });
  });

  test("throws an ApiError on server failure", async () => {
    mockFetch(500, { error: "internal error" });

    await expect(fetchVideoResults("abc123")).rejects.toThrow(
      new ApiError("internal error", 500),
    );
  });

  test("throws an ApiError when the error body is not JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("<html>bad gateway</html>", { status: 502 }),
    );

    await expect(fetchVideoResults("abc123")).rejects.toThrow(
      new ApiError("request failed with status 502", 502),
    );
  });
});

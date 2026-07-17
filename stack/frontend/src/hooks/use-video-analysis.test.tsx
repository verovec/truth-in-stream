import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import type { LiveFrame } from "@/lib/live/frames";
import type { VideoAnalysis } from "@/lib/video/analysis";
import { type TrackedVideo, useVideoAnalysis } from "./use-video-analysis";

const FRAME: LiveFrame = {
  type: "subtitle",
  id: "s1",
  start: 0,
  end: 2,
  text: "hello",
};

function analysis(overrides: Partial<VideoAnalysis> = {}): VideoAnalysis {
  return {
    analysisStatus: "complete",
    analysisError: null,
    analyzedAt: "2026-07-17T09:00:00Z",
    analysisRuns: 1,
    analysisProgressMs: 0,
    engine: null,
    counters: null,
    frames: [FRAME],
    ...overrides,
  };
}

function harness({
  video,
  loadAnalysis,
  startAnalysis = vi.fn(async () => {}),
}: {
  video: TrackedVideo | null;
  loadAnalysis: (id: string, signal?: AbortSignal) => Promise<VideoAnalysis>;
  startAnalysis?: (id: string, signal?: AbortSignal) => Promise<void>;
}) {
  const onStatusChange = vi.fn();
  const rendered = renderHook(
    (props: { video: TrackedVideo | null }) =>
      useVideoAnalysis({
        video: props.video,
        onStatusChange,
        loadAnalysis,
        startAnalysis,
        pollIntervalMs: 2000,
      }),
    { initialProps: { video } },
  );
  return { ...rendered, onStatusChange, startAnalysis };
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("useVideoAnalysis", () => {
  test("fetches a complete video's stored frames once and never polls", async () => {
    vi.useFakeTimers();
    const loadAnalysis = vi.fn(async () => analysis());
    const h = harness({
      video: { id: "vid-1", analysisStatus: "complete" },
      loadAnalysis,
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(h.result.current.frames).toEqual([FRAME]);
    expect(h.result.current.loadFailed).toBe(false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(loadAnalysis).toHaveBeenCalledTimes(1);
    expect(h.onStatusChange).not.toHaveBeenCalled();
  });

  test("does not fetch at all for a video that was never analysed", async () => {
    vi.useFakeTimers();
    const loadAnalysis = vi.fn(async () => analysis());
    harness({ video: { id: "vid-1", analysisStatus: "none" }, loadAnalysis });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(loadAnalysis).not.toHaveBeenCalled();
  });

  test("marks the load failed when a complete video's analysis cannot be fetched, and retryLoad re-fetches", async () => {
    const loadAnalysis = vi
      .fn<(id: string) => Promise<VideoAnalysis>>()
      .mockRejectedValueOnce(new ApiError("internal error", 500))
      .mockResolvedValueOnce(analysis());
    const h = harness({
      video: { id: "vid-1", analysisStatus: "complete" },
      loadAnalysis,
    });

    await waitFor(() => expect(h.result.current.loadFailed).toBe(true));
    expect(h.result.current.frames).toBeNull();

    act(() => h.result.current.retryLoad());
    await waitFor(() => expect(h.result.current.frames).toEqual([FRAME]));
    expect(h.result.current.loadFailed).toBe(false);
  });

  test("marks the load failed when a complete response carries no frames", async () => {
    const loadAnalysis = vi.fn(async () => analysis({ frames: null }));
    const h = harness({
      video: { id: "vid-1", analysisStatus: "complete" },
      loadAnalysis,
    });

    await waitFor(() => expect(h.result.current.loadFailed).toBe(true));
  });

  test("polls an analysing video every 2 s, surfacing progress, and stops at completion with the frames", async () => {
    vi.useFakeTimers();
    const responses = [
      analysis({ analysisStatus: "analysing", analysisProgressMs: 5000, frames: null, analyzedAt: null }),
      analysis({ analysisStatus: "analysing", analysisProgressMs: 10_000, frames: null, analyzedAt: null }),
      analysis({ analysisStatus: "complete", analysisProgressMs: 60_000 }),
    ];
    const loadAnalysis = vi.fn(async () => responses.shift() ?? analysis());
    const h = harness({
      video: { id: "vid-1", analysisStatus: "analysing" },
      loadAnalysis,
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(h.result.current.progressMs).toBe(5000);
    expect(loadAnalysis).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(h.result.current.progressMs).toBe(10_000);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(h.result.current.frames).toEqual([FRAME]);
    expect(h.onStatusChange).toHaveBeenCalledWith(
      "vid-1",
      "complete",
      "2026-07-17T09:00:00Z",
    );

    // Terminal: the completing response schedules no further tick.
    const calls = loadAnalysis.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(loadAnalysis).toHaveBeenCalledTimes(calls);
  });

  test("keeps already-hydrated frames when a re-analysis poll reports analysing without frames", async () => {
    vi.useFakeTimers();
    const loadAnalysis = vi
      .fn<(id: string) => Promise<VideoAnalysis>>()
      .mockResolvedValueOnce(analysis())
      .mockResolvedValue(
        analysis({
          analysisStatus: "analysing",
          analysisProgressMs: 3000,
          frames: null,
          analyzedAt: null,
        }),
      );
    const h = harness({
      video: { id: "vid-1", analysisStatus: "complete" },
      loadAnalysis,
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(h.result.current.frames).toEqual([FRAME]);

    // The backoffice re-ran the analysis: the list now says analysing. The poll
    // sees no frames in the response but the stored result stays hydrated.
    h.rerender({ video: { id: "vid-1", analysisStatus: "analysing" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(h.result.current.progressMs).toBe(3000);
    expect(h.result.current.frames).toEqual([FRAME]);
  });

  test("fetches the failure reason once for a failed video", async () => {
    const loadAnalysis = vi.fn(async () =>
      analysis({
        analysisStatus: "failed",
        analysisError: "backend restarted mid-run",
        frames: null,
        analyzedAt: null,
      }),
    );
    const h = harness({
      video: { id: "vid-1", analysisStatus: "failed" },
      loadAnalysis,
    });

    await waitFor(() =>
      expect(h.result.current.analysisError).toBe("backend restarted mid-run"),
    );
    expect(loadAnalysis).toHaveBeenCalledTimes(1);
  });

  test("start() reports the accepted run upward and resets progress", async () => {
    const loadAnalysis = vi.fn(async () => analysis());
    const startAnalysis = vi.fn(async () => {});
    const h = harness({
      video: { id: "vid-1", analysisStatus: "none" },
      loadAnalysis,
      startAnalysis,
    });

    act(() => h.result.current.start());
    await waitFor(() => expect(h.result.current.starting).toBe(false));

    expect(startAnalysis).toHaveBeenCalledWith("vid-1");
    expect(h.onStatusChange).toHaveBeenCalledWith("vid-1", "analysing");
    expect(h.result.current.startError).toBeNull();
  });

  test.each([
    [409, "conflict"],
    [422, "notReady"],
    [403, "forbidden"],
    [500, "failed"],
  ] as const)("start() classifies a %i as %s", async (status, expected) => {
    const loadAnalysis = vi.fn(async () => analysis());
    const startAnalysis = vi.fn(async () => {
      throw new ApiError("nope", status);
    });
    const h = harness({
      video: { id: "vid-1", analysisStatus: "none" },
      loadAnalysis,
      startAnalysis,
    });

    act(() => h.result.current.start());
    await waitFor(() => expect(h.result.current.startError).toBe(expected));

    if (expected === "conflict") {
      // Someone else's run is already in flight: adopt it so the poll shows it.
      expect(h.onStatusChange).toHaveBeenCalledWith("vid-1", "analysing");
    } else {
      expect(h.onStatusChange).not.toHaveBeenCalled();
    }
  });

  test("unmounting stops the poll", async () => {
    vi.useFakeTimers();
    const loadAnalysis = vi.fn(async () =>
      analysis({
        analysisStatus: "analysing",
        analysisProgressMs: 1000,
        frames: null,
        analyzedAt: null,
      }),
    );
    const h = harness({
      video: { id: "vid-1", analysisStatus: "analysing" },
      loadAnalysis,
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    const calls = loadAnalysis.mock.calls.length;
    expect(calls).toBeGreaterThan(0);

    h.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(loadAnalysis).toHaveBeenCalledTimes(calls);
  });

  test("switching videos resets the track", async () => {
    const loadAnalysis = vi.fn(async () => analysis());
    const h = harness({
      video: { id: "vid-1", analysisStatus: "complete" },
      loadAnalysis,
    });
    await waitFor(() => expect(h.result.current.frames).toEqual([FRAME]));

    h.rerender({ video: { id: "vid-2", analysisStatus: "none" } });
    expect(h.result.current.frames).toBeNull();
    expect(h.result.current.progressMs).toBe(0);
  });
});

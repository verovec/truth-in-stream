import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import type { BackofficeVideo, VideoAnalysisDetail } from "./analysis-api";
import { BackofficeVideoList } from "./backoffice-video-list";

const list = fr.app.backoffice.videos.list;
const analysis = list.analysis;

function video(over: Partial<BackofficeVideo> = {}): BackofficeVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    durationMs: null,
    analysisStatus: "none",
    analyzedAt: null,
    ...over,
  };
}

function detail(over: Partial<VideoAnalysisDetail> = {}): VideoAnalysisDetail {
  return {
    analysisStatus: "none",
    analysisError: null,
    analyzedAt: null,
    analysisRuns: 0,
    analysisProgressMs: 0,
    counters: null,
    ...over,
  };
}

function renderList(
  videos: BackofficeVideo[],
  overrides: Partial<Parameters<typeof BackofficeVideoList>[0]> = {},
) {
  return render(
    <BackofficeVideoList
      videos={videos}
      remove={vi.fn()}
      onDeleted={vi.fn()}
      startAnalysis={vi.fn(async () => {})}
      onAnalysisStarted={vi.fn()}
      loadAnalysis={vi.fn(async () => detail())}
      pollIntervalMs={10}
      {...overrides}
    />,
  );
}

afterEach(() => vi.restoreAllMocks());

describe("BackofficeVideoList", () => {
  test("shows the empty state when there are no videos", () => {
    renderList([]);
    expect(screen.getByText(list.empty)).toBeInTheDocument();
  });

  test("renders each video's title with kind and status badges", () => {
    renderList([
      video(),
      video({
        id: "vid-2",
        title: "Town Hall",
        kind: "youtube",
        status: "pending",
      }),
    ]);
    expect(screen.getByText("Common Myths")).toBeInTheDocument();
    expect(screen.getByText("Town Hall")).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.sample)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.youtube)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.status.pending)).toBeInTheDocument();
  });

  test("confirms before deleting, then calls remove and onDeleted", async () => {
    const remove = vi.fn().mockResolvedValue(undefined);
    const onDeleted = vi.fn();
    renderList([video({ id: "vid-2", title: "Town Hall", kind: "youtube" })], {
      remove,
      onDeleted,
    });

    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    // The two-step confirm does not fire until confirmed.
    expect(remove).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: list.confirmYes }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("vid-2"));
    expect(onDeleted).toHaveBeenCalledTimes(1);
  });

  test("cancelling the confirm deletes nothing and restores the delete control", () => {
    const remove = vi.fn();
    renderList([video()], { remove });
    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    fireEvent.click(screen.getByRole("button", { name: list.confirmNo }));
    expect(remove).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: list.delete }),
    ).toBeInTheDocument();
  });

  test("surfaces a failed delete inline without calling onDeleted", async () => {
    const remove = vi.fn().mockRejectedValue(new ApiError("boom", 500));
    const onDeleted = vi.fn();
    renderList([video()], { remove, onDeleted });

    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    fireEvent.click(screen.getByRole("button", { name: list.confirmYes }));

    expect(
      await screen.findByText(
        formatTemplate(list.deleteError, { message: "boom" }),
      ),
    ).toBeInTheDocument();
    expect(onDeleted).not.toHaveBeenCalled();
  });

  test("an un-analysed row stays quiet and offers a direct Analyse action", () => {
    renderList([video()]);
    expect(screen.queryByText(analysis.badge.complete)).not.toBeInTheDocument();
    expect(screen.queryByText(analysis.badge.failed)).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: analysis.analyse }),
    ).toBeInTheDocument();
  });

  test("a non-ready row offers no analyse action", () => {
    renderList([video({ status: "pending", kind: "youtube" })]);
    expect(
      screen.queryByRole("button", { name: analysis.analyse }),
    ).not.toBeInTheDocument();
  });

  test("an analysing row shows the live percentage from progress over duration", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({ analysisStatus: "analysing", analysisProgressMs: 25000 }),
    );
    renderList([video({ analysisStatus: "analysing", durationMs: 100000 })], {
      loadAnalysis,
    });

    expect(
      await screen.findByText(
        formatTemplate(analysis.badge.analysingPct, { pct: 25 }),
      ),
    ).toBeInTheDocument();
    // No action while a run is live.
    expect(
      screen.queryByRole("button", { name: analysis.analyse }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: analysis.reanalyse }),
    ).not.toBeInTheDocument();
  });

  test("an analysing row without a known duration shows the indeterminate label", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({ analysisStatus: "analysing", analysisProgressMs: 25000 }),
    );
    renderList([video({ analysisStatus: "analysing", durationMs: null })], {
      loadAnalysis,
    });

    expect(screen.getByText(analysis.badge.analysing)).toBeInTheDocument();
    await waitFor(() => expect(loadAnalysis).toHaveBeenCalled());
    expect(screen.getByText(analysis.badge.analysing)).toBeInTheDocument();
  });

  test("a completed row shows the badge, the date, and the claim counters", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({
        analysisStatus: "complete",
        analyzedAt: "2026-07-16T09:00:00Z",
        analysisRuns: 1,
        counters: { total: 8, credible: 4, disputed: 3, unverifiable: 1 },
      }),
    );
    renderList(
      [
        video({
          analysisStatus: "complete",
          analyzedAt: "2026-07-16T09:00:00Z",
          durationMs: 100000,
        }),
      ],
      { loadAnalysis },
    );

    expect(screen.getByText(analysis.badge.complete)).toBeInTheDocument();
    expect(
      await screen.findByText(
        new RegExp(
          formatTemplate(analysis.counts, {
            total: 8,
            credible: 4,
            disputed: 3,
            unverifiable: 1,
          }).replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
        ),
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        new RegExp(
          formatTemplate(analysis.analysedOn, {
            date: new Date("2026-07-16T09:00:00Z").toLocaleString("fr", {
              dateStyle: "medium",
              timeStyle: "short",
            }),
          }).replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
        ),
      ),
    ).toBeInTheDocument();
  });

  test("a failed row surfaces the stored analysis error", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({
        analysisStatus: "failed",
        analysisError: "transcriber connection lost",
      }),
    );
    renderList([video({ analysisStatus: "failed" })], { loadAnalysis });

    expect(screen.getByText(analysis.badge.failed)).toBeInTheDocument();
    expect(
      await screen.findByText(
        formatTemplate(analysis.failedError, {
          message: "transcriber connection lost",
        }),
      ),
    ).toBeInTheDocument();
    // A failed run restarts directly - nothing of value would be overwritten.
    expect(
      screen.getByRole("button", { name: analysis.retry }),
    ).toBeInTheDocument();
  });

  test("Analyse fires directly and reports the started run", async () => {
    const startAnalysis = vi.fn(async () => {});
    const onAnalysisStarted = vi.fn();
    renderList([video()], { startAnalysis, onAnalysisStarted });

    fireEvent.click(screen.getByRole("button", { name: analysis.analyse }));
    await waitFor(() => expect(startAnalysis).toHaveBeenCalledWith("vid-1"));
    expect(onAnalysisStarted).toHaveBeenCalledWith("vid-1");
  });

  test("Re-analyse asks for confirmation and only fires once confirmed", async () => {
    const startAnalysis = vi.fn(async () => {});
    const onAnalysisStarted = vi.fn();
    renderList(
      [
        video({
          analysisStatus: "complete",
          analyzedAt: "2026-07-16T09:00:00Z",
        }),
      ],
      { startAnalysis, onAnalysisStarted },
    );

    fireEvent.click(screen.getByRole("button", { name: analysis.reanalyse }));
    expect(startAnalysis).not.toHaveBeenCalled();
    expect(screen.getByText(analysis.confirm)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: analysis.confirmYes }));
    await waitFor(() => expect(startAnalysis).toHaveBeenCalledWith("vid-1"));
    expect(onAnalysisStarted).toHaveBeenCalledWith("vid-1");
  });

  test("cancelling the re-analyse confirm fires nothing and restores the control", () => {
    const startAnalysis = vi.fn(async () => {});
    renderList(
      [
        video({
          analysisStatus: "complete",
          analyzedAt: "2026-07-16T09:00:00Z",
        }),
      ],
      { startAnalysis },
    );

    fireEvent.click(screen.getByRole("button", { name: analysis.reanalyse }));
    fireEvent.click(screen.getByRole("button", { name: analysis.confirmNo }));
    expect(startAnalysis).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: analysis.reanalyse }),
    ).toBeInTheDocument();
  });

  test("a 409 conflict explains itself and reports the live run", async () => {
    const startAnalysis = vi
      .fn()
      .mockRejectedValue(new ApiError("analysis is already in progress", 409));
    const onAnalysisStarted = vi.fn();
    renderList([video()], { startAnalysis, onAnalysisStarted });

    fireEvent.click(screen.getByRole("button", { name: analysis.analyse }));
    expect(
      await screen.findByText(analysis.errors.conflict),
    ).toBeInTheDocument();
    expect(onAnalysisStarted).toHaveBeenCalledWith("vid-1");
  });

  test("a 422 not-ready answer explains itself without reporting a run", async () => {
    const startAnalysis = vi
      .fn()
      .mockRejectedValue(new ApiError("video is not ready for analysis", 422));
    const onAnalysisStarted = vi.fn();
    renderList([video()], { startAnalysis, onAnalysisStarted });

    fireEvent.click(screen.getByRole("button", { name: analysis.analyse }));
    expect(
      await screen.findByText(analysis.errors.notReady),
    ).toBeInTheDocument();
    expect(onAnalysisStarted).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: analysis.analyse }),
    ).toBeInTheDocument();
  });

  test("an analysing row polls its detail and stops once the run is terminal", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({ analysisStatus: "analysing", analysisProgressMs: 25000 }),
    );
    const analysing = video({ analysisStatus: "analysing", durationMs: 100000 });
    const props = {
      remove: vi.fn(),
      onDeleted: vi.fn(),
      startAnalysis: vi.fn(async () => {}),
      onAnalysisStarted: vi.fn(),
      loadAnalysis,
      pollIntervalMs: 10,
    };
    const { rerender } = render(
      <BackofficeVideoList videos={[analysing]} {...props} />,
    );

    // The poll keeps reading progress while the row is analysing.
    await waitFor(() =>
      expect(loadAnalysis.mock.calls.length).toBeGreaterThan(2),
    );

    loadAnalysis.mockImplementation(async () =>
      detail({
        analysisStatus: "complete",
        analyzedAt: "2026-07-16T09:00:00Z",
        counters: { total: 2, credible: 1, disputed: 1, unverifiable: 0 },
      }),
    );
    rerender(
      <BackofficeVideoList
        videos={[
          video({
            analysisStatus: "complete",
            analyzedAt: "2026-07-16T09:00:00Z",
            durationMs: 100000,
          }),
        ]}
        {...props}
      />,
    );

    // One terminal fetch brings the counters, then the polling stops.
    await waitFor(() =>
      expect(
        screen.getByText(new RegExp("2 affirmations")),
      ).toBeInTheDocument(),
    );
    const settled = loadAnalysis.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(loadAnalysis.mock.calls.length).toBe(settled);
  });

  test("a re-analyse starting clears the finished run's progress from the badge", async () => {
    const loadAnalysis = vi.fn(async () =>
      detail({
        analysisStatus: "complete",
        analyzedAt: "2026-07-16T09:00:00Z",
        analysisProgressMs: 100000,
        counters: { total: 2, credible: 1, disputed: 1, unverifiable: 0 },
      }),
    );
    const props = {
      remove: vi.fn(),
      onDeleted: vi.fn(),
      startAnalysis: vi.fn(async () => {}),
      onAnalysisStarted: vi.fn(),
      loadAnalysis,
      pollIntervalMs: 10000,
    };
    const completed = video({
      analysisStatus: "complete",
      analyzedAt: "2026-07-16T09:00:00Z",
      durationMs: 100000,
    });
    const { rerender } = render(
      <BackofficeVideoList videos={[completed]} {...props} />,
    );
    await waitFor(() => expect(loadAnalysis).toHaveBeenCalled());

    // The old run's detail (progress at 100 %) must not leak into the new
    // run's badge: the row shows the indeterminate label until a fresh
    // progress read lands.
    loadAnalysis.mockImplementation(
      () => new Promise<VideoAnalysisDetail>(() => {}),
    );
    rerender(
      <BackofficeVideoList
        videos={[{ ...completed, analysisStatus: "analysing" }]}
        {...props}
      />,
    );
    expect(screen.getByText(analysis.badge.analysing)).toBeInTheDocument();
    expect(
      screen.queryByText(
        formatTemplate(analysis.badge.analysingPct, { pct: 100 }),
      ),
    ).not.toBeInTheDocument();
  });
});

import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import type { PutUploader } from "@/lib/video/upload";
import type {
  AnalysedLibraryVideo,
  VideoAnalysis,
} from "@/lib/video/analysis";
import { BackofficeVideosSection } from "./backoffice-videos-section";

const list = fr.app.backoffice.videos.list;
const analysis = list.analysis;

function videoRecord(overrides: Partial<AnalysedLibraryVideo> = {}): AnalysedLibraryVideo {
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
    ...overrides,
  };
}

function analysisDetail(
  overrides: Partial<VideoAnalysis> = {},
): VideoAnalysis {
  return {
    analysisStatus: "none",
    analysisError: null,
    analyzedAt: null,
    analysisRuns: 0,
    analysisProgressMs: 0,
    engine: null,
    counters: null,
    frames: null,
    ...overrides,
  };
}

afterEach(() => vi.restoreAllMocks());

describe("BackofficeVideosSection", () => {
  test("uploading a file confirms it and refreshes the management list", async () => {
    const confirmed = videoRecord({
      id: "vid-9",
      title: "Holiday",
      kind: "upload",
    });
    const loadVideos = vi
      .fn<() => Promise<AnalysedLibraryVideo[]>>()
      .mockResolvedValueOnce([])
      .mockResolvedValue([confirmed]);
    stubBackend([
      {
        match: (url, init) =>
          url.endsWith("/api/videos/uploads") && init?.method === "POST",
        responses: [
          json(201, {
            video_id: "vid-9",
            object_key: "uploads/vid-9.mp4",
            status: "pending",
            upload: { url: "https://storage/put", method: "PUT", headers: {} },
          }),
        ],
      },
      {
        match: (url) => url.endsWith("/api/videos/vid-9/confirm"),
        responses: [
          json(200, {
            id: "vid-9",
            title: "Holiday",
            status: "ready",
            kind: "upload",
            content_type: "video/mp4",
            size_bytes: 20,
            created_at: "2026-06-11T10:00:00Z",
            updated_at: "2026-06-11T10:00:00Z",
          }),
        ],
      },
    ]);
    const uploader: PutUploader = async (_p, _f, onProgress) => {
      onProgress(10, 10);
    };

    render(
      <BackofficeVideosSection loadVideos={loadVideos} uploader={uploader} />,
    );

    await waitFor(() => {
      expect(screen.getByText(list.empty)).toBeInTheDocument();
    });

    await userEvent.upload(
      screen.getByLabelText(fr.app.uploader.inputAria),
      new File(["x".repeat(20)], "Holiday.mp4", { type: "video/mp4" }),
    );

    // The confirmed upload appears as a management row (with a delete control),
    // fetched by the post-confirm refresh.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: list.delete }),
      ).toBeInTheDocument();
    });
    expect(screen.getByText("Holiday")).toBeInTheDocument();
  });

  test("importing a YouTube link lists it pending, then polling flips it to ready", async () => {
    // Everything is injected, so no network should happen; an empty stub makes
    // any stray fetch fail loudly.
    stubBackend([]);
    const pending = videoRecord({
      id: "vid-yt",
      title: "Town Hall",
      status: "pending",
      kind: "youtube",
    });
    const ready = videoRecord({
      id: "vid-yt",
      title: "Town Hall",
      status: "ready",
      kind: "youtube",
    });
    const loadVideos = vi
      .fn<() => Promise<AnalysedLibraryVideo[]>>()
      .mockResolvedValueOnce([])
      .mockResolvedValue([pending]);
    const submitYoutube = vi.fn(async () => pending);
    const pollVideos = vi.fn(async () => [ready]);

    render(
      <BackofficeVideosSection
        loadVideos={loadVideos}
        submitYoutube={submitYoutube}
        pollVideos={pollVideos}
        pollIntervalMs={10}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText(list.empty)).toBeInTheDocument();
    });

    await userEvent.type(
      screen.getByLabelText(fr.app.youtube.label),
      "https://youtu.be/townhall",
    );
    await userEvent.click(
      screen.getByRole("button", { name: fr.app.youtube.add }),
    );

    // The refresh lists the pending row.
    await waitFor(() => {
      expect(
        screen.getByText(fr.app.library.status.pending),
      ).toBeInTheDocument();
    });
    // Polling advances it to ready on its own.
    await waitFor(() => {
      expect(screen.getByText(fr.app.library.status.ready)).toBeInTheDocument();
    });
    expect(submitYoutube).toHaveBeenCalledWith(
      "https://youtu.be/townhall",
      expect.anything(),
    );
  });

  test("deleting a video removes it after the list refreshes", async () => {
    stubBackend([]);
    const keep = videoRecord({ id: "vid-1", title: "Common Myths", kind: "sample" });
    const target = videoRecord({
      id: "vid-2",
      title: "Town Hall",
      kind: "youtube",
    });
    const remove = vi.fn(async () => {});
    const loadVideos = vi
      .fn<() => Promise<AnalysedLibraryVideo[]>>()
      .mockResolvedValueOnce([keep, target])
      .mockResolvedValue([keep]);

    render(<BackofficeVideosSection loadVideos={loadVideos} remove={remove} />);

    await waitFor(() => {
      expect(screen.getByText("Town Hall")).toBeInTheDocument();
    });
    expect(screen.getByText("Common Myths")).toBeInTheDocument();

    // Delete the Town Hall row via its own two-step confirm.
    const row = screen.getByText("Town Hall").closest("li");
    expect(row).not.toBeNull();
    const rowScope = within(row as HTMLElement);
    fireEvent.click(rowScope.getByRole("button", { name: list.delete }));
    fireEvent.click(rowScope.getByRole("button", { name: list.confirmYes }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("vid-2"));
    // The refresh re-lists without Town Hall; the other row stays.
    await waitFor(() => {
      expect(screen.queryByText("Town Hall")).not.toBeInTheDocument();
    });
    expect(screen.getByText("Common Myths")).toBeInTheDocument();
  });

  test("a failed delete surfaces an inline error and keeps the row", async () => {
    stubBackend([]);
    const target = videoRecord({
      id: "vid-2",
      title: "Town Hall",
      kind: "youtube",
    });
    const remove = vi.fn().mockRejectedValue(new ApiError("nope", 500));
    const loadVideos = vi.fn(async () => [target]);

    render(<BackofficeVideosSection loadVideos={loadVideos} remove={remove} />);

    await waitFor(() => {
      expect(screen.getByText("Town Hall")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    fireEvent.click(screen.getByRole("button", { name: list.confirmYes }));

    expect(
      await screen.findByText(
        formatTemplate(list.deleteError, { message: "nope" }),
      ),
    ).toBeInTheDocument();
    // The row is not removed on a failed delete.
    expect(screen.getByText("Town Hall")).toBeInTheDocument();
  });

  test("an analysing row polls the catalog until the run completes, then stops", async () => {
    stubBackend([]);
    const analysing = videoRecord({
      analysisStatus: "analysing",
      durationMs: 100000,
    });
    const complete = videoRecord({
      analysisStatus: "complete",
      analyzedAt: "2026-07-16T09:00:00Z",
      durationMs: 100000,
    });
    const loadVideos = vi.fn(async () => [analysing]);
    const pollVideos = vi
      .fn<() => Promise<AnalysedLibraryVideo[]>>()
      .mockResolvedValueOnce([analysing])
      .mockResolvedValue([complete]);
    const loadAnalysis = vi
      .fn<() => Promise<VideoAnalysis>>()
      .mockImplementation(async () =>
        analysisDetail({
          analysisStatus: "analysing",
          analysisProgressMs: 25000,
        }),
      );

    render(
      <BackofficeVideosSection
        loadVideos={loadVideos}
        pollVideos={pollVideos}
        loadAnalysis={loadAnalysis}
        pollIntervalMs={10}
      />,
    );

    // Progress renders from the per-id detail against the known duration.
    expect(
      await screen.findByText(
        formatTemplate(analysis.badge.analysingPct, { pct: 25 }),
      ),
    ).toBeInTheDocument();

    loadAnalysis.mockImplementation(async () =>
      analysisDetail({
        analysisStatus: "complete",
        analyzedAt: "2026-07-16T09:00:00Z",
        counters: { total: 5, credible: 2, disputed: 2, unverifiable: 1 },
      }),
    );

    // The list poll observes the completion and the row resolves to the new
    // result: badge, date, and counters.
    expect(await screen.findByText(analysis.badge.complete)).toBeInTheDocument();
    expect(await screen.findByText(/5 affirmations/)).toBeInTheDocument();

    // With no run in flight the polling goes quiet.
    await new Promise((resolve) => setTimeout(resolve, 30));
    const settledPolls = pollVideos.mock.calls.length;
    const settledDetails = loadAnalysis.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(pollVideos.mock.calls.length).toBe(settledPolls);
    expect(loadAnalysis.mock.calls.length).toBe(settledDetails);
  });

  test("triggering an analysis flips the row to analysing and arms the polling", async () => {
    stubBackend([]);
    const idle = videoRecord({ durationMs: 100000 });
    const analysing = videoRecord({
      analysisStatus: "analysing",
      durationMs: 100000,
    });
    const loadVideos = vi.fn(async () => [idle]);
    const pollVideos = vi.fn(async () => [analysing]);
    const startAnalysis = vi.fn(async () => {});
    const loadAnalysis = vi.fn(async () =>
      analysisDetail({ analysisStatus: "analysing", analysisProgressMs: 50000 }),
    );

    render(
      <BackofficeVideosSection
        loadVideos={loadVideos}
        pollVideos={pollVideos}
        startAnalysis={startAnalysis}
        loadAnalysis={loadAnalysis}
        pollIntervalMs={10}
      />,
    );

    fireEvent.click(
      await screen.findByRole("button", { name: analysis.analyse }),
    );

    await waitFor(() => expect(startAnalysis).toHaveBeenCalledWith("vid-1"));
    // The row reads analysing at once (the 202 recorded it server-side) and
    // the armed polling brings the live progress in.
    expect(
      await screen.findByText(
        formatTemplate(analysis.badge.analysingPct, { pct: 50 }),
      ),
    ).toBeInTheDocument();
    await waitFor(() => expect(pollVideos).toHaveBeenCalled());
  });

  test("a 409 conflict keeps its explanation visible while the row flips to analysing", async () => {
    stubBackend([]);
    const idle = videoRecord({ durationMs: 100000 });
    const analysing = videoRecord({
      analysisStatus: "analysing",
      durationMs: 100000,
    });
    const loadVideos = vi.fn(async () => [idle]);
    const pollVideos = vi.fn(async () => [analysing]);
    const startAnalysis = vi
      .fn()
      .mockRejectedValue(new ApiError("analysis is already in progress", 409));
    const loadAnalysis = vi.fn(async () =>
      analysisDetail({ analysisStatus: "analysing", analysisProgressMs: 10000 }),
    );

    render(
      <BackofficeVideosSection
        loadVideos={loadVideos}
        pollVideos={pollVideos}
        startAnalysis={startAnalysis}
        loadAnalysis={loadAnalysis}
        pollIntervalMs={10}
      />,
    );

    fireEvent.click(
      await screen.findByRole("button", { name: analysis.analyse }),
    );

    // The row reflects the live run the 409 revealed, and the explanation is
    // not swallowed by that flip.
    expect(
      await screen.findByText(analysis.errors.conflict),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        formatTemplate(analysis.badge.analysingPct, { pct: 10 }),
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(analysis.errors.conflict)).toBeInTheDocument();
  });

  test("a stale poll racing the trigger cannot flip an analysing row back", async () => {
    stubBackend([]);
    const preTrigger = videoRecord({
      analysisStatus: "complete",
      analyzedAt: "2026-07-10T09:00:00Z",
      durationMs: 100000,
    });
    const loadVideos = vi.fn(async () => [preTrigger]);
    // Every poll keeps reporting the pre-trigger stored result, as a response
    // raced by the 202 would.
    const pollVideos = vi.fn(async () => [preTrigger]);
    const startAnalysis = vi.fn(async () => {});
    const loadAnalysis = vi.fn(async () =>
      analysisDetail({ analysisStatus: "analysing", analysisProgressMs: 1000 }),
    );

    render(
      <BackofficeVideosSection
        loadVideos={loadVideos}
        pollVideos={pollVideos}
        startAnalysis={startAnalysis}
        loadAnalysis={loadAnalysis}
        pollIntervalMs={10}
      />,
    );

    fireEvent.click(
      await screen.findByRole("button", { name: analysis.reanalyse }),
    );
    fireEvent.click(screen.getByRole("button", { name: analysis.confirmYes }));
    await waitFor(() => expect(startAnalysis).toHaveBeenCalledWith("vid-1"));

    // Polling runs, yet the stale "complete" (same analyzedAt) never displaces
    // the analysing row.
    await waitFor(() =>
      expect(pollVideos.mock.calls.length).toBeGreaterThan(2),
    );
    expect(
      screen.getByText(formatTemplate(analysis.badge.analysingPct, { pct: 1 })),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: analysis.reanalyse }),
    ).not.toBeInTheDocument();
  });
});

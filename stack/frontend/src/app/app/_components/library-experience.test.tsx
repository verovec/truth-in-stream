import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend, type BackendRoute } from "@/test/fact-check";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveSocketFactory } from "@/lib/live/ports";
import type { AnalysedLibraryVideo } from "@/lib/video/analysis";
import { LibraryExperience } from "./library-experience";

vi.mock("react-player", () => import("@/test/react-player-mock"));

function videoRecord(
  overrides: Partial<AnalysedLibraryVideo> = {},
): AnalysedLibraryVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    analysisStatus: "none",
    analyzedAt: null,
    ...overrides,
  };
}

// liveSeams returns spy socket/capture factories: the analysed-playback tests
// assert they are never touched, the live-regression test asserts they are.
function liveSeams() {
  const socketFactory = vi.fn<LiveSocketFactory>(() => ({
    send: vi.fn(),
    close: vi.fn(),
  }));
  const captureFactory = vi.fn(() => ({
    resume: vi.fn(),
    suspend: vi.fn(),
    stop: vi.fn(),
  }));
  return { socketFactory, captureFactory };
}

const analysisRoute = (id: string, ...bodies: unknown[]): BackendRoute => ({
  match: (url) => url.endsWith(`/api/videos/${id}/analysis`),
  responses: bodies.map((body) => json(200, body)),
});

// A one-statement stored session in wire shape, hydrated through the same
// parser the socket uses.
const STORED_FRAMES = [
  {
    type: "subtitle",
    id: "s1",
    start: 2,
    end: 4,
    text: "the stored transcript line",
    speaker: "A",
  },
  {
    type: "result",
    id: "s1",
    start: 2,
    end: 4,
    text: "the stored transcript line",
    matches: [],
  },
];

const completeAnalysisBody = {
  analysis_status: "complete",
  analyzed_at: "2026-07-17T09:00:00Z",
  analysis_runs: 1,
  analysis_progress_ms: 4000,
  counters: { total: 1, credible: 1, disputed: 0, unverifiable: 0 },
  frames: STORED_FRAMES,
};

function playableWire(id: string, title: string, kind: string, url: string) {
  return {
    id,
    title,
    status: "ready",
    kind,
    content_type: "video/mp4",
    size_bytes: 0,
    created_at: "2026-06-10T18:00:00Z",
    updated_at: "2026-06-10T18:00:00Z",
    playback: { url, method: "GET", headers: {} },
  };
}

const getVideoRoute = (id: string, body: unknown): BackendRoute => ({
  match: (url) => url.endsWith(`/api/videos/${id}`),
  responses: [json(200, body)],
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("LibraryExperience", () => {
  test("default-selects the first ready sample and loads it into the player", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
    ]);

    render(<LibraryExperience loadVideos={async () => [videoRecord()]} />);

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });
    // The live analysis panel mounts for the ready video, waiting to stream.
    expect(
      screen.getByRole("complementary", { name: fr.app.panel.heading }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(fr.app.panel.hints.idle),
    ).toBeInTheDocument();
    // The running-findings strip sits above the grid, idle until playback starts.
    const summary = screen.getByRole("region", {
      name: fr.app.summary.ariaLabel,
    });
    expect(summary).toBeInTheDocument();
    expect(
      within(summary).getByText(fr.app.summary.idleHint),
    ).toBeInTheDocument();
    // The playing video's title is surfaced as a heading above the library.
    expect(
      screen.getByRole("heading", { name: "Common Myths" }),
    ).toBeInTheDocument();
  });

  test("is a pure consumption surface: no upload or YouTube ingestion controls", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
    ]);

    render(<LibraryExperience loadVideos={async () => [videoRecord()]} />);

    // The library still lists and plays.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: /common myths/i }),
      ).toBeInTheDocument();
    });
    // Ingestion moved to the backoffice: the uploader and the YouTube form are
    // gone from /app entirely.
    expect(
      screen.queryByLabelText(fr.app.uploader.inputAria),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText(fr.app.youtube.label),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("upload-zone")).not.toBeInTheDocument();
  });

  test("shows a loading skeleton while the library is being fetched", () => {
    // A promise that never resolves holds the list in its loading state so the
    // skeleton is what the operator sees first.
    render(
      <LibraryExperience
        loadVideos={() => new Promise<AnalysedLibraryVideo[]>(() => {})}
      />,
    );

    expect(
      screen.getByRole("status", { name: fr.app.library.loadingAria }),
    ).toBeInTheDocument();
  });

  test("selecting another video loads it into the player", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
      getVideoRoute(
        "vid-2",
        playableWire("vid-2", "My Upload", "upload", "https://storage/play/vid-2"),
      ),
    ]);

    render(
      <LibraryExperience
        loadVideos={async () => [
          videoRecord(),
          videoRecord({ id: "vid-2", title: "My Upload", kind: "upload" }),
        ]}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });

    await userEvent.click(screen.getByRole("button", { name: /my upload/i }));

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-2",
      );
    });
    expect(screen.getByText(fr.app.panel.hints.idle)).toBeInTheDocument();
  });

  test("shows an error with retry when the library cannot load", async () => {
    const loadVideos = vi
      .fn<() => Promise<AnalysedLibraryVideo[]>>()
      .mockRejectedValueOnce(new Error("backend unavailable"))
      .mockResolvedValueOnce([videoRecord()]);
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
    ]);

    render(<LibraryExperience loadVideos={loadVideos} />);

    await waitFor(() => {
      // The raw API message is interpolated into the localized template.
      expect(screen.getByRole("alert")).toHaveTextContent(
        formatTemplate(fr.app.library.loadError, {
          message: "backend unavailable",
        }),
      );
    });

    await userEvent.click(screen.getByRole("button", { name: fr.app.library.retry }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /common myths/i })).toBeInTheDocument();
    });
  });

  test("a pre-analysed video hydrates its full transcript over REST before playback, opening no socket and no capture", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
      analysisRoute("vid-1", completeAnalysisBody),
    ]);
    const { socketFactory, captureFactory } = liveSeams();

    render(
      <LibraryExperience
        loadVideos={async () => [
          videoRecord({
            analysisStatus: "complete",
            analyzedAt: "2026-07-17T09:00:00Z",
          }),
        ]}
        socketFactory={socketFactory}
        captureFactory={captureFactory}
      />,
    );

    // The stored transcript is on screen before playback ever starts.
    expect(
      await screen.findByText("the stored transcript line"),
    ).toBeInTheDocument();
    // The tile carries the analysed badge from the list payload.
    expect(
      screen.getByRole("button", { name: /common myths/i }),
    ).toHaveTextContent(fr.app.library.analysedBadge);
    // The analysed chip replaces the pre-analyse control (chip + tile badge
    // both read "Analysée"); no trigger is offered on an analysed video.
    expect(screen.getAllByText(fr.app.analysis.complete)).toHaveLength(2);
    expect(
      screen.queryByRole("button", { name: fr.app.analysis.analyse }),
    ).not.toBeInTheDocument();

    // Playing the video must not open the live session: no WebSocket, no
    // audio capture, ever, for an analysed video.
    fireEvent.play(screen.getByTestId("media"));
    await waitFor(() => {
      expect(screen.getByText("the stored transcript line")).toBeInTheDocument();
    });
    expect(socketFactory).not.toHaveBeenCalled();
    expect(captureFactory).not.toHaveBeenCalled();
  });

  test("a video without a stored analysis keeps the live flow: play opens the WebSocket (regression)", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
    ]);
    const { socketFactory, captureFactory } = liveSeams();

    render(
      <LibraryExperience
        loadVideos={async () => [videoRecord()]}
        socketFactory={socketFactory}
        captureFactory={captureFactory}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });
    // No analysis fetch happened for a never-analysed video: the stub would
    // throw on an unexpected /analysis call.
    expect(socketFactory).not.toHaveBeenCalled();

    fireEvent.play(screen.getByTestId("media"));

    await waitFor(() => expect(socketFactory).toHaveBeenCalledTimes(1));
    expect(String(socketFactory.mock.calls[0][0])).toContain(
      "/api/videos/vid-1/live",
    );
  });

  test("an admin pre-analyses a ready video from the player and watches it complete", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
      {
        match: (url, init) =>
          url.endsWith("/api/videos/vid-1/analyse") && init?.method === "POST",
        responses: [() => new Response(null, { status: 202 })],
      },
      analysisRoute(
        "vid-1",
        {
          analysis_status: "analysing",
          analysis_runs: 1,
          analysis_progress_ms: 65_000,
        },
        completeAnalysisBody,
      ),
    ]);

    render(
      <LibraryExperience
        role="admin"
        loadVideos={async () => [videoRecord()]}
        pollIntervalMs={10}
      />,
    );

    // The trigger shows for the admin on a ready, never-analysed video.
    const button = await screen.findByRole("button", {
      name: fr.app.analysis.analyse,
    });
    await userEvent.click(button);

    // The accepted run shows the polled progress chip.
    expect(await screen.findByRole("status", { name: fr.app.analysis.progressAria })).toHaveTextContent(
      formatTemplate(fr.app.analysis.progress, { position: "1:05" }),
    );

    // The completing poll delivers the stored frames: the transcript hydrates
    // and the tile gains its analysed badge without a reload.
    expect(
      await screen.findByText("the stored transcript line"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /common myths/i }),
    ).toHaveTextContent(fr.app.library.analysedBadge);
  });

  test("a non-admin sees no pre-analyse control", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
    ]);

    render(<LibraryExperience role="guest" loadVideos={async () => [videoRecord()]} />);

    await waitFor(() => {
      expect(screen.getByTestId("media")).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: fr.app.analysis.analyse }),
    ).not.toBeInTheDocument();
  });
});

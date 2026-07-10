import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend, type BackendRoute } from "@/test/fact-check";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type { LibraryVideo } from "@/lib/video/api";
import { LibraryExperience } from "./library-experience";

vi.mock("react-player", () => import("@/test/react-player-mock"));

function videoRecord(overrides: Partial<LibraryVideo> = {}): LibraryVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    ...overrides,
  };
}

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
        loadVideos={() => new Promise<LibraryVideo[]>(() => {})}
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
      .fn<() => Promise<LibraryVideo[]>>()
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
});

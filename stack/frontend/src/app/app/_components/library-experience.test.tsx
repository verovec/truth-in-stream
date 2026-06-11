import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { json, stubBackend, type BackendRoute } from "@/test/fact-check";
import type { LibraryVideo } from "@/lib/video/api";
import type { PutUploader } from "@/lib/video/upload";
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
    // The live fact-check panel mounts for the ready video, waiting to stream.
    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(/fact checks stream here while the video plays/i),
    ).toBeInTheDocument();
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
      screen.getByRole("status", { name: "Loading library" }),
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
    expect(
      screen.getByText(/fact checks stream here while the video plays/i),
    ).toBeInTheDocument();
  });

  test("an uploaded file appears in the library as a ready video", async () => {
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
      getVideoRoute(
        "vid-9",
        playableWire("vid-9", "Holiday", "upload", "https://storage/play/vid-9"),
      ),
    ]);
    const uploader: PutUploader = async (_p, _f, onProgress) => {
      onProgress(10, 10);
    };

    render(<LibraryExperience loadVideos={async () => []} uploader={uploader} />);

    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });

    await userEvent.upload(
      screen.getByLabelText(/upload a video/i),
      new File(["x".repeat(20)], "Holiday.mp4", { type: "video/mp4" }),
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /holiday/i })).toBeEnabled();
    });
  });

  test("keeps the selected video playing when an upload completes", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
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
      <LibraryExperience
        loadVideos={async () => [videoRecord()]}
        uploader={uploader}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });

    await userEvent.upload(
      screen.getByLabelText(/upload a video/i),
      new File(["x".repeat(20)], "Holiday.mp4", { type: "video/mp4" }),
    );

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /holiday/i })).toBeInTheDocument();
    });
    // The originally selected sample is still the active video; the new upload
    // did not steal selection or re-load the player.
    expect(screen.getByTestId("media")).toHaveAttribute(
      "src",
      "https://storage/play/vid-1",
    );
  });

  test("pastes a YouTube link, shows it pending, then flips it to ready by polling", async () => {
    stubBackend([
      getVideoRoute(
        "vid-yt",
        playableWire("vid-yt", "Town Hall", "youtube", "https://storage/play/vid-yt"),
      ),
    ]);
    const submitYoutube = vi.fn(
      async (): Promise<LibraryVideo> =>
        videoRecord({
          id: "vid-yt",
          title: "Town Hall",
          status: "pending",
          kind: "youtube",
        }),
    );
    // Polling observes the background download finishing: the list now reports
    // the row as ready.
    const pollVideos = vi.fn(async () => [
      videoRecord({
        id: "vid-yt",
        title: "Town Hall",
        status: "ready",
        kind: "youtube",
      }),
    ]);

    render(
      <LibraryExperience
        loadVideos={async () => []}
        submitYoutube={submitYoutube}
        pollVideos={pollVideos}
        pollIntervalMs={10}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });

    await userEvent.type(
      screen.getByLabelText(/youtube url/i),
      "https://youtu.be/townhall",
    );
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    // The pending entry appears immediately, disabled until it is ready.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /town hall/i })).toBeDisabled();
    });

    // Polling flips it to ready on its own; the tile becomes selectable.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /town hall/i })).toBeEnabled();
    });
  });

  test("resubmitting an already-ingested link selects it without adding a duplicate tile", async () => {
    stubBackend([
      getVideoRoute(
        "vid-1",
        playableWire("vid-1", "Common Myths", "sample", "https://storage/play/vid-1"),
      ),
      getVideoRoute(
        "vid-yt",
        playableWire("vid-yt", "Town Hall", "youtube", "https://storage/play/vid-yt"),
      ),
    ]);
    const existing = videoRecord({
      id: "vid-yt",
      title: "Town Hall",
      status: "ready",
      kind: "youtube",
    });
    // The backend deduplicates: resubmitting the same link returns the existing
    // record rather than a new one.
    const submitYoutube = vi.fn(async (): Promise<LibraryVideo> => existing);

    render(
      <LibraryExperience
        loadVideos={async () => [videoRecord(), existing]}
        submitYoutube={submitYoutube}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });

    await userEvent.type(
      screen.getByLabelText(/youtube url/i),
      "https://youtu.be/townhall",
    );
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    // Selection moves to the existing video; no second tile is created.
    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-yt",
      );
    });
    expect(
      screen.getAllByRole("button", { name: /town hall/i }),
    ).toHaveLength(1);
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
      expect(screen.getByRole("alert")).toHaveTextContent(/backend unavailable/i);
    });

    await userEvent.click(screen.getByRole("button", { name: /try again/i }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /common myths/i })).toBeInTheDocument();
    });
  });
});

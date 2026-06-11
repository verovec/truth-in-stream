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

const factCheckRoutes: BackendRoute[] = [
  {
    match: (url, init) =>
      url.endsWith("/api/videos") && init?.method === "POST",
    responses: [json(200, { video_id: "fc-1", status: "complete" })],
  },
  {
    match: (url) => url.endsWith("/results"),
    responses: [json(200, { video_id: "fc-1", segments: [] })],
  },
];

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
      ...factCheckRoutes,
    ]);

    render(<LibraryExperience loadVideos={async () => [videoRecord()]} />);

    await waitFor(() => {
      expect(screen.getByTestId("media")).toHaveAttribute(
        "src",
        "https://storage/play/vid-1",
      );
    });
    expect(
      screen.getByRole("complementary", { name: /fact checks/i }),
    ).toBeInTheDocument();
    // The batch fact-check cycle runs for the curated sample end to end.
    expect(
      await screen.findByText(/no speech segments were found/i),
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
      ...factCheckRoutes,
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
      ...factCheckRoutes,
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
      ...factCheckRoutes,
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

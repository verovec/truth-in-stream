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
import type { LibraryVideo } from "@/lib/video/api";
import type { PutUploader } from "@/lib/video/upload";
import { BackofficeVideosSection } from "./backoffice-videos-section";

const list = fr.app.backoffice.videos.list;

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

afterEach(() => vi.restoreAllMocks());

describe("BackofficeVideosSection", () => {
  test("uploading a file confirms it and refreshes the management list", async () => {
    const confirmed = videoRecord({
      id: "vid-9",
      title: "Holiday",
      kind: "upload",
    });
    const loadVideos = vi
      .fn<() => Promise<LibraryVideo[]>>()
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
      .fn<() => Promise<LibraryVideo[]>>()
      .mockResolvedValueOnce([])
      .mockResolvedValue([pending]);
    const submitYoutube = vi.fn(async (): Promise<LibraryVideo> => pending);
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
      .fn<() => Promise<LibraryVideo[]>>()
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
});

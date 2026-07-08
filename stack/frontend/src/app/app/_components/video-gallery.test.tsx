import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { LibraryVideo } from "@/lib/video/api";
import type { UploadJob } from "@/hooks/use-video-uploads";
import { VideoGallery } from "./video-gallery";

function video(overrides: Partial<LibraryVideo> = {}): LibraryVideo {
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

describe("VideoGallery", () => {
  test("renders samples and uploads together with their status", () => {
    render(
      <VideoGallery
        videos={[
          video(),
          video({ id: "vid-2", title: "My Upload", kind: "upload", status: "pending" }),
        ]}
        jobs={[]}
        selectedId="vid-1"
        onSelect={() => {}}
        onDismiss={() => {}}
      />,
    );

    expect(screen.getByText("Common Myths")).toBeInTheDocument();
    expect(screen.getByText("My Upload")).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.sample)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.upload)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.status.pending)).toBeInTheDocument();
  });

  test("selecting a ready video reports it; a pending one is not selectable", async () => {
    const onSelect = vi.fn();
    render(
      <VideoGallery
        videos={[
          video(),
          video({ id: "vid-2", title: "My Upload", kind: "upload", status: "pending" }),
        ]}
        jobs={[]}
        selectedId={null}
        onSelect={onSelect}
        onDismiss={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /common myths/i }));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "vid-1" }));

    expect(screen.getByRole("button", { name: /my upload/i })).toBeDisabled();
  });

  test("marks the selected video as pressed", () => {
    render(
      <VideoGallery
        videos={[video()]}
        jobs={[]}
        selectedId="vid-1"
        onSelect={() => {}}
        onDismiss={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /common myths/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  test("renders an in-flight upload job with progress", () => {
    const job: UploadJob = {
      id: "job-1",
      title: "New Clip",
      fileName: "new-clip.mp4",
      state: { status: "uploading", progress: 0.4 },
    };
    render(
      <VideoGallery
        videos={[]}
        jobs={[job]}
        selectedId={null}
        onSelect={() => {}}
        onDismiss={() => {}}
      />,
    );

    expect(screen.getByText("New Clip")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");
  });

  test("hides ready jobs, which are shown as library rows instead", () => {
    const job: UploadJob = {
      id: "job-1",
      title: "Done Clip",
      fileName: "done.mp4",
      state: {
        status: "ready",
        video: video({ id: "vid-9", title: "Done Clip", kind: "upload" }),
      },
    };
    render(
      <VideoGallery
        videos={[video({ id: "vid-9", title: "Done Clip", kind: "upload" })]}
        jobs={[job]}
        selectedId={null}
        onSelect={() => {}}
        onDismiss={() => {}}
      />,
    );
    // Exactly one tile labelled "Done Clip" (the library row), not the job too.
    expect(screen.getAllByText("Done Clip")).toHaveLength(1);
  });

  test("shows an empty state when there is nothing to show", () => {
    render(
      <VideoGallery
        videos={[]}
        jobs={[]}
        selectedId={null}
        onSelect={() => {}}
        onDismiss={() => {}}
      />,
    );
    expect(screen.getByText(fr.app.library.empty)).toBeInTheDocument();
  });
});

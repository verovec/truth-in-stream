import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { AnalysedLibraryVideo } from "@/lib/video/analysis";
import { VideoGallery } from "./video-gallery";

function video(
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

describe("VideoGallery", () => {
  test("renders samples and uploads together with their status", () => {
    render(
      <VideoGallery
        videos={[
          video(),
          video({ id: "vid-2", title: "My Upload", kind: "upload", status: "pending" }),
        ]}
        selectedId="vid-1"
        onSelect={() => {}}
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
        selectedId={null}
        onSelect={onSelect}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /common myths/i }));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "vid-1" }));

    expect(screen.getByRole("button", { name: /my upload/i })).toBeDisabled();
  });

  test("marks the selected video as pressed", () => {
    render(
      <VideoGallery videos={[video()]} selectedId="vid-1" onSelect={() => {}} />,
    );
    expect(screen.getByRole("button", { name: /common myths/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  test("shows an empty state when there is nothing to show", () => {
    render(<VideoGallery videos={[]} selectedId={null} onSelect={() => {}} />);
    expect(screen.getByText(fr.app.library.empty)).toBeInTheDocument();
  });

  test("badges a pre-analysed video from the list payload; others carry no badge", () => {
    render(
      <VideoGallery
        videos={[
          video({
            analysisStatus: "complete",
            analyzedAt: "2026-07-17T09:00:00Z",
          }),
          video({ id: "vid-2", title: "My Upload", kind: "upload" }),
          video({ id: "vid-3", title: "Mid Run", analysisStatus: "analysing" }),
        ]}
        selectedId={null}
        onSelect={() => {}}
      />,
    );

    // Exactly one "Analysée" badge: complete carries it, none/analysing do not.
    expect(screen.getAllByText(fr.app.library.analysedBadge)).toHaveLength(1);
    expect(
      screen.getByRole("button", { name: /common myths/i }),
    ).toHaveTextContent(fr.app.library.analysedBadge);
  });
});

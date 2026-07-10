import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import type { LibraryVideo } from "@/lib/video/api";
import { BackofficeVideoList } from "./backoffice-video-list";

const list = fr.app.backoffice.videos.list;

function video(over: Partial<LibraryVideo> = {}): LibraryVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    ...over,
  };
}

afterEach(() => vi.restoreAllMocks());

describe("BackofficeVideoList", () => {
  test("shows the empty state when there are no videos", () => {
    render(
      <BackofficeVideoList videos={[]} remove={vi.fn()} onDeleted={vi.fn()} />,
    );
    expect(screen.getByText(list.empty)).toBeInTheDocument();
  });

  test("renders each video's title with kind and status badges", () => {
    render(
      <BackofficeVideoList
        videos={[
          video(),
          video({
            id: "vid-2",
            title: "Town Hall",
            kind: "youtube",
            status: "pending",
          }),
        ]}
        remove={vi.fn()}
        onDeleted={vi.fn()}
      />,
    );
    expect(screen.getByText("Common Myths")).toBeInTheDocument();
    expect(screen.getByText("Town Hall")).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.sample)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.kind.youtube)).toBeInTheDocument();
    expect(screen.getByText(fr.app.library.status.pending)).toBeInTheDocument();
  });

  test("confirms before deleting, then calls remove and onDeleted", async () => {
    const remove = vi.fn().mockResolvedValue(undefined);
    const onDeleted = vi.fn();
    render(
      <BackofficeVideoList
        videos={[video({ id: "vid-2", title: "Town Hall", kind: "youtube" })]}
        remove={remove}
        onDeleted={onDeleted}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    // The two-step confirm does not fire until confirmed.
    expect(remove).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: list.confirmYes }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("vid-2"));
    expect(onDeleted).toHaveBeenCalledTimes(1);
  });

  test("cancelling the confirm deletes nothing and restores the delete control", () => {
    const remove = vi.fn();
    render(
      <BackofficeVideoList
        videos={[video()]}
        remove={remove}
        onDeleted={vi.fn()}
      />,
    );
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
    render(
      <BackofficeVideoList
        videos={[video()]}
        remove={remove}
        onDeleted={onDeleted}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: list.delete }));
    fireEvent.click(screen.getByRole("button", { name: list.confirmYes }));

    expect(
      await screen.findByText(
        formatTemplate(list.deleteError, { message: "boom" }),
      ),
    ).toBeInTheDocument();
    expect(onDeleted).not.toHaveBeenCalled();
  });
});

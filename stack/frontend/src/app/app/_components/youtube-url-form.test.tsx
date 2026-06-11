import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import type { LibraryVideo } from "@/lib/video/api";
import { YoutubeUrlForm } from "./youtube-url-form";

function pendingYoutube(overrides: Partial<LibraryVideo> = {}): LibraryVideo {
  return {
    id: "vid-yt",
    title: "https://www.youtube.com/watch?v=abc",
    status: "pending",
    kind: "youtube",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-11T12:00:00Z",
    updatedAt: "2026-06-11T12:00:00Z",
    ...overrides,
  };
}

describe("YoutubeUrlForm", () => {
  test("submits the url, reports the added video, and clears the input", async () => {
    const video = pendingYoutube();
    const submit = vi.fn(async () => video);
    const onAdded = vi.fn();

    render(<YoutubeUrlForm submit={submit} onAdded={onAdded} />);

    const input = screen.getByLabelText(/youtube url/i);
    await userEvent.type(input, "https://www.youtube.com/watch?v=abc");
    await userEvent.click(screen.getByRole("button", { name: /add/i }));

    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(video));
    expect(submit).toHaveBeenCalledWith(
      "https://www.youtube.com/watch?v=abc",
      expect.any(AbortSignal),
    );
    expect(input).toHaveValue("");
  });

  test("disables the input and button while a submission is in flight", async () => {
    let resolve!: (video: LibraryVideo) => void;
    const submit = vi.fn(
      () =>
        new Promise<LibraryVideo>((r) => {
          resolve = r;
        }),
    );

    render(<YoutubeUrlForm submit={submit} onAdded={vi.fn()} />);

    const input = screen.getByLabelText(/youtube url/i);
    await userEvent.type(input, "https://youtu.be/abc");
    await userEvent.click(screen.getByRole("button", { name: /add/i }));

    await waitFor(() => expect(input).toBeDisabled());
    expect(screen.getByRole("button", { name: /add/i })).toBeDisabled();

    resolve(pendingYoutube());
    await waitFor(() => expect(input).toBeEnabled());
  });

  test("surfaces the backend rejection message inline and adds nothing", async () => {
    const submit = vi.fn(async () => {
      throw new ApiError("not a valid youtube video url", 400);
    });
    const onAdded = vi.fn();

    render(<YoutubeUrlForm submit={submit} onAdded={onAdded} />);

    const input = screen.getByLabelText(/youtube url/i);
    await userEvent.type(input, "https://example.com/not-youtube");
    await userEvent.click(screen.getByRole("button", { name: /add/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /not a valid youtube video url/i,
    );
    expect(onAdded).not.toHaveBeenCalled();
    // The rejected link stays in the field so the operator can correct it.
    expect(input).toHaveValue("https://example.com/not-youtube");
  });

  test("rejects an empty or malformed link locally without calling the backend", async () => {
    const submit = vi.fn();

    render(<YoutubeUrlForm submit={submit} onAdded={vi.fn()} />);

    await userEvent.type(screen.getByLabelText(/youtube url/i), "not a url");
    await userEvent.click(screen.getByRole("button", { name: /add/i }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
  });
});

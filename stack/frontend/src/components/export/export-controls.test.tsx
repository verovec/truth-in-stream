import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import { ExportControls } from "./export-controls";
import { ApiError } from "@/lib/http";
import { fr } from "@/lib/i18n/dictionaries/fr";

const copy = fr.app.exports;

afterEach(() => {
  vi.restoreAllMocks();
});

describe("ExportControls", () => {
  test("renders nothing for a guest", () => {
    const { container } = render(
      <ExportControls role="guest" videoId="vid-1" />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  test("renders nothing when no video is active", () => {
    const { container } = render(
      <ExportControls role="admin" videoId={null} />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  test("shows the SRT and CSV download controls for an admin", () => {
    render(<ExportControls role="admin" videoId="vid-1" />);

    expect(
      screen.getByRole("button", { name: copy.transcript }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: copy.claims }),
    ).toBeInTheDocument();
  });

  test("downloads the SRT when the admin clicks it", async () => {
    const download = vi.fn().mockResolvedValue("vid-1.srt");
    const user = userEvent.setup();
    render(
      <ExportControls role="admin" videoId="vid-1" download={download} />,
    );

    await user.click(screen.getByRole("button", { name: copy.transcript }));

    expect(download).toHaveBeenCalledWith("vid-1", "srt");
  });

  test("shows an inline message when the snapshot is missing (404)", async () => {
    const download = vi
      .fn()
      .mockRejectedValue(new ApiError("no cached analysis", 404));
    const user = userEvent.setup();
    render(
      <ExportControls role="admin" videoId="vid-1" download={download} />,
    );

    await user.click(screen.getByRole("button", { name: copy.claims }));

    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent(
        copy.missingSnapshot,
      );
    });
  });
});

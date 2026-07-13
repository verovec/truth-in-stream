import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import { ApiError } from "@/lib/http";
import type { Channel } from "@/lib/tv/api";
import { BackofficeTvSection } from "./backoffice-tv-section";

const list = fr.app.backoffice.tv.list;

function channel(over: Partial<Channel> = {}): Channel {
  return {
    id: "chan-1",
    slug: "france-24",
    name: "France 24",
    sourceKind: "youtube",
    sourceRef: "https://www.youtube.com/@FRANCE24/live",
    enabled: true,
    archiveEnabled: false,
    live: true,
    ...over,
  };
}

// captureSwitch returns the first switch in the single channel row (capture);
// the second is the archive switch.
function captureSwitch() {
  return screen.getAllByRole("switch")[0];
}

afterEach(() => vi.restoreAllMocks());

describe("BackofficeTvSection", () => {
  test("renders a row per channel with its source reference and live status", async () => {
    const loadChannels = vi.fn().mockResolvedValue([
      channel(),
      channel({
        id: "chan-2",
        slug: "public-senat",
        name: "Public Sénat",
        sourceKind: "hls",
        sourceRef: "https://stream.example/senat.m3u8",
        enabled: false,
        live: false,
      }),
    ]);
    render(<BackofficeTvSection loadChannels={loadChannels} />);

    expect(await screen.findByText("France 24")).toBeInTheDocument();
    expect(screen.getByText("Public Sénat")).toBeInTheDocument();
    expect(screen.getByText("https://stream.example/senat.m3u8")).toBeInTheDocument();
    expect(screen.getByText(list.live)).toBeInTheDocument();
    expect(screen.getByText(list.offline)).toBeInTheDocument();
  });

  test("shows the empty state when there are no channels", async () => {
    const loadChannels = vi.fn().mockResolvedValue([]);
    render(<BackofficeTvSection loadChannels={loadChannels} />);
    expect(await screen.findByText(list.empty)).toBeInTheDocument();
  });

  test("surfaces a load failure with a retry control", async () => {
    const loadChannels = vi.fn().mockRejectedValue(new ApiError("boom", 500));
    render(<BackofficeTvSection loadChannels={loadChannels} />);
    expect(
      await screen.findByText(formatTemplate(list.loadError, { message: "boom" })),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: list.retry })).toBeInTheDocument();
  });

  test("optimistically flips capture and persists on success", async () => {
    const loadChannels = vi.fn().mockResolvedValue([channel()]);
    const update = vi.fn().mockResolvedValue(channel({ enabled: false }));
    render(<BackofficeTvSection loadChannels={loadChannels} update={update} />);

    await screen.findByText("France 24");
    expect(captureSwitch()).toHaveAttribute("aria-checked", "true");

    fireEvent.click(captureSwitch());

    // Optimistic: the switch flips before the PATCH resolves.
    expect(captureSwitch()).toHaveAttribute("aria-checked", "false");
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith("chan-1", { enabled: false }),
    );
    expect(captureSwitch()).toHaveAttribute("aria-checked", "false");
  });

  test("rolls back the toggle and shows a reason when the PATCH fails", async () => {
    const loadChannels = vi.fn().mockResolvedValue([channel()]);
    const update = vi.fn().mockRejectedValue(new ApiError("nope", 500));
    render(<BackofficeTvSection loadChannels={loadChannels} update={update} />);

    await screen.findByText("France 24");
    fireEvent.click(captureSwitch());

    expect(
      await screen.findByText(
        formatTemplate(list.toggleError, { message: "nope" }),
      ),
    ).toBeInTheDocument();
    // Rolled back to the original enabled=true.
    expect(captureSwitch()).toHaveAttribute("aria-checked", "true");
  });

  test("deletes a channel after a two-step confirm and re-lists", async () => {
    const loadChannels = vi.fn().mockResolvedValue([channel()]);
    const remove = vi.fn().mockResolvedValue(undefined);
    render(<BackofficeTvSection loadChannels={loadChannels} remove={remove} />);

    await screen.findByText("France 24");
    const row = screen.getByText("France 24").closest("tr") as HTMLElement;

    fireEvent.click(within(row).getByRole("button", { name: list.delete }));
    expect(remove).not.toHaveBeenCalled();

    fireEvent.click(within(row).getByRole("button", { name: list.confirmYes }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("chan-1"));
    // A successful delete re-lists (loadChannels called again beyond the mount).
    await waitFor(() => expect(loadChannels).toHaveBeenCalledTimes(2));
  });
});

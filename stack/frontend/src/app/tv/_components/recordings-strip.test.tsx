import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { Recording } from "@/lib/tv/api";
import type { PlayableVideo } from "@/lib/video/api";
import { RecordingsStrip } from "./recordings-strip";

// VideoPlayer wraps react-player; stub it so the strip test asserts wiring (the
// resolved playback URL) without pulling in the media element.
vi.mock("@/components/playback/video-player", () => ({
  VideoPlayer: ({ src, title }: { src: string; title: string }) => (
    <div data-testid="player" data-src={src}>
      {title}
    </div>
  ),
}));

const recordings: Recording[] = [
  {
    id: "v2",
    title: "franceinfo - 2026-07-10 21:00",
    recordedAt: "2026-07-10T21:00:00Z",
    status: "ready",
  },
  {
    id: "v1",
    title: "franceinfo - 2026-07-10 20:00",
    recordedAt: "2026-07-10T20:00:00Z",
    status: "ready",
  },
];

const playable = (id: string): PlayableVideo => ({
  id,
  title: `recording ${id}`,
  status: "ready",
  kind: "tv" as PlayableVideo["kind"],
  contentType: "video/mp4",
  sizeBytes: 1,
  createdAt: "2026-07-10T21:00:00Z",
  updatedAt: "2026-07-10T21:00:00Z",
  playback: { url: `https://storage/${id}.mp4`, method: "GET", headers: {} },
});

describe("RecordingsStrip", () => {
  test("lists the channel's recordings in served (newest-first) order", async () => {
    const load = vi.fn().mockResolvedValue(recordings);
    render(<RecordingsStrip channelId="chan-1" loadRecordings={load} />);

    await waitFor(() => expect(load).toHaveBeenCalledWith("chan-1", expect.anything()));
    const titles = (await screen.findAllByRole("button")).map((b) => b.textContent);
    expect(titles[0]).toContain("21:00");
    expect(titles[1]).toContain("20:00");
  });

  test("opening a recording resolves it by id and plays the presigned URL", async () => {
    const load = vi.fn().mockResolvedValue(recordings);
    const resolve = vi.fn((id: string) => Promise.resolve(playable(id)));
    render(
      <RecordingsStrip
        channelId="chan-1"
        loadRecordings={load}
        resolveRecording={resolve}
      />,
    );

    const row = await screen.findByRole("button", { name: /21:00/ });
    fireEvent.click(row);

    await waitFor(() => expect(resolve).toHaveBeenCalledWith("v2", expect.anything()));
    const player = await screen.findByTestId("player");
    expect(player).toHaveAttribute("data-src", "https://storage/v2.mp4");
  });

  test("shows an empty message when the channel has no recordings", async () => {
    const load = vi.fn().mockResolvedValue([]);
    render(<RecordingsStrip channelId="chan-1" loadRecordings={load} />);
    expect(await screen.findByText(fr.app.tv.recordings.empty)).toBeInTheDocument();
  });
});

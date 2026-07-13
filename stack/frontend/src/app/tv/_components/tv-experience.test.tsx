import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { Channel } from "@/lib/tv/api";
import { TvExperience } from "./tv-experience";

// ChannelView owns the live/recordings subtree (tested separately); stub it so
// the experience test covers grid <-> channel navigation only.
vi.mock("./channel-view", () => ({
  ChannelView: ({ channel }: { channel: Channel }) => (
    <div data-testid="channel-view">{channel.name}</div>
  ),
}));

const channels: Channel[] = [
  {
    id: "chan-1",
    slug: "franceinfo",
    name: "franceinfo",
    sourceKind: "youtube",
    sourceRef: "https://www.youtube.com/franceinfo/live",
    enabled: true,
    archiveEnabled: true,
    live: true,
  },
  {
    id: "chan-2",
    slug: "senat",
    name: "Sénat",
    sourceKind: "hls",
    sourceRef: "https://videos.senat.fr/direct",
    enabled: false,
    archiveEnabled: true,
    live: false,
  },
];

describe("TvExperience", () => {
  test("loads the channels and shows the role-filtered grid", async () => {
    const load = vi.fn().mockResolvedValue(channels);
    render(<TvExperience role="guest" loadChannels={load} />);

    expect(await screen.findByText("franceinfo")).toBeInTheDocument();
    // Guest never sees the disabled channel.
    expect(screen.queryByText("Sénat")).not.toBeInTheDocument();
    expect(screen.getByText(fr.app.tv.heading)).toBeInTheDocument();
  });

  test("opens a channel and returns to the grid via back", async () => {
    const load = vi.fn().mockResolvedValue(channels);
    render(<TvExperience role="admin" loadChannels={load} />);

    fireEvent.click(await screen.findByRole("button", { name: /franceinfo/ }));
    expect(screen.getByTestId("channel-view")).toHaveTextContent("franceinfo");

    fireEvent.click(screen.getByRole("button", { name: fr.app.tv.back }));
    await waitFor(() =>
      expect(screen.queryByTestId("channel-view")).not.toBeInTheDocument(),
    );
    expect(screen.getByText(fr.app.tv.heading)).toBeInTheDocument();
  });

  test("surfaces a load error with a retry", async () => {
    const load = vi
      .fn()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(channels);
    render(<TvExperience role="guest" loadChannels={load} />);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    fireEvent.click(screen.getByRole("button", { name: fr.app.tv.retry }));
    expect(await screen.findByText("franceinfo")).toBeInTheDocument();
  });
});

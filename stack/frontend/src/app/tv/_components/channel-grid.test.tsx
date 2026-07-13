import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { Channel } from "@/lib/tv/api";
import { ChannelGrid, visibleChannels } from "./channel-grid";

const enabledLive: Channel = {
  id: "chan-1",
  slug: "franceinfo",
  name: "franceinfo",
  sourceKind: "youtube",
  sourceRef: "https://www.youtube.com/franceinfo/live",
  enabled: true,
  archiveEnabled: true,
  live: true,
};

const disabledOffline: Channel = {
  id: "chan-2",
  slug: "senat",
  name: "Sénat",
  sourceKind: "hls",
  sourceRef: "https://videos.senat.fr/direct",
  enabled: false,
  archiveEnabled: true,
  live: false,
};

describe("visibleChannels", () => {
  test("shows every channel to an admin", () => {
    expect(visibleChannels([enabledLive, disabledOffline], "admin")).toEqual([
      enabledLive,
      disabledOffline,
    ]);
  });

  test("hides disabled channels from a guest", () => {
    expect(visibleChannels([enabledLive, disabledOffline], "guest")).toEqual([
      enabledLive,
    ]);
  });
});

describe("ChannelGrid", () => {
  test("an admin sees the disabled channel greyed with a disabled badge", () => {
    render(
      <ChannelGrid
        channels={[enabledLive, disabledOffline]}
        role="admin"
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("franceinfo")).toBeInTheDocument();
    const disabledTile = screen.getByRole("button", { name: /Sénat/ });
    expect(disabledTile).toHaveAttribute("data-disabled", "true");
    expect(screen.getByText(fr.app.tv.grid.disabled)).toBeInTheDocument();
  });

  test("a guest never sees the disabled channel", () => {
    render(
      <ChannelGrid
        channels={[enabledLive, disabledOffline]}
        role="guest"
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText("franceinfo")).toBeInTheDocument();
    expect(screen.queryByText("Sénat")).not.toBeInTheDocument();
    expect(screen.queryByText(fr.app.tv.grid.disabled)).not.toBeInTheDocument();
  });

  test("marks a live channel with the ON AIR badge", () => {
    render(
      <ChannelGrid channels={[enabledLive]} role="guest" onSelect={() => {}} />,
    );
    expect(screen.getByText(fr.app.tv.grid.onAir)).toBeInTheDocument();
  });

  test("selecting a tile reports the channel", () => {
    const onSelect = vi.fn();
    render(
      <ChannelGrid channels={[enabledLive]} role="guest" onSelect={onSelect} />,
    );
    fireEvent.click(screen.getByRole("button", { name: /franceinfo/ }));
    expect(onSelect).toHaveBeenCalledWith(enabledLive);
  });

  test("shows an empty message when nothing is visible to the caller", () => {
    render(
      <ChannelGrid
        channels={[disabledOffline]}
        role="guest"
        onSelect={() => {}}
      />,
    );
    expect(screen.getByText(fr.app.tv.grid.empty)).toBeInTheDocument();
  });
});

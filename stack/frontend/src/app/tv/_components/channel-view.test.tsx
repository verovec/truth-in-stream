import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { Channel } from "@/lib/tv/api";
import { ChannelView } from "./channel-view";

// The live subtree is exercised in its own tests; here it is stubbed so the view
// test asserts the channel-kind branching (embed vs no-player) and the off-air
// path, not the panels' internals. Stubbing the provider also keeps the test off
// a real WebSocket.
vi.mock("@/components/live/channel-live-provider", () => ({
  ChannelLiveProvider: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="channel-live">{children}</div>
  ),
}));
vi.mock("@/app/app/_components/live-summary-strip", () => ({
  LiveSummaryStrip: () => <div data-testid="summary-strip" />,
}));
vi.mock("@/app/app/_components/live-speaker-credibility", () => ({
  LiveSpeakerCredibility: () => <div data-testid="speaker-credibility" />,
}));
vi.mock("@/app/app/_components/live-fact-check-panel", () => ({
  LiveFactCheckPanel: () => <div data-testid="fact-check-panel" />,
}));
vi.mock("./recordings-strip", () => ({
  RecordingsStrip: ({ channelId }: { channelId: string }) => (
    <div data-testid="recordings-strip">{channelId}</div>
  ),
}));

const youtubeLive: Channel = {
  id: "chan-1",
  slug: "franceinfo",
  name: "franceinfo",
  sourceKind: "youtube",
  sourceRef: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  enabled: true,
  archiveEnabled: true,
  live: true,
};

const hlsLive: Channel = {
  id: "chan-2",
  slug: "senat",
  name: "Sénat",
  sourceKind: "hls",
  sourceRef: "https://videos.senat.fr/direct",
  enabled: true,
  archiveEnabled: true,
  live: true,
};

describe("ChannelView", () => {
  test("a live youtube channel shows the embed, the live panels, and its recordings", () => {
    render(<ChannelView channel={youtubeLive} />);
    const iframe = screen.getByTitle("franceinfo");
    expect(iframe.tagName).toBe("IFRAME");
    expect(iframe).toHaveAttribute(
      "src",
      "https://www.youtube.com/embed/dQw4w9WgXcQ",
    );
    expect(screen.getByTestId("fact-check-panel")).toBeInTheDocument();
    expect(screen.getByTestId("summary-strip")).toBeInTheDocument();
    expect(screen.getByText(fr.app.tv.channel.analysisDelay)).toBeInTheDocument();
    expect(screen.getByTestId("recordings-strip")).toHaveTextContent("chan-1");
  });

  test("a live hls channel shows the live panels with no player and no iframe", () => {
    render(<ChannelView channel={hlsLive} />);
    expect(screen.queryByTitle("Sénat")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: fr.app.tv.embed.openOnYoutube })).not.toBeInTheDocument();
    expect(screen.getByText(fr.app.tv.channel.hlsNoPlayer)).toBeInTheDocument();
    expect(screen.getByTestId("fact-check-panel")).toBeInTheDocument();
  });

  test("an off-air channel states it plainly, drops the panels, and still lists recordings", () => {
    render(<ChannelView channel={{ ...youtubeLive, live: false }} />);
    expect(
      screen.getByText(
        fr.app.tv.channel.offAir.replace("{name}", "franceinfo"),
      ),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("fact-check-panel")).not.toBeInTheDocument();
    expect(screen.queryByTitle("franceinfo")).not.toBeInTheDocument();
    expect(screen.getByTestId("recordings-strip")).toHaveTextContent("chan-1");
  });
});

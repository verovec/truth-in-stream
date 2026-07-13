import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import type { LiveAnalysisSnapshot } from "@/lib/live/live-analysis-store";
import { emptySummary } from "@/lib/live/summary";
import { ChannelLiveProvider } from "./channel-live-provider";
import { useLiveAnalysisSelector } from "./live-analysis-provider";

const mockUseChannelLive = vi.hoisted(() =>
  vi.fn<(channelId: string) => LiveAnalysis>(),
);

vi.mock("@/hooks/use-channel-live", () => ({
  useChannelLive: mockUseChannelLive,
}));

afterEach(() => mockUseChannelLive.mockReset());

const analysis = (overrides: Partial<LiveAnalysis> = {}): LiveAnalysis => ({
  statements: [],
  caption: "",
  status: "live",
  summary: emptySummary(),
  claimsFor: () => [],
  speakers: [],
  ...overrides,
});

function Probe({
  select,
}: {
  select: (snapshot: LiveAnalysisSnapshot) => string;
}) {
  return <span data-testid="probe">{useLiveAnalysisSelector(select)}</span>;
}

describe("ChannelLiveProvider", () => {
  test("publishes the channel viewer's analysis to the shared selector context", () => {
    mockUseChannelLive.mockReturnValue(
      analysis({ status: "live", summary: { ...emptySummary(), checked: 4 } }),
    );

    render(
      <ChannelLiveProvider channelId="chan-1">
        <Probe select={(s) => `${s?.status ?? "idle"}:${s?.summary.checked ?? 0}`} />
      </ChannelLiveProvider>,
    );

    expect(mockUseChannelLive).toHaveBeenCalledWith("chan-1", {});
    expect(screen.getByTestId("probe")).toHaveTextContent("live:4");
  });

  test("drives an idle snapshot when there is no channel", () => {
    mockUseChannelLive.mockReturnValue(analysis());

    render(
      <ChannelLiveProvider channelId={null}>
        <Probe select={(s) => (s === null ? "idle" : "active")} />
      </ChannelLiveProvider>,
    );

    expect(mockUseChannelLive).not.toHaveBeenCalled();
    expect(screen.getByTestId("probe")).toHaveTextContent("idle");
  });
});

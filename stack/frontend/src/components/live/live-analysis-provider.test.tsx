import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import type { LiveAnalysisSnapshot } from "@/lib/live/live-analysis-store";
import { emptySummary } from "@/lib/live/summary";
import {
  LiveAnalysisProvider,
  useLiveAnalysisSelector,
} from "./live-analysis-provider";

const mockUseLiveAnalysis = vi.hoisted(() =>
  vi.fn<(videoId: string) => LiveAnalysis>(),
);

vi.mock("@/hooks/use-live-analysis", () => ({
  useLiveAnalysis: mockUseLiveAnalysis,
}));

afterEach(() => {
  mockUseLiveAnalysis.mockReset();
});

const analysis = (overrides: Partial<LiveAnalysis> = {}): LiveAnalysis => ({
  statements: [],
  caption: "",
  status: "live",
  summary: emptySummary(),
  ...overrides,
});

// Probe renders whatever the selector yields so the test can read the shared
// snapshot the driver published.
function Probe({
  select,
}: {
  select: (snapshot: LiveAnalysisSnapshot) => string;
}) {
  const value = useLiveAnalysisSelector(select);
  return <span data-testid="probe">{value}</span>;
}

describe("LiveAnalysisProvider", () => {
  test("publishes the driver's live analysis to selector consumers", () => {
    mockUseLiveAnalysis.mockReturnValue(
      analysis({ status: "live", summary: { ...emptySummary(), checked: 3 } }),
    );

    render(
      <LiveAnalysisProvider videoId="vid-1">
        <Probe select={(s) => `${s?.status ?? "idle"}:${s?.summary.checked ?? 0}`} />
      </LiveAnalysisProvider>,
    );

    expect(mockUseLiveAnalysis).toHaveBeenCalledWith("vid-1");
    expect(screen.getByTestId("probe")).toHaveTextContent("live:3");
  });

  test("drives an idle snapshot when there is no active video", () => {
    mockUseLiveAnalysis.mockReturnValue(analysis());

    render(
      <LiveAnalysisProvider videoId={null}>
        <Probe select={(s) => (s === null ? "idle" : "active")} />
      </LiveAnalysisProvider>,
    );

    // With no video, the driver never mounts, so the hook is never called and
    // the snapshot stays null.
    expect(mockUseLiveAnalysis).not.toHaveBeenCalled();
    expect(screen.getByTestId("probe")).toHaveTextContent("idle");
  });

  test("starts a fresh session when the active video changes, without blanking", () => {
    mockUseLiveAnalysis.mockImplementation((id) => {
      const checked = id === "vid-1" ? 2 : 5;
      return analysis({ summary: { ...emptySummary(), checked } });
    });

    const { rerender } = render(
      <LiveAnalysisProvider videoId="vid-1">
        <Probe select={(s) => `${s?.summary.checked ?? "idle"}`} />
      </LiveAnalysisProvider>,
    );
    expect(screen.getByTestId("probe")).toHaveTextContent("2");

    rerender(
      <LiveAnalysisProvider videoId="vid-2">
        <Probe select={(s) => `${s?.summary.checked ?? "idle"}`} />
      </LiveAnalysisProvider>,
    );
    // The new session's snapshot wins; the old driver's teardown must not blank
    // the freshly mounted one.
    expect(screen.getByTestId("probe")).toHaveTextContent("5");
  });

  test("clears the snapshot back to idle when the active video is removed", () => {
    mockUseLiveAnalysis.mockReturnValue(
      analysis({ summary: { ...emptySummary(), checked: 2 } }),
    );

    const { rerender } = render(
      <LiveAnalysisProvider videoId="vid-1">
        <Probe select={(s) => `${s?.summary.checked ?? "idle"}`} />
      </LiveAnalysisProvider>,
    );
    expect(screen.getByTestId("probe")).toHaveTextContent("2");

    rerender(
      <LiveAnalysisProvider videoId={null}>
        <Probe select={(s) => `${s?.summary.checked ?? "idle"}`} />
      </LiveAnalysisProvider>,
    );
    expect(screen.getByTestId("probe")).toHaveTextContent("idle");
  });
});

describe("useLiveAnalysisSelector", () => {
  test("throws without a provider ancestor", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => render(<Probe select={() => "x"} />)).toThrow(
      /LiveAnalysisProvider/,
    );
    spy.mockRestore();
  });
});

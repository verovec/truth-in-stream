import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import { LiveAnalysisProvider } from "@/components/live/live-analysis-provider";
import type { LiveSummary } from "@/lib/live/summary";
import { emptySummary } from "@/lib/live/summary";
import { LiveSummaryStrip, SummaryStripView } from "./live-summary-strip";

const mockUseLiveAnalysis = vi.hoisted(() => vi.fn<() => LiveAnalysis>());

vi.mock("@/hooks/use-live-analysis", () => ({
  useLiveAnalysis: mockUseLiveAnalysis,
}));

afterEach(() => {
  mockUseLiveAnalysis.mockReset();
});

const summary = (overrides: Partial<LiveSummary> = {}): LiveSummary => ({
  ...emptySummary(),
  ...overrides,
});

describe("SummaryStripView", () => {
  test("shows an idle hint when no video is being analysed", () => {
    render(<SummaryStripView summary={null} status="idle" />);

    expect(screen.getByText(/findings appear here/i)).toBeInTheDocument();
    // No counts are shown in the idle state.
    expect(screen.queryByLabelText(/checked:/i)).not.toBeInTheDocument();
  });

  test("stays quiet for a selected video before analysis has started", () => {
    // A ready-but-paused video has an empty summary and an idle status; nothing
    // is playing yet, so the strip shows the idle hint, not a row of zeros.
    render(<SummaryStripView summary={summary()} status="idle" />);

    expect(screen.getByText(/findings appear here/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/checked:/i)).not.toBeInTheDocument();
  });

  test("renders the running counts once analysis is live", () => {
    render(
      <SummaryStripView
        summary={summary({
          checked: 5,
          corroborates: 3,
          contradicts: 1,
          unclear: 1,
          evidence: 2,
          skipped: 4,
        })}
        status="live"
      />,
    );

    expect(screen.getByLabelText("Checked: 5")).toBeInTheDocument();
    expect(screen.getByLabelText("Corroborated: 3")).toBeInTheDocument();
    expect(screen.getByLabelText("Contradicted: 1")).toBeInTheDocument();
    expect(screen.getByLabelText("Unclear: 1")).toBeInTheDocument();
    // Supporting evidence is distinguishable from claim verdicts.
    expect(screen.getByLabelText("Evidence: 2")).toBeInTheDocument();
    expect(screen.getByLabelText("Not checked: 4")).toBeInTheDocument();
  });

  test("marks the counts as a polite live region for assistive tech", () => {
    render(<SummaryStripView summary={summary({ checked: 1 })} status="live" />);

    const region = screen.getByLabelText("Checked: 1").closest("[aria-live]");
    expect(region).toHaveAttribute("aria-live", "polite");
  });

  test("surfaces a reconnecting indicator while keeping the counts", () => {
    render(
      <SummaryStripView
        summary={summary({ checked: 7, corroborates: 4 })}
        status="reconnecting"
      />,
    );

    expect(screen.getByText(/reconnecting/i)).toBeInTheDocument();
    // Counts accumulated before the drop are preserved through a reconnect.
    expect(screen.getByLabelText("Checked: 7")).toBeInTheDocument();
  });

  test("flags an interrupted session instead of reading all-clear", () => {
    // The fact-check panel surfaces an error alert when analysis breaks; the
    // strip must not contradict it with the cheerful idle hint, so an errored
    // session shows the counts so far and an interrupted indicator.
    render(
      <SummaryStripView summary={summary({ checked: 2 })} status="error" />,
    );

    expect(screen.getByText(/interrupted/i)).toBeInTheDocument();
    expect(screen.getByLabelText("Checked: 2")).toBeInTheDocument();
    expect(screen.queryByText(/findings appear here/i)).not.toBeInTheDocument();
  });

  test("shows in-progress statements while live", () => {
    render(
      <SummaryStripView summary={summary({ analysing: 2 })} status="live" />,
    );

    expect(screen.getByText(/2 in progress/i)).toBeInTheDocument();
  });
});

describe("LiveSummaryStrip", () => {
  test("reads the shared live snapshot through the provider", () => {
    mockUseLiveAnalysis.mockReturnValue({
      statements: [],
      caption: "",
      status: "live",
      summary: summary({ checked: 9, contradicts: 2 }),
      claimsFor: () => [],
      speakers: [],
    });

    render(
      <LiveAnalysisProvider videoId="vid-1">
        <LiveSummaryStrip />
      </LiveAnalysisProvider>,
    );

    expect(screen.getByLabelText("Checked: 9")).toBeInTheDocument();
    expect(screen.getByLabelText("Contradicted: 2")).toBeInTheDocument();
  });

  test("idles when no video is active", () => {
    render(
      <LiveAnalysisProvider videoId={null}>
        <LiveSummaryStrip />
      </LiveAnalysisProvider>,
    );

    expect(screen.getByText(/findings appear here/i)).toBeInTheDocument();
  });
});

import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import { LiveAnalysisProvider } from "@/components/live/live-analysis-provider";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  emptyClaims,
} from "@/lib/live/claims";
import { parseLiveFrame } from "@/lib/live/frames";
import type { LiveStatement } from "@/lib/live/statements";
import type { LiveSummary } from "@/lib/live/summary";
import { emptySummary, summarizeStatements } from "@/lib/live/summary";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
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

// Each stat labels itself "<label>: <value>". The labels come from the French
// app dictionary, the default a provider-less render falls back to.
const stats = fr.app.summary.stats;
const statLabel = (key: keyof typeof stats, value: number) =>
  `${stats[key]}: ${value}`;

describe("SummaryStripView", () => {
  test("shows an idle hint when no video is being analysed", () => {
    render(<SummaryStripView summary={null} status="idle" />);

    expect(screen.getByText(fr.app.summary.idleHint)).toBeInTheDocument();
    // No counts are shown in the idle state.
    expect(
      screen.queryByLabelText(new RegExp(`^${stats.checked}:`)),
    ).not.toBeInTheDocument();
  });

  test("stays quiet for a selected video before analysis has started", () => {
    // A ready-but-paused video has an empty summary and an idle status; nothing
    // is playing yet, so the strip shows the idle hint, not a row of zeros.
    render(<SummaryStripView summary={summary()} status="idle" />);

    expect(screen.getByText(fr.app.summary.idleHint)).toBeInTheDocument();
    expect(
      screen.queryByLabelText(new RegExp(`^${stats.checked}:`)),
    ).not.toBeInTheDocument();
  });

  test("renders the running counts once analysis is live", () => {
    render(
      <SummaryStripView
        summary={summary({
          checked: 5,
          corroborates: 3,
          contradicts: 1,
          unclear: 1,
          unverifiable: 2,
          evidence: 2,
          skipped: 4,
        })}
        status="live"
      />,
    );

    expect(screen.getByLabelText(statLabel("checked", 5))).toBeInTheDocument();
    expect(
      screen.getByLabelText(statLabel("corroborates", 3)),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText(statLabel("contradicts", 1)),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(statLabel("unclear", 1))).toBeInTheDocument();
    // The verify path's unverifiable verdict reads "Invérifiables", matching the
    // per-claim list, instead of being folded into the curated "Incertaines" count.
    expect(
      screen.getByLabelText(statLabel("unverifiable", 2)),
    ).toBeInTheDocument();
    // Supporting evidence is distinguishable from claim verdicts.
    expect(screen.getByLabelText(statLabel("evidence", 2))).toBeInTheDocument();
    expect(screen.getByLabelText(statLabel("skipped", 4))).toBeInTheDocument();
  });

  test("marks the counts as a polite live region for assistive tech", () => {
    render(<SummaryStripView summary={summary({ checked: 1 })} status="live" />);

    const region = screen
      .getByLabelText(statLabel("checked", 1))
      .closest("[aria-live]");
    expect(region).toHaveAttribute("aria-live", "polite");
  });

  test("surfaces a reconnecting indicator while keeping the counts", () => {
    render(
      <SummaryStripView
        summary={summary({ checked: 7, corroborates: 4 })}
        status="reconnecting"
      />,
    );

    expect(screen.getByText(fr.app.connection.reconnecting)).toBeInTheDocument();
    // Counts accumulated before the drop are preserved through a reconnect.
    expect(screen.getByLabelText(statLabel("checked", 7))).toBeInTheDocument();
  });

  test("flags an interrupted session instead of reading all-clear", () => {
    // The fact-check panel surfaces an error alert when analysis breaks; the
    // strip must not contradict it with the cheerful idle hint, so an errored
    // session shows the counts so far and an interrupted indicator.
    render(
      <SummaryStripView summary={summary({ checked: 2 })} status="error" />,
    );

    expect(screen.getByText(fr.app.connection.interrupted)).toBeInTheDocument();
    expect(screen.getByLabelText(statLabel("checked", 2))).toBeInTheDocument();
    expect(screen.queryByText(fr.app.summary.idleHint)).not.toBeInTheDocument();
  });

  test("shows in-progress statements while live", () => {
    render(
      <SummaryStripView summary={summary({ analysing: 2 })} status="live" />,
    );

    expect(
      screen.getByText(formatTemplate(fr.app.summary.inProgress, { count: 2 })),
    ).toBeInTheDocument();
  });
});

describe("verify-path unverifiable verdict end to end", () => {
  test("a parsed unverifiable claim_result surfaces as an Unverifiable count, not Unclear", () => {
    // Drive the operator-visible path the way a live stream does: a real
    // claim_result frame off the wire, through the claim reducers and the
    // summary projection, into the rendered strip. The verify path's
    // unverifiable verdict must read "Unverifiable" in the strip, matching the
    // per-claim list, and must not inflate the curated "Unclear" count.
    const claimsFrame = parseLiveFrame(
      JSON.stringify({
        type: "claims",
        id: "u1",
        claims: [{ claim_id: "u1-0", text: "the claim" }],
      }),
    );
    const resultFrame = parseLiveFrame(
      JSON.stringify({
        type: "claim_result",
        id: "u1",
        claim_id: "u1-0",
        status: "verified",
        verdict: "unverifiable",
      }),
    );
    if (claimsFrame?.type !== "claims" || resultFrame?.type !== "claim_result") {
      throw new Error("frames failed to parse");
    }

    let claims = applyClaimsFrame(emptyClaims(), claimsFrame);
    claims = applyClaimResultFrame(claims, resultFrame);

    const statements: LiveStatement[] = [
      { id: "u1", start: 0, end: 2, text: "the claim", status: "analysing" },
    ];
    const summary = summarizeStatements(statements, claims);

    render(<SummaryStripView summary={summary} status="live" />);

    expect(
      screen.getByLabelText(statLabel("unverifiable", 1)),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(statLabel("unclear", 0))).toBeInTheDocument();
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

    expect(screen.getByLabelText(statLabel("checked", 9))).toBeInTheDocument();
    expect(
      screen.getByLabelText(statLabel("contradicts", 2)),
    ).toBeInTheDocument();
  });

  test("idles when no video is active", () => {
    render(
      <LiveAnalysisProvider videoId={null}>
        <LiveSummaryStrip />
      </LiveAnalysisProvider>,
    );

    expect(screen.getByText(fr.app.summary.idleHint)).toBeInTheDocument();
  });
});

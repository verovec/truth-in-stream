import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { LiveAnalysisProvider } from "@/components/live/live-analysis-provider";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import { fr } from "@/lib/i18n/dictionaries/fr";
import type { LiveStatement } from "@/lib/live/statements";
import { summarizeStatements } from "@/lib/live/summary";
import { stubScrollLayout } from "@/test/scroll-layout";
import { LiveFactCheckPanel } from "./live-fact-check-panel";

const mockUseLiveAnalysis = vi.hoisted(() => vi.fn<() => LiveAnalysis>());

vi.mock("@/hooks/use-live-analysis", () => ({
  useLiveAnalysis: mockUseLiveAnalysis,
}));

afterEach(() => {
  mockUseLiveAnalysis.mockReset();
});

// The panel reads the shared live snapshot, so a test drives it by mocking the
// hook the provider's driver runs and rendering the panel under the provider.
function renderPanel(
  analysis: Omit<
    LiveAnalysis,
    "summary" | "claimsFor" | "highlightsFor" | "speakers"
  > &
    Partial<Pick<LiveAnalysis, "claimsFor" | "highlightsFor" | "speakers">>,
) {
  mockUseLiveAnalysis.mockReturnValue({
    ...analysis,
    summary: summarizeStatements(analysis.statements),
    claimsFor: analysis.claimsFor ?? (() => []),
    highlightsFor: analysis.highlightsFor ?? (() => []),
    speakers: analysis.speakers ?? [],
  });
  return render(
    <PlaybackProvider>
      <LiveAnalysisProvider videoId="vid-1">
        <LiveFactCheckPanel />
      </LiveAnalysisProvider>
    </PlaybackProvider>,
  );
}

const checked = (
  start: number,
  text: string,
  overrides: Partial<Extract<LiveStatement, { status: "checked" }>> = {},
): LiveStatement => ({
  id: `${start}`,
  start,
  end: start + 1,
  text,
  status: "checked",
  matches: [],
  ...overrides,
});

describe("LiveFactCheckPanel", () => {
  test("reads the active session from the shared live provider", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(mockUseLiveAnalysis).toHaveBeenCalledWith("vid-1", expect.anything());
  });

  test("separates the panel into a subtitles region and a fact-checks region", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(
      screen.getByRole("region", { name: fr.app.panel.subtitlesAria }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: fr.app.panel.factChecks }),
    ).toBeInTheDocument();
  });

  test("offers a labelled resizable divider between the two regions", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(
      screen.getByRole("separator", {
        name: fr.app.panel.separator,
      }),
    ).toBeInTheDocument();
  });

  test("defaults the fact-checks region to the minority of the panel height", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    const separator = screen.getByRole("separator", {
      name: fr.app.panel.separator,
    });
    // aria-valuenow is the subtitles share; a value above 50 means the
    // transcript holds the majority and the fact-checks region is the minority.
    expect(
      Number(separator.getAttribute("aria-valuenow")),
    ).toBeGreaterThan(50);
  });

  test("places the interim caption at the top of the subtitles region", () => {
    renderPanel({
      statements: [checked(0, "an already committed statement")],
      caption: "and this is still being spoken",
      status: "live",
    });
    const subtitles = screen.getByRole("region", { name: fr.app.panel.subtitlesAria });
    const caption = within(subtitles).getByText(/still being spoken/i);
    const transcript = within(subtitles).getByRole("list", {
      name: fr.app.subtitles.transcriptAria,
    });
    // The live utterance is the newest speech, so it sits above the committed
    // statements rather than at the bottom of the region.
    expect(
      caption.compareDocumentPosition(transcript) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  test("arrow keys repartition the two regions through the divider", async () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    const separator = screen.getByRole("separator", {
      name: fr.app.panel.separator,
    });
    const before = Number(separator.getAttribute("aria-valuenow"));
    separator.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(Number(separator.getAttribute("aria-valuenow"))).toBeGreaterThan(
      before,
    );
  });

  test("shows the idle hint before the stream starts", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(screen.getByText(fr.app.panel.hints.idle)).toBeInTheDocument();
  });

  test("shows a live indicator and renders statements once streaming", () => {
    renderPanel({
      statements: [checked(0, "a checked claim")],
      caption: "",
      status: "live",
    });
    expect(screen.getByText(fr.app.connection.live)).toBeInTheDocument();
    expect(screen.getByText(/a checked claim/i)).toBeInTheDocument();
  });

  test("shows the live caption while an utterance is still being spoken", () => {
    renderPanel({ statements: [], caption: "the earth is", status: "live" });
    // The interim caption shows even with no checked statements, and the idle
    // hint is suppressed so the transcript is visible word by word.
    expect(screen.getByText(/the earth is/i)).toBeInTheDocument();
    expect(screen.queryByText(fr.app.panel.hints.idle)).not.toBeInTheDocument();
    // Clamped to two lines so a long interim utterance cannot grow the subtitle
    // region and shove the committed transcript below it.
    expect(screen.getByText(/the earth is/i).className).toContain("line-clamp-2");
  });

  test("renders verdicts in the fact-check region and scrolls the origin subtitle into the list", async () => {
    const { scrollTo, scrollIntoView, restore } = stubScrollLayout();
    try {
      renderPanel({
        statements: [
          checked(5, "the moon landing happened", {
            matches: [
              {
                kind: "claim",
                claim: "Apollo 11 landed in 1969",
                verdict: "corroborates",
                sources: [{ title: "NASA", url: "https://nasa.gov" }],
                similarity: 0.9,
              },
            ],
          }),
        ],
        caption: "",
        status: "live",
      });

      const factChecks = screen.getByRole("region", { name: fr.app.panel.factChecks });
      expect(
        within(factChecks).getByText(/apollo 11 landed in 1969/i),
      ).toBeInTheDocument();

      scrollTo.mockClear();
      await userEvent.click(
        within(factChecks).getByRole("button", {
          name: /the moon landing happened/i,
        }),
      );

      const subtitles = screen.getByRole("region", { name: fr.app.panel.subtitlesAria });
      const list = within(subtitles).getByRole("list", {
        name: fr.app.subtitles.transcriptAria,
      });
      // Selecting a fact-check reveals its origin by scrolling the subtitle list,
      // never the page.
      expect(scrollTo.mock.instances).toContain(list);
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("highlights the matching fact-check when its transcript line is clicked", async () => {
    renderPanel({
      statements: [
        checked(5, "the moon landing happened", {
          matches: [
            {
              kind: "claim",
              claim: "Apollo 11 landed in 1969",
              verdict: "corroborates",
              sources: [],
              similarity: 0.9,
            },
          ],
        }),
      ],
      caption: "",
      status: "live",
    });

    const subtitles = screen.getByRole("region", { name: fr.app.panel.subtitlesAria });
    // Clicking the transcript line selects it for inspection; this must not seek.
    await userEvent.click(
      within(subtitles).getByText(/the moon landing happened/i),
    );

    const factChecks = screen.getByRole("region", { name: fr.app.panel.factChecks });
    const entry = within(factChecks)
      .getByRole("button", { name: /the moon landing happened/i })
      .closest("li");
    expect(entry).toHaveAttribute("aria-current", "true");
  });

  test("fills the fact-check region from verified claims on the verify path", () => {
    // A verify-path statement stays "analysing" and carries no statement-level
    // matches; its verdicts arrive as atomic claims. The fact-check region must
    // read those claims, otherwise it stays permanently empty.
    renderPanel({
      statements: [
        {
          id: "u1",
          start: 5,
          end: 6,
          text: "the bridge opened in 1937",
          status: "analysing",
        },
      ],
      caption: "",
      status: "live",
      claimsFor: (id) =>
        id === "u1"
          ? [
              {
                claimId: "c0",
                text: "the bridge opened in 1937",
                status: "verified",
                source: "verified",
                verdict: "credible",
              },
            ]
          : [],
    });

    const factChecks = screen.getByRole("region", { name: fr.app.panel.factChecks });
    expect(within(factChecks).getByText(/fiable/i)).toBeInTheDocument();
    expect(
      within(factChecks).getByText(/vérifié sur preuves/i),
    ).toBeInTheDocument();
    // The empty hint is gone once a verdict resolves.
    expect(
      within(factChecks).queryByText(fr.app.factChecks.empty),
    ).not.toBeInTheDocument();
  });

  test("sticks the panel under the app header with a data-independent size", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    // The panel is sticky and pins just below the sticky app header. Its top
    // offset and height anchor to the header-height token, never to the summary or
    // speaker strips above it, so incoming statements, verdicts and speakers grow
    // those strips without ever resizing the panel; content scrolls inside.
    const panel = screen.getByRole("complementary", {
      name: fr.app.panel.heading,
    });
    expect(panel.className).toContain("sticky");
    expect(panel.className).toContain("top-[calc(var(--app-header-h)+0.5rem)]");
    expect(panel.className).toContain("h-[calc(100svh-var(--app-header-h)-1rem)]");
    // The old magic offset that coupled the panel height to the strips is gone.
    expect(panel.className).not.toContain("16rem");
    expect(panel.className).not.toContain("top-4");
  });

  test("re-selecting the same fact-check entry scrolls its origin in again", async () => {
    const { scrollTo, scrollIntoView, restore } = stubScrollLayout();
    try {
      renderPanel({
        statements: [
          checked(5, "the moon landing happened", {
            matches: [
              {
                kind: "claim",
                claim: "Apollo 11 landed in 1969",
                verdict: "corroborates",
                sources: [{ title: "NASA", url: "https://nasa.gov" }],
                similarity: 0.9,
              },
            ],
          }),
        ],
        caption: "",
        status: "live",
      });

      const factChecks = screen.getByRole("region", { name: fr.app.panel.factChecks });
      const entry = within(factChecks).getByRole("button", {
        name: /the moon landing happened/i,
      });

      await userEvent.click(entry);
      scrollTo.mockClear();
      // Same entry again: the id is unchanged, so without the selection tick the
      // scroll effect would not re-run.
      await userEvent.click(entry);

      const subtitles = screen.getByRole("region", { name: fr.app.panel.subtitlesAria });
      const list = within(subtitles).getByRole("list", {
        name: fr.app.subtitles.transcriptAria,
      });
      expect(scrollTo.mock.instances).toContain(list);
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("streams the newest statement to the top of the subtitles region", () => {
    renderPanel({
      statements: [
        checked(0, "the first thing said"),
        checked(10, "the second thing said"),
        checked(20, "the latest thing said"),
      ],
      caption: "",
      status: "live",
    });

    const subtitles = screen.getByRole("region", { name: fr.app.panel.subtitlesAria });
    const rows = within(subtitles).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("the latest thing said");
    expect(rows[rows.length - 1]).toHaveTextContent("the first thing said");
  });

  test("surfaces a reconnecting notice without hiding existing statements", () => {
    renderPanel({
      statements: [checked(0, "earlier verdict")],
      caption: "",
      status: "reconnecting",
    });
    expect(screen.getByText(fr.app.panel.reconnecting)).toBeInTheDocument();
    expect(screen.getByText(/earlier verdict/i)).toBeInTheDocument();
  });

  test("surfaces a non-blocking error alert when analysis fails", () => {
    renderPanel({ statements: [], caption: "", status: "error" });
    expect(screen.getByRole("alert")).toHaveTextContent(
      fr.app.panel.interrupted,
    );
  });
});

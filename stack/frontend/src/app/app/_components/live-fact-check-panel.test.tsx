import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { renderWithPlayback } from "@/test/playback";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import type { LiveStatement } from "@/lib/live/statements";
import { LiveFactCheckPanel } from "./live-fact-check-panel";

const mockUseLiveAnalysis = vi.hoisted(() => vi.fn<() => LiveAnalysis>());

vi.mock("@/hooks/use-live-analysis", () => ({
  useLiveAnalysis: mockUseLiveAnalysis,
}));

function renderPanel(analysis: LiveAnalysis) {
  mockUseLiveAnalysis.mockReturnValue(analysis);
  return renderWithPlayback(<LiveFactCheckPanel videoId="vid-1" />);
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
  test("forwards the video id to the live analysis hook", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(mockUseLiveAnalysis).toHaveBeenCalledWith("vid-1");
  });

  test("separates the panel into a subtitles region and a fact-checks region", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(
      screen.getByRole("region", { name: "Live subtitles" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: "Fact checks" }),
    ).toBeInTheDocument();
  });

  test("shows the idle hint before the stream starts", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(
      screen.getByText(/fact checks stream here while the video plays/i),
    ).toBeInTheDocument();
  });

  test("shows a live indicator and renders statements once streaming", () => {
    renderPanel({
      statements: [checked(0, "a checked claim")],
      caption: "",
      status: "live",
    });
    expect(screen.getByText(/^live$/i)).toBeInTheDocument();
    expect(screen.getByText(/a checked claim/i)).toBeInTheDocument();
  });

  test("shows the live caption while an utterance is still being spoken", () => {
    renderPanel({ statements: [], caption: "the earth is", status: "live" });
    // The interim caption shows even with no checked statements, and the idle
    // hint is suppressed so the transcript is visible word by word.
    expect(screen.getByText(/the earth is/i)).toBeInTheDocument();
    expect(
      screen.queryByText(/fact checks stream here while the video plays/i),
    ).not.toBeInTheDocument();
  });

  test("renders verdicts in the fact-check region and selects the origin subtitle", async () => {
    const spy = vi
      .spyOn(HTMLElement.prototype, "scrollIntoView")
      .mockImplementation(() => {});
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

      const factChecks = screen.getByRole("region", { name: "Fact checks" });
      expect(
        within(factChecks).getByText(/apollo 11 landed in 1969/i),
      ).toBeInTheDocument();

      await userEvent.click(
        within(factChecks).getByRole("button", {
          name: /the moon landing happened/i,
        }),
      );

      const subtitles = screen.getByRole("region", { name: "Live subtitles" });
      const originRow = within(subtitles)
        .getByText("the moon landing happened")
        .closest("li");
      expect(spy.mock.instances).toContain(originRow);
    } finally {
      spy.mockRestore();
    }
  });

  test("re-selecting the same fact-check entry scrolls its origin in again", async () => {
    const spy = vi
      .spyOn(HTMLElement.prototype, "scrollIntoView")
      .mockImplementation(() => {});
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

      const factChecks = screen.getByRole("region", { name: "Fact checks" });
      const entry = within(factChecks).getByRole("button", {
        name: /the moon landing happened/i,
      });

      await userEvent.click(entry);
      spy.mockClear();
      // Same entry again: the id is unchanged, so without the selection tick the
      // scroll effect would not re-run.
      await userEvent.click(entry);

      const subtitles = screen.getByRole("region", { name: "Live subtitles" });
      const originRow = within(subtitles)
        .getByText("the moon landing happened")
        .closest("li");
      expect(spy.mock.instances).toContain(originRow);
    } finally {
      spy.mockRestore();
    }
  });

  test("surfaces a reconnecting notice without hiding existing statements", () => {
    renderPanel({
      statements: [checked(0, "earlier verdict")],
      caption: "",
      status: "reconnecting",
    });
    expect(screen.getByText(/connection lost\. reconnecting/i)).toBeInTheDocument();
    expect(screen.getByText(/earlier verdict/i)).toBeInTheDocument();
  });

  test("surfaces a non-blocking error alert when analysis fails", () => {
    renderPanel({ statements: [], caption: "", status: "error" });
    expect(screen.getByRole("alert")).toHaveTextContent(
      /live analysis was interrupted/i,
    );
  });
});

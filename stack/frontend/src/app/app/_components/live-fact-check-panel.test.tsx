import { screen } from "@testing-library/react";
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

const checked = (start: number, text: string): LiveStatement => ({
  id: `${start}`,
  start,
  end: start + 1,
  text,
  status: "checked",
  matches: [],
});

describe("LiveFactCheckPanel", () => {
  test("forwards the video id to the live analysis hook", () => {
    renderPanel({ statements: [], caption: "", status: "idle" });
    expect(mockUseLiveAnalysis).toHaveBeenCalledWith("vid-1");
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

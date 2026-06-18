import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import type { SpeakerCredibility } from "@/lib/live/speakers";
import { SpeakerCredibilityView } from "./live-speaker-credibility";

const speaker = (
  overrides: Partial<SpeakerCredibility> = {},
): SpeakerCredibility => ({
  speaker: "A",
  score: 0.6,
  credible: 2,
  disputed: 1,
  unverifiable: 0,
  misleadingFraming: 0,
  ...overrides,
});

describe("SpeakerCredibilityView", () => {
  test("renders nothing when there are no speakers", () => {
    const { container: empty } = render(
      <SpeakerCredibilityView speakers={null} />,
    );
    expect(empty).toBeEmptyDOMElement();

    const { container: none } = render(
      <SpeakerCredibilityView speakers={[]} />,
    );
    expect(none).toBeEmptyDOMElement();
  });

  test("renders one row per speaker with score and sample tally", () => {
    render(
      <SpeakerCredibilityView
        speakers={[
          speaker({ speaker: "A", score: 0.62, credible: 3, disputed: 2 }),
          speaker({ speaker: "B", score: 0.4, credible: 1, disputed: 3, unverifiable: 2 }),
        ]}
      />,
    );

    expect(
      screen.getByLabelText(/Intervenant A : 62 % de fiabilité, 5 vérifiées/i),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText(
        /Intervenant B : 40 % de fiabilité, 4 vérifiées · 2 invérifiables/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("62 %")).toBeInTheDocument();
  });

  test("omits the unverifiable tally when there are none", () => {
    render(
      <SpeakerCredibilityView speakers={[speaker({ unverifiable: 0 })]} />,
    );
    expect(screen.queryByText(/invérifiable/i)).not.toBeInTheDocument();
  });

  test("shows a distinct misleading-framing tally separate from the credibility score", () => {
    render(
      <SpeakerCredibilityView
        speakers={[
          speaker({ speaker: "A", credible: 3, disputed: 1, misleadingFraming: 2 }),
        ]}
      />,
    );
    // The framing count is its own affordance, orthogonal to the score: a speaker
    // can be credible overall yet have flagged framing.
    expect(screen.getByText(/2 cadrage/i)).toBeInTheDocument();
  });

  test("omits the misleading-framing tally when there is none", () => {
    render(
      <SpeakerCredibilityView speakers={[speaker({ misleadingFraming: 0 })]} />,
    );
    expect(screen.queryByText(/cadrage/i)).not.toBeInTheDocument();
  });

  test("de-emphasizes a thin sample so an early score reads as tentative", () => {
    render(
      <SpeakerCredibilityView
        speakers={[speaker({ speaker: "A", score: 0.6, credible: 1, disputed: 0 })]}
      />,
    );
    // One checked claim is below the thin threshold: the score is muted rather
    // than coloured as a confident positive verdict.
    const score = screen.getByText("60 %");
    expect(score.className).toContain("text-zinc-400");
  });
});

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
      screen.getByLabelText(/Speaker A: 62% credible, 5 checked/i),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText(/Speaker B: 40% credible, 4 checked · 2 unverifiable/i),
    ).toBeInTheDocument();
    expect(screen.getByText("62%")).toBeInTheDocument();
  });

  test("omits the unverifiable tally when there are none", () => {
    render(
      <SpeakerCredibilityView speakers={[speaker({ unverifiable: 0 })]} />,
    );
    expect(screen.queryByText(/unverifiable/i)).not.toBeInTheDocument();
  });

  test("de-emphasizes a thin sample so an early score reads as tentative", () => {
    render(
      <SpeakerCredibilityView
        speakers={[speaker({ speaker: "A", score: 0.6, credible: 1, disputed: 0 })]}
      />,
    );
    // One checked claim is below the thin threshold: the score is muted rather
    // than coloured as a confident positive verdict.
    const score = screen.getByText("60%");
    expect(score.className).toContain("text-zinc-400");
  });
});

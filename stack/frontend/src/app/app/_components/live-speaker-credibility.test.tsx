import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import type { SpeakerTally } from "@/lib/live/speakers";
import { SpeakerCredibilityView } from "./live-speaker-credibility";

const speaker = (overrides: Partial<SpeakerTally> = {}): SpeakerTally => ({
  speaker: "A",
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

  test("renders one row per speaker with the itemised verdict breakdown", () => {
    render(
      <SpeakerCredibilityView
        speakers={[
          speaker({ speaker: "A", credible: 3, disputed: 2 }),
          speaker({ speaker: "B", credible: 1, disputed: 3, unverifiable: 2 }),
        ]}
      />,
    );

    // The aria-label is comma-joined (locale-neutral) so it does not bake French
    // space-before-colon spacing into the English announcement.
    expect(
      screen.getByLabelText(
        /Intervenant A, 5 affirmations vérifiables, 3 crédibles · 2 contestées/i,
      ),
    ).toBeInTheDocument();
    // French pluralizes with Intl.PluralRules: a count of 1 reads singular
    // ("1 crédible"), while the other counts stay plural.
    expect(
      screen.getByLabelText(
        /Intervenant B, 6 affirmations vérifiables, 1 crédible · 3 contestées · 2 invérifiables/i,
      ),
    ).toBeInTheDocument();
  });

  test("shows no rolled-up percentage or score", () => {
    render(
      <SpeakerCredibilityView
        speakers={[speaker({ credible: 3, disputed: 2 })]}
      />,
    );
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
    expect(screen.queryByText(/fiabilité,/i)).not.toBeInTheDocument();
  });

  test("omits the unverifiable count when there are none", () => {
    render(<SpeakerCredibilityView speakers={[speaker({ unverifiable: 0 })]} />);
    expect(screen.queryByText(/invérifiable/i)).not.toBeInTheDocument();
  });

  test("shows a distinct misleading-framing tally separate from the verdict counts", () => {
    render(
      <SpeakerCredibilityView
        speakers={[
          speaker({ speaker: "A", credible: 3, disputed: 1, misleadingFraming: 2 }),
        ]}
      />,
    );
    // The framing count is its own affordance, orthogonal to the verdict counts: a
    // speaker can make credible claims yet have flagged framing.
    expect(screen.getByText(/2 cadrages trompeurs/i)).toBeInTheDocument();
  });

  test("omits the misleading-framing tally when there is none", () => {
    render(
      <SpeakerCredibilityView speakers={[speaker({ misleadingFraming: 0 })]} />,
    );
    expect(screen.queryByText(/cadrage/i)).not.toBeInTheDocument();
  });

  test("renders no colour-banded score element", () => {
    const { container } = render(
      <SpeakerCredibilityView
        speakers={[speaker({ credible: 5, disputed: 0 })]}
      />,
    );
    // The old widget tinted a score green/red; the itemised breakdown carries no
    // such verdict-palette element.
    expect(container.querySelector(".text-emerald-700")).toBeNull();
    expect(container.querySelector(".text-rose-700")).toBeNull();
  });
});

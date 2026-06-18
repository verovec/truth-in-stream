import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveClaim } from "@/lib/live/claims";
import { VerifiedClaim } from "./live-claim-verdict";

const claimMatch = (
  overrides: Partial<Extract<SegmentMatch, { kind: "claim" }>> = {},
): SegmentMatch => ({
  kind: "claim",
  claim: "le chômage est à 7,5 %",
  verdict: "corroborates",
  sources: [{ title: "INSEE", url: "https://insee.fr/chomage" }],
  similarity: 0.9,
  ...overrides,
});

const evidenceMatch = (): SegmentMatch => ({
  kind: "evidence",
  excerpt: "un extrait de contexte",
  article: { title: "Chômage en France", url: "https://fr.wikipedia.org/wiki/x" },
  similarity: 0.7,
});

const verified = (overrides: Partial<LiveClaim> = {}): LiveClaim => ({
  claimId: "u0-0",
  text: "une affirmation",
  status: "verified",
  ...overrides,
});

describe("VerifiedClaim two-axis display", () => {
  test("renders the literal badge for each literal verdict", () => {
    render(<VerifiedClaim claim={verified({ literal: "accurate" })} />);
    expect(screen.getByText("Exact")).toBeInTheDocument();
  });

  test("renders flag chips for a flagged claim", () => {
    render(
      <VerifiedClaim
        claim={verified({
          literal: "accurate",
          flags: ["cherry-picked", "outdated"],
        })}
      />,
    );
    expect(screen.getByText("Données triées")).toBeInTheDocument();
    expect(screen.getByText("Périmé")).toBeInTheDocument();
  });

  test("renders no flag row for a flagless claim", () => {
    render(<VerifiedClaim claim={verified({ literal: "accurate" })} />);
    expect(
      screen.queryByLabelText("Drapeaux de manipulation"),
    ).not.toBeInTheDocument();
  });

  test("renders a legacy claim with no literal axis using its credibility verdict", () => {
    render(<VerifiedClaim claim={verified({ verdict: "credible" })} />);
    expect(screen.getByText("Fiable")).toBeInTheDocument();
    expect(screen.queryByText("Exact")).not.toBeInTheDocument();
  });

  test("shows a single Invérifiable badge when the literal and credibility axes collapse", () => {
    // The credibility verdict is derived from the literal one, so an unverifiable
    // literal yields an unverifiable credibility verdict; both badges would read
    // "Invérifiable", so only the literal axis is shown to avoid a duplicate.
    render(
      <VerifiedClaim
        claim={verified({ literal: "unverifiable", verdict: "unverifiable" })}
      />,
    );
    expect(screen.getAllByText("Invérifiable")).toHaveLength(1);
  });

  test("keeps both axes when an accurate claim is disputed on credibility", () => {
    render(
      <VerifiedClaim claim={verified({ literal: "accurate", verdict: "disputed" })} />,
    );
    expect(screen.getByText("Exact")).toBeInTheDocument();
    expect(screen.getByText("Contesté")).toBeInTheDocument();
  });
});

describe("VerifiedClaim primary source", () => {
  test("shows the primary source name, url, and quoted span from the first cited match", () => {
    render(
      <VerifiedClaim
        claim={verified({
          literal: "accurate",
          matches: [claimMatch(), evidenceMatch()],
        })}
      />,
    );
    const link = screen.getByRole("link", { name: /INSEE/ });
    expect(link).toHaveAttribute("href", "https://insee.fr/chomage");
    expect(screen.getByText(/le chômage est à 7,5 %/)).toBeInTheDocument();
  });

  test("uses the article attribution and excerpt for an evidence-only citation", () => {
    render(
      <VerifiedClaim
        claim={verified({ literal: "unverifiable", matches: [evidenceMatch()] })}
      />,
    );
    const link = screen.getByRole("link", { name: /Chômage en France/ });
    expect(link).toHaveAttribute("href", "https://fr.wikipedia.org/wiki/x");
    expect(screen.getByText(/un extrait de contexte/)).toBeInTheDocument();
  });

  test("renders no primary-source affordance when the claim cites nothing", () => {
    render(<VerifiedClaim claim={verified({ literal: "unverifiable" })} />);
    expect(
      screen.queryByLabelText("Source principale"),
    ).not.toBeInTheDocument();
  });

  test("prefers a named claim source over a preceding evidence fallback", () => {
    // A curated/primary claim source outranks a Wikipedia evidence fallback even
    // when the evidence match comes first in the array.
    render(
      <VerifiedClaim
        claim={verified({
          literal: "accurate",
          matches: [evidenceMatch(), claimMatch()],
        })}
      />,
    );
    const source = screen.getByLabelText("Source principale");
    expect(within(source).getByRole("link", { name: /INSEE/ })).toBeInTheDocument();
    expect(within(source).getByText(/le chômage est à 7,5 %/)).toBeInTheDocument();
  });
});

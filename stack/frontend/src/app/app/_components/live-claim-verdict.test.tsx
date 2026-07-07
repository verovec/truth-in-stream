import { fireEvent, render, screen, within } from "@testing-library/react";
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

// A routed source-pack passage: kind "evidence" but with a real publisher and
// no genuine article, as the political path emits for INSEE / voting / press.
const sourcePackMatch = (
  overrides: Partial<Extract<SegmentMatch, { kind: "evidence" }>> = {},
): SegmentMatch => ({
  kind: "evidence",
  excerpt: "taux de chômage 7,5 %",
  article: { title: "Wikipedia", url: "https://www.wikipedia.org" },
  sources: [{ title: "INSEE", url: "https://insee.fr/chomage" }],
  similarity: 1,
  ...overrides,
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

describe("VerifiedClaim source label chip", () => {
  test("renders the visible 'Source :' prefix and links the source url", () => {
    render(
      <VerifiedClaim
        claim={verified({
          verdict: "credible",
          sourceLabel: "INSEE",
          sourceUrl: "https://insee.fr/x",
        })}
      />,
    );
    const chip = screen.getByRole("link", { name: "Source : INSEE" });
    expect(chip).toHaveAttribute("href", "https://insee.fr/x");
  });

  test("renders the 'Source :' prefix as plain text when no url is present", () => {
    render(
      <VerifiedClaim
        claim={verified({ verdict: "credible", sourceLabel: "Wikipédia" })}
      />,
    );
    const chip = screen.getByText("Source : Wikipédia");
    expect(chip).toBeInTheDocument();
    expect(chip).not.toHaveAttribute("href");
  });

  test("renders no source chip for a knowledge-only verdict with no label", () => {
    render(
      <VerifiedClaim
        claim={verified({ verdict: "unverifiable", basis: "knowledge" })}
      />,
    );
    expect(screen.queryByText(/^Source : /)).not.toBeInTheDocument();
  });

  test("keeps the provider chip distinct from the curated/verified origin tag", () => {
    render(
      <VerifiedClaim
        claim={verified({
          verdict: "credible",
          source: "verified",
          sourceLabel: "INSEE",
          sourceUrl: "https://insee.fr/x",
        })}
      />,
    );
    expect(screen.getByText("vérifié sur preuves")).toBeInTheDocument();
    expect(screen.getByText("Source : INSEE")).toBeInTheDocument();
  });

  test("shows the evidence id and contribution in the operator detail panel", () => {
    render(
      <VerifiedClaim
        claim={verified({
          verdict: "credible",
          sourceLabel: "INSEE",
          matches: [
            claimMatch({ evidenceId: "insee:CHOM:0", contribution: 0.42 }),
          ],
        })}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Voir le détail" }));
    expect(screen.getByText("insee:CHOM:0")).toBeInTheDocument();
    expect(screen.getByText(/contribution 0\.42/)).toBeInTheDocument();
  });

  test("credits the real publisher for a routed source-pack citation, not Wikipedia", () => {
    render(
      <VerifiedClaim
        claim={verified({
          verdict: "credible",
          sourceLabel: "INSEE",
          matches: [sourcePackMatch({ evidenceId: "insee:CHOM:0" })],
        })}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Voir le détail" }));
    const links = screen.getAllByRole("link", { name: "INSEE" });
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) {
      expect(link).toHaveAttribute("href", "https://insee.fr/chomage");
    }
    expect(
      screen.queryByRole("link", { name: /Wikipedia/ }),
    ).not.toBeInTheDocument();
  });
});

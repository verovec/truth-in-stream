import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import type { FactCheckEntry } from "@/lib/live/fact-checks";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { LiveFactCheckList } from "./live-fact-check-list";

const claimEntry: FactCheckEntry = {
  kind: "match",
  key: "s1:0",
  statementId: "s1",
  start: 12,
  snippet: "the earth is round",
  match: {
    kind: "claim",
    claim: "Earth is an oblate spheroid",
    verdict: "corroborates",
    sources: [{ title: "NASA", url: "https://nasa.gov" }],
    similarity: 0.92,
  },
};

const evidenceEntry: FactCheckEntry = {
  kind: "match",
  key: "s2:0",
  statementId: "s2",
  start: 30,
  snippet: "earth is a planet",
  match: {
    kind: "evidence",
    excerpt: "Earth is the third planet from the Sun",
    article: { title: "Earth", url: "https://en.wikipedia.org/wiki/Earth" },
    similarity: 0.8,
  },
};

const verifiedClaimEntry: FactCheckEntry = {
  kind: "claim",
  key: "s3:claim:c0",
  statementId: "s3",
  start: 45,
  snippet: "the bridge opened in 1937",
  claim: {
    claimId: "c0",
    text: "the bridge opened in 1937",
    status: "verified",
    source: "verified",
    verdict: "disputed",
    rationale: "the source gives a different year",
    matches: [
      {
        kind: "evidence",
        excerpt: "Construction finished in 1937.",
        article: { title: "Golden Gate Bridge", url: "https://en.wikipedia.org/wiki/x" },
        similarity: 0.8,
      },
    ],
  },
};

describe("LiveFactCheckList", () => {
  test("renders a claim verdict with its sources and the originating subtitle", () => {
    render(
      <LiveFactCheckList
        entries={[claimEntry]}
        selectedStatementId={null}
        onSelect={() => {}}
      />,
    );

    expect(screen.getByText(/oblate spheroid/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "NASA" })).toHaveAttribute(
      "href",
      "https://nasa.gov",
    );
    // Origin reference back to the subtitle: timestamp and a snippet.
    expect(screen.getByText("0:12")).toBeInTheDocument();
    expect(screen.getByText(/the earth is round/i)).toBeInTheDocument();
  });

  test("renders evidence with its Wikipedia attribution", () => {
    render(
      <LiveFactCheckList
        entries={[evidenceEntry]}
        selectedStatementId={null}
        onSelect={() => {}}
      />,
    );

    expect(screen.getByText(/third planet from the sun/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Earth" })).toHaveAttribute(
      "href",
      "https://en.wikipedia.org/wiki/Earth",
    );
  });

  test("renders a verified claim's verdict, revealing its reasoning on tap", async () => {
    const user = userEvent.setup();
    render(
      <LiveFactCheckList
        entries={[verifiedClaimEntry]}
        selectedStatementId={null}
        onSelect={() => {}}
      />,
    );

    // The verdict and the primary-source span show at once; the rationale is
    // hidden until tapped.
    expect(screen.getByText(/contesté/i)).toBeInTheDocument();
    expect(screen.getByText("0:45")).toBeInTheDocument();
    expect(
      screen.getByText(/construction finished in 1937/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/the source gives a different year/i),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /voir le détail/i }));
    expect(
      screen.getByText(/the source gives a different year/i),
    ).toBeInTheDocument();
    // The excerpt now appears in both the primary-source preview and the
    // expanded citation list.
    expect(
      screen.getAllByText(/construction finished in 1937/i).length,
    ).toBeGreaterThanOrEqual(2);
  });

  test("selecting an entry reports its originating statement id", async () => {
    const onSelect = vi.fn();
    render(
      <LiveFactCheckList
        entries={[claimEntry]}
        selectedStatementId={null}
        onSelect={onSelect}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /the earth is round/i }));
    expect(onSelect).toHaveBeenCalledWith("s1");
  });

  test("marks the selected entry for the operator", () => {
    render(
      <LiveFactCheckList
        entries={[claimEntry, evidenceEntry]}
        selectedStatementId="s1"
        onSelect={() => {}}
      />,
    );

    const selected = screen
      .getByText(/oblate spheroid/i)
      .closest("li");
    expect(selected).toHaveAttribute("aria-current", "true");
  });

  test("shows an empty hint when no fact-checks have resolved", () => {
    render(
      <LiveFactCheckList
        entries={[]}
        selectedStatementId={null}
        onSelect={() => {}}
      />,
    );

    expect(screen.getByText(fr.app.factChecks.empty)).toBeInTheDocument();
  });
});

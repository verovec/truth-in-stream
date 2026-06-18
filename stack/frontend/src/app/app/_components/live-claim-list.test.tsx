import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveClaim } from "@/lib/live/claims";
import { LiveClaimList } from "./live-claim-list";

const claim = (overrides: Partial<LiveClaim> & Pick<LiveClaim, "claimId">): LiveClaim => ({
  text: "the bridge opened in 1937",
  status: "pending",
  ...overrides,
});

const evidence = (excerpt: string): SegmentMatch => ({
  kind: "evidence",
  excerpt,
  article: { title: "Golden Gate Bridge", url: "https://en.wikipedia.org/wiki/x" },
  similarity: 0.8,
});

describe("LiveClaimList", () => {
  test("renders nothing for an empty claim list (legacy stream)", () => {
    const { container } = render(<LiveClaimList claims={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  test("a pending claim shows it is queued", () => {
    render(<LiveClaimList claims={[claim({ claimId: "c0" })]} />);
    expect(screen.getByText(/en attente de vérification/i)).toBeInTheDocument();
  });

  test("a checking claim shows a checking placeholder", () => {
    render(
      <LiveClaimList claims={[claim({ claimId: "c0", status: "checking" })]} />,
    );
    expect(screen.getByText(/^vérification…$/i)).toBeInTheDocument();
  });

  test("a verified verdict reveals its rationale and citations only on tap", async () => {
    const user = userEvent.setup();
    render(
      <LiveClaimList
        claims={[
          claim({
            claimId: "c0",
            status: "verified",
            source: "verified",
            verdict: "disputed",
            rationale: "the source gives a different year",
            matches: [evidence("Construction finished in 1937.")],
          }),
        ]}
      />,
    );

    // The verdict and the primary-source span are visible at once; the rationale
    // is hidden until tapped.
    expect(screen.getByText(/contesté/i)).toBeInTheDocument();
    expect(screen.getByText(/Construction finished in 1937/i)).toBeInTheDocument();
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
      screen.getAllByText(/Construction finished in 1937/i).length,
    ).toBeGreaterThanOrEqual(2);
  });

  test("distinguishes a curated verdict from a verified one", () => {
    render(
      <LiveClaimList
        claims={[
          claim({ claimId: "c0", status: "verified", source: "curated", verdict: "credible" }),
          claim({ claimId: "c1", status: "verified", source: "verified", verdict: "credible" }),
        ]}
      />,
    );
    expect(screen.getByText(/source vérifiée/i)).toBeInTheDocument();
    expect(screen.getByText(/vérifié sur preuves/i)).toBeInTheDocument();
  });

  test("renders unverifiable as an honest verdict, not an error", () => {
    render(
      <LiveClaimList
        claims={[
          claim({
            claimId: "c0",
            status: "verified",
            source: "verified",
            verdict: "unverifiable",
          }),
        ]}
      />,
    );
    expect(screen.getByText(/invérifiable/i)).toBeInTheDocument();
    expect(screen.queryByText(/n'a pas pu être vérifiée/i)).not.toBeInTheDocument();
  });

  test("marks a knowledge-basis verdict as having no direct sources", () => {
    render(
      <LiveClaimList
        claims={[
          claim({
            claimId: "c0",
            status: "verified",
            source: "verified",
            verdict: "credible",
            basis: "knowledge",
          }),
        ]}
      />,
    );
    expect(screen.getByText(/sans source directe/i)).toBeInTheDocument();
  });

  test("renders an unchecked claim as a capacity terminal state", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "unchecked", skipReason: "not_checked" })]}
      />,
    );
    expect(screen.getByText(/capacité/i)).toBeInTheDocument();
  });

  test("renders an errored claim as an honest terminal state, not a blank row", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "error", error: "verification failed" })]}
      />,
    );
    expect(screen.getByText(/n'a pas pu être vérifiée/i)).toBeInTheDocument();
  });

  test("a verified verdict with no rationale or citations offers no disclosure", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "verified", verdict: "credible" })]}
      />,
    );
    expect(screen.getByText(/fiable/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /voir le détail/i }),
    ).not.toBeInTheDocument();
  });
});

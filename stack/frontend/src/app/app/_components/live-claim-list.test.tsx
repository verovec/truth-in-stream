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
    expect(screen.getByText(/queued for checking/i)).toBeInTheDocument();
  });

  test("a checking claim shows a checking placeholder", () => {
    render(
      <LiveClaimList claims={[claim({ claimId: "c0", status: "checking" })]} />,
    );
    expect(screen.getByText(/^checking…$/i)).toBeInTheDocument();
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

    // The verdict is visible at once; the reasoning is hidden until tapped.
    expect(screen.getByText(/disputed/i)).toBeInTheDocument();
    expect(
      screen.queryByText(/the source gives a different year/i),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /show reasoning/i }));
    expect(
      screen.getByText(/the source gives a different year/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Construction finished in 1937/i)).toBeInTheDocument();
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
    expect(screen.getByText(/from a curated source/i)).toBeInTheDocument();
    expect(screen.getByText(/checked against evidence/i)).toBeInTheDocument();
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
    expect(screen.getByText(/unverifiable/i)).toBeInTheDocument();
    expect(screen.queryByText(/could not be checked/i)).not.toBeInTheDocument();
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
    expect(screen.getByText(/no direct sources/i)).toBeInTheDocument();
  });

  test("renders an unchecked claim as a capacity terminal state", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "unchecked", skipReason: "not_checked" })]}
      />,
    );
    expect(screen.getByText(/at capacity/i)).toBeInTheDocument();
  });

  test("renders an errored claim as an honest terminal state, not a blank row", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "error", error: "verification failed" })]}
      />,
    );
    expect(screen.getByText(/could not be checked/i)).toBeInTheDocument();
  });

  test("a verified verdict with no rationale or citations offers no disclosure", () => {
    render(
      <LiveClaimList
        claims={[claim({ claimId: "c0", status: "verified", verdict: "credible" })]}
      />,
    );
    expect(screen.getByText(/credible/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /show reasoning/i }),
    ).not.toBeInTheDocument();
  });
});

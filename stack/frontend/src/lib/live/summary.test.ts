import { describe, expect, test } from "vitest";
import type { SegmentMatch, Verdict } from "@/lib/fact-check/api";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  type ClaimsState,
  emptyClaims,
} from "./claims";
import type { ClaimStatus, ClaimVerdict, ManipulationFlag } from "./frames";
import type { LiveStatement } from "./statements";
import { emptySummary, summarizeStatements } from "./summary";

const checked = (
  id: string,
  start: number,
  overrides: Partial<Extract<LiveStatement, { status: "checked" }>> = {},
): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text: `statement ${id}`,
  status: "checked",
  matches: [],
  ...overrides,
});

const analysing = (id: string, start: number): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text: `statement ${id}`,
  status: "analysing",
});

const claim = (verdict: Verdict): SegmentMatch => ({
  kind: "claim",
  claim: "a claim",
  verdict,
  sources: [],
  similarity: 0.8,
});

const evidence = (): SegmentMatch => ({
  kind: "evidence",
  excerpt: "an excerpt",
  article: { title: "Earth", url: "https://en.wikipedia.org/wiki/Earth" },
  similarity: 0.7,
});

// unitClaims builds a real ClaimsState for one unit through the same reducers
// the live hook uses, so the summary is exercised against genuine claim state
// rather than a hand-built map.
function unitClaims(
  unitId: string,
  claims: {
    status: ClaimStatus;
    verdict?: ClaimVerdict;
    flags?: ManipulationFlag[];
  }[],
): ClaimsState {
  const ids = claims.map((_, i) => `${unitId}-${i}`);
  let state = applyClaimsFrame(emptyClaims(), {
    type: "claims",
    id: unitId,
    claims: ids.map((claimId, i) => ({
      claimId,
      text: `claim ${i}`,
      status: "pending" as const,
    })),
  });
  claims.forEach((c, i) => {
    state = applyClaimResultFrame(state, {
      type: "claim_result",
      id: unitId,
      claimId: ids[i],
      status: c.status,
      verdict: c.verdict,
      flags: c.flags,
    });
  });
  return state;
}

describe("summarizeStatements", () => {
  test("an empty list summarizes to all-zero counts", () => {
    expect(summarizeStatements([])).toEqual(emptySummary());
  });

  test("tallies claim verdicts and evidence across checked statements", () => {
    const summary = summarizeStatements([
      checked("a", 0, { matches: [claim("corroborates"), evidence()] }),
      checked("b", 2, { matches: [claim("contradicts")] }),
      checked("c", 4, { matches: [claim("unclear"), claim("corroborates")] }),
    ]);

    expect(summary).toEqual({
      checked: 3,
      corroborates: 2,
      contradicts: 1,
      unclear: 1,
      unverifiable: 0,
      evidence: 1,
      misleadingFraming: 0,
      skipped: 0,
      analysing: 0,
    });
  });

  test("counts a checked statement with no matches as checked but contributes no verdicts", () => {
    const summary = summarizeStatements([checked("a", 0, { matches: [] })]);

    expect(summary).toMatchObject({
      checked: 1,
      corroborates: 0,
      contradicts: 0,
      unclear: 0,
      evidence: 0,
    });
  });

  test("counts skipped and errored statements as not-checked, never as verdicts", () => {
    // A skipped or errored statement is excluded from verdict/evidence tallies
    // even if it somehow carries matches, mirroring the fact-check list: a row
    // marked not-checked can never also show a verdict in the summary.
    const summary = summarizeStatements([
      checked("skip", 0, { skipReason: "not_a_claim", matches: [claim("corroborates")] }),
      checked("cap", 2, { skipReason: "not_checked", matches: [claim("contradicts")] }),
      checked("err", 4, { error: "analysis failed", matches: [evidence()] }),
    ]);

    expect(summary).toEqual({
      checked: 0,
      corroborates: 0,
      contradicts: 0,
      unclear: 0,
      unverifiable: 0,
      evidence: 0,
      misleadingFraming: 0,
      skipped: 3,
      analysing: 0,
    });
  });

  test("counts still-analysing statements as in-progress, not checked", () => {
    const summary = summarizeStatements([
      analysing("a", 0),
      analysing("b", 2),
      checked("c", 4, { matches: [claim("corroborates")] }),
    ]);

    expect(summary).toMatchObject({ analysing: 2, checked: 1, corroborates: 1 });
  });

  test("partitions every statement into exactly one of checked, skipped, or analysing", () => {
    const statements: LiveStatement[] = [
      analysing("a", 0),
      checked("b", 2, { matches: [claim("unclear")] }),
      checked("c", 4, { skipReason: "not_covered" }),
      checked("d", 6, { error: "boom" }),
      checked("e", 8, { matches: [] }),
    ];

    const summary = summarizeStatements(statements);

    expect(summary.checked + summary.skipped + summary.analysing).toBe(
      statements.length,
    );
  });
});

describe("summarizeStatements on the verify path (claims-aware)", () => {
  test("a unit with a still-checking claim stays in progress, never checked", () => {
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [{ status: "checking" }]),
    );

    expect(summary).toMatchObject({ analysing: 1, checked: 0, skipped: 0 });
  });

  test("a unit resolves once every claim is terminal, tallying per-claim verdicts", () => {
    // The verify path's unverifiable verdict keeps its own bucket rather than
    // collapsing into the curated unclear count, so the strip reads the same
    // word as the per-claim list.
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [
        { status: "verified", verdict: "credible" },
        { status: "verified", verdict: "disputed" },
        { status: "verified", verdict: "unverifiable" },
      ]),
    );

    expect(summary).toEqual({
      checked: 1,
      corroborates: 1,
      contradicts: 1,
      unclear: 0,
      unverifiable: 1,
      evidence: 0,
      misleadingFraming: 0,
      skipped: 0,
      analysing: 0,
    });
  });

  test("a unit with at least one verified claim counts as checked", () => {
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [
        { status: "verified", verdict: "credible" },
        { status: "unchecked" },
      ]),
    );

    expect(summary).toMatchObject({ checked: 1, skipped: 0, corroborates: 1 });
  });

  test("a unit whose claims are all unchecked or errored counts as not checked", () => {
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [{ status: "unchecked" }, { status: "error" }]),
    );

    expect(summary).toEqual({
      checked: 0,
      corroborates: 0,
      contradicts: 0,
      unclear: 0,
      unverifiable: 0,
      evidence: 0,
      misleadingFraming: 0,
      skipped: 1,
      analysing: 0,
    });
  });

  test("a degenerate verified claim with no verdict counts as unverifiable, like the list", () => {
    // live-claim-list renders a verdict-less verified claim as "Unverifiable",
    // so the strip must tally it the same way, never as curated unclear.
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [{ status: "verified" }]),
    );

    expect(summary).toMatchObject({ checked: 1, unverifiable: 1, unclear: 0 });
  });

  test("legacy statements with no claims keep their statement-derived counts", () => {
    // Claims for a unit that has no statement in the list must not leak into the
    // tally, and statements with no claims fall back to the legacy path.
    const summary = summarizeStatements(
      [
        checked("a", 0, { matches: [claim("corroborates")] }),
        analysing("b", 2),
      ],
      unitClaims("orphan", [{ status: "verified", verdict: "credible" }]),
    );

    expect(summary).toEqual({
      checked: 1,
      corroborates: 1,
      contradicts: 0,
      unclear: 0,
      unverifiable: 0,
      evidence: 0,
      misleadingFraming: 0,
      skipped: 0,
      analysing: 1,
    });
  });

  test("mixes legacy and verify-path units in one pass", () => {
    const summary = summarizeStatements(
      [checked("legacy", 0, { matches: [claim("contradicts")] }), analysing("u1", 2)],
      unitClaims("u1", [{ status: "verified", verdict: "credible" }]),
    );

    expect(summary).toMatchObject({
      checked: 2,
      corroborates: 1,
      contradicts: 1,
      analysing: 0,
    });
  });

  test("tallies a flagged verified claim on the misleading-framing axis alongside its verdict", () => {
    // A literally accurate but cherry-picked claim counts both as corroborates
    // (its verdict) and on the orthogonal misleading-framing axis; a flagless
    // disputed claim moves only the verdict count.
    const summary = summarizeStatements(
      [analysing("u1", 0)],
      unitClaims("u1", [
        { status: "verified", verdict: "credible", flags: ["cherry-picked"] },
        { status: "verified", verdict: "disputed" },
      ]),
    );

    expect(summary).toMatchObject({
      checked: 1,
      corroborates: 1,
      contradicts: 1,
      misleadingFraming: 1,
    });
  });
});

import { describe, expect, test } from "vitest";
import type { SegmentMatch } from "@/lib/fact-check/api";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  claimsForUnit,
  dropUnits,
  emptyClaims,
} from "./claims";
import type { ClaimResultFrame, ClaimsFrame, ClaimStatus } from "./frames";

const claimsFrame = (
  unitId: string,
  ...claims: [claimId: string, text: string][]
): ClaimsFrame => ({
  type: "claims",
  id: unitId,
  claims: claims.map(([claimId, text]) => ({ claimId, text, status: "pending" })),
});

const result = (
  unitId: string,
  claimId: string,
  status: ClaimStatus,
  extra: Partial<ClaimResultFrame> = {},
): ClaimResultFrame => ({
  type: "claim_result",
  id: unitId,
  claimId,
  status,
  ...extra,
});

const claimMatch = (claim: string): SegmentMatch => ({
  kind: "claim",
  claim,
  verdict: "corroborates",
  sources: [{ title: "src", url: "https://example.org" }],
  similarity: 0.9,
});

describe("claims store", () => {
  test("a claims frame announces each atomic claim as pending in order", () => {
    const state = applyClaimsFrame(
      emptyClaims(),
      claimsFrame("u0", ["u0-0", "first claim"], ["u0-1", "second claim"]),
    );
    expect(claimsForUnit(state, "u0")).toEqual([
      { claimId: "u0-0", text: "first claim", status: "pending" },
      { claimId: "u0-1", text: "second claim", status: "pending" },
    ]);
  });

  test("pending -> checking -> verified replaces the claim row in place, keyed on claim_id", () => {
    let state = applyClaimsFrame(
      emptyClaims(),
      claimsFrame("u0", ["u0-0", "the bridge opened in 1937"]),
    );
    expect(claimsForUnit(state, "u0")[0].status).toBe("pending");

    state = applyClaimResultFrame(state, result("u0", "u0-0", "checking"));
    expect(claimsForUnit(state, "u0")[0].status).toBe("checking");

    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "verified", {
        source: "verified",
        verdict: "credible",
        confidence: 0.82,
        rationale: "the source confirms the year",
        matches: [claimMatch("opened 1937")],
      }),
    );
    const claims = claimsForUnit(state, "u0");
    // Replaced in place: still one row under the same claim_id, now the verdict.
    expect(claims).toHaveLength(1);
    expect(claims[0]).toMatchObject({
      claimId: "u0-0",
      text: "the bridge opened in 1937",
      status: "verified",
      source: "verified",
      verdict: "credible",
      confidence: 0.82,
    });
    expect(claims[0].matches).toHaveLength(1);
  });

  test("a curated verdict is tagged its source distinctly from a verified one", () => {
    let state = applyClaimsFrame(
      emptyClaims(),
      claimsFrame("u0", ["u0-0", "borrowed"], ["u0-1", "reasoned"]),
    );
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "verified", {
        source: "curated",
        verdict: "disputed",
      }),
    );
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-1", "verified", {
        source: "verified",
        verdict: "credible",
      }),
    );
    const claims = claimsForUnit(state, "u0");
    expect(claims[0]).toMatchObject({ source: "curated", verdict: "disputed" });
    expect(claims[1]).toMatchObject({ source: "verified", verdict: "credible" });
  });

  test("not_enough_info is a terminal verified verdict, not an error", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "c"]));
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "verified", {
        source: "verified",
        verdict: "unverifiable",
        confidence: 0,
      }),
    );
    expect(claimsForUnit(state, "u0")[0]).toMatchObject({
      status: "verified",
      verdict: "unverifiable",
      error: undefined,
    });
  });

  test("an unchecked claim is an honest terminal capacity state", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "c"]));
    state = applyClaimResultFrame(state, result("u0", "u0-0", "checking"));
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "unchecked", { skipReason: "not_checked" }),
    );
    expect(claimsForUnit(state, "u0")[0]).toMatchObject({
      status: "unchecked",
      skipReason: "not_checked",
    });
  });

  test("an errored claim is terminal and distinct from a verdict", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "c"]));
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "error", { error: "verification failed" }),
    );
    expect(claimsForUnit(state, "u0")[0]).toMatchObject({
      status: "error",
      verdict: undefined,
      error: "verification failed",
    });
  });

  test("a late checking placeholder never downgrades a verdict already shown", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "c"]));
    state = applyClaimResultFrame(
      state,
      result("u0", "u0-0", "verified", { verdict: "credible" }),
    );
    state = applyClaimResultFrame(state, result("u0", "u0-0", "checking"));
    expect(claimsForUnit(state, "u0")[0]).toMatchObject({
      status: "verified",
      verdict: "credible",
    });
  });

  test("a result arriving before its claims frame still renders and keeps its verdict", () => {
    let state = applyClaimResultFrame(
      emptyClaims(),
      result("u0", "u0-0", "verified", { verdict: "credible" }),
    );
    // The announcement lands after the verdict (reconnect replay): the verdict is
    // kept and the announced text backfilled.
    state = applyClaimsFrame(state, claimsFrame("u0", ["u0-0", "the claim text"]));
    expect(claimsForUnit(state, "u0")[0]).toMatchObject({
      status: "verified",
      verdict: "credible",
      text: "the claim text",
    });
  });

  test("two units keep their claims independent", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "a"]));
    state = applyClaimsFrame(state, claimsFrame("u1", ["u1-0", "b"]));
    expect(claimsForUnit(state, "u0")).toHaveLength(1);
    expect(claimsForUnit(state, "u1")).toHaveLength(1);
    expect(claimsForUnit(state, "u0")[0].text).toBe("a");
    expect(claimsForUnit(state, "u1")[0].text).toBe("b");
  });

  test("claimsForUnit is empty for a unit with no claims (legacy stream)", () => {
    expect(claimsForUnit(emptyClaims(), "missing")).toEqual([]);
  });

  test("dropUnits keeps only the surviving units' claims", () => {
    let state = applyClaimsFrame(emptyClaims(), claimsFrame("keep", ["k-0", "a"]));
    state = applyClaimsFrame(state, claimsFrame("drop", ["d-0", "b"]));
    state = dropUnits(state, new Set(["keep"]));
    expect(claimsForUnit(state, "keep")).toHaveLength(1);
    expect(claimsForUnit(state, "drop")).toEqual([]);
  });

  test("applyClaimsFrame does not mutate the prior state", () => {
    const first = applyClaimsFrame(emptyClaims(), claimsFrame("u0", ["u0-0", "a"]));
    const second = applyClaimResultFrame(
      first,
      result("u0", "u0-0", "verified", { verdict: "credible" }),
    );
    expect(claimsForUnit(first, "u0")[0].status).toBe("pending");
    expect(claimsForUnit(second, "u0")[0].status).toBe("verified");
  });
});

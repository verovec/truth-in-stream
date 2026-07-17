import { describe, expect, test } from "vitest";
import { applyClaimResultFrame, applyClaimsFrame, emptyClaims } from "./claims";
import type { ClaimsFrame } from "./frames";
import {
  type ClaimHighlight,
  claimHighlights,
  segmentTextParts,
} from "./highlight";

const highlight = (
  overrides: Partial<ClaimHighlight> = {},
): ClaimHighlight => ({
  unitId: "u0",
  claimId: "u0-0",
  start: 0,
  end: 1,
  status: "pending",
  ...overrides,
});

describe("claimHighlights", () => {
  const frame: ClaimsFrame = {
    type: "claims",
    id: "u0",
    claims: [
      {
        claimId: "u0-0",
        text: "Le chomage en France a baisse.",
        status: "pending",
        quote: "chomage a baisse",
        spans: [{ segmentId: "s1", start: 3, end: 19 }],
      },
      {
        claimId: "u0-1",
        text: "Les impots ont augmente.",
        status: "pending",
        quote: "impots ont augmente. Oui",
        spans: [
          { segmentId: "s1", start: 21, end: 40 },
          { segmentId: "s2", start: 0, end: 3 },
        ],
      },
      { claimId: "u0-2", text: "Sans ancrage.", status: "pending" },
    ],
  };

  test("indexes spans by segment id, joined with status and quote", () => {
    const state = applyClaimsFrame(emptyClaims(), frame);
    const index = claimHighlights(state);
    expect(index.get("s1")).toEqual([
      {
        unitId: "u0",
        claimId: "u0-0",
        start: 3,
        end: 19,
        status: "pending",
        verdict: undefined,
        quote: "chomage a baisse",
      },
      {
        unitId: "u0",
        claimId: "u0-1",
        start: 21,
        end: 40,
        status: "pending",
        verdict: undefined,
        quote: "impots ont augmente. Oui",
      },
    ]);
    expect(index.get("s2")).toHaveLength(1);
    // The unanchored claim contributes nothing anywhere.
    expect([...index.values()].flat()).toHaveLength(3);
  });

  test("a claim verdict retints its highlight in place", () => {
    let state = applyClaimsFrame(emptyClaims(), frame);
    state = applyClaimResultFrame(state, {
      type: "claim_result",
      id: "u0",
      claimId: "u0-0",
      status: "verified",
      verdict: "disputed",
    });
    const index = claimHighlights(state);
    const first = index.get("s1")?.at(0);
    expect(first?.status).toBe("verified");
    expect(first?.verdict).toBe("disputed");
    // The sibling claim keeps its pending highlight untouched.
    expect(index.get("s1")?.at(1)?.status).toBe("pending");
  });
});

describe("segmentTextParts", () => {
  test("no highlights yields the whole text as one plain part", () => {
    expect(segmentTextParts("Le chomage a baisse.", [])).toEqual([
      { text: "Le chomage a baisse." },
    ]);
  });

  test("slices around one highlight and reassembles the exact text", () => {
    const h = highlight({ start: 3, end: 19 });
    const parts = segmentTextParts("Le chomage a baisse fortement.", [h]);
    expect(parts).toEqual([
      { text: "Le " },
      { text: "chomage a baisse", highlight: h },
      { text: " fortement." },
    ]);
    expect(parts.map((p) => p.text).join("")).toBe(
      "Le chomage a baisse fortement.",
    );
  });

  test("offsets count code points, never splitting a surrogate pair", () => {
    // "𝔉" is an astral character: one code point, two UTF-16 units. The
    // highlight targets the two characters after it.
    const h = highlight({ start: 1, end: 3 });
    const parts = segmentTextParts("𝔉ab c", [h]);
    expect(parts).toEqual([
      { text: "𝔉" },
      { text: "ab", highlight: h },
      { text: " c" },
    ]);
  });

  test("clamps a span past the end and drops an empty one", () => {
    const over = highlight({ start: 4, end: 99 });
    const empty = highlight({ start: 99, end: 120 });
    expect(segmentTextParts("abcdef", [over, empty])).toEqual([
      { text: "abcd" },
      { text: "ef", highlight: over },
    ]);
  });

  test("overlapping spans trim to first-come order", () => {
    const first = highlight({ claimId: "a", start: 0, end: 4 });
    const second = highlight({ claimId: "b", start: 2, end: 6 });
    const parts = segmentTextParts("abcdef", [second, first]);
    expect(parts).toEqual([
      { text: "abcd", highlight: first },
      { text: "ef", highlight: second },
    ]);
  });

  test("a slice that is not part of the claim's quote renders plain", () => {
    // The offsets were computed against a different session's text that reused
    // this segment id (the TV reconnect case): the quote no longer matches, so
    // no words are marked - marking the wrong words would be worse.
    const stale = highlight({ start: 0, end: 10, quote: "le chomage a baisse" });
    expect(segmentTextParts("Une toute autre phrase.", [stale])).toEqual([
      { text: "Une toute autre phrase." },
    ]);
  });

  test("a boundary-crossing fragment of the quote still matches", () => {
    // A span covers only its segment's share of a cross-segment quote, so the
    // guard tests substring-of-quote, with case and whitespace folded.
    const fragment = highlight({
      start: 0,
      end: 10,
      quote: "baisse. Les IMPOTS",
    });
    const parts = segmentTextParts("Les impots montent.", [fragment]);
    expect(parts[0]).toEqual({ text: "Les impots", highlight: fragment });
  });
});

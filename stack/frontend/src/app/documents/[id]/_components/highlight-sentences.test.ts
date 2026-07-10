import { describe, expect, test } from "vitest";
import type { DocumentSentence } from "@/lib/documents/api";
import type { LiveClaim } from "@/lib/live/claims";
import type { ClaimVerdict } from "@/lib/live/frames";
import { toHighlightSentences } from "./highlight-sentences";

function claim(verdict: ClaimVerdict, text: string): LiveClaim {
  return {
    claimId: `c-${verdict}-${text}`,
    text,
    status: "verified",
    verdict,
    confidence: 0.8,
    matches: [],
  };
}

function sentence(over: Partial<DocumentSentence> = {}): DocumentSentence {
  return {
    seq: 0,
    page: 1,
    text: "Une phrase analysée.",
    occurrence: 1,
    skipReason: "",
    claims: [],
    ...over,
  };
}

describe("toHighlightSentences", () => {
  test("keeps only credible and disputed sentences", () => {
    const highlights = toHighlightSentences([
      sentence({ seq: 0, claims: [claim("credible", "A")] }),
      sentence({ seq: 1, claims: [claim("disputed", "B")] }),
      sentence({ seq: 2, claims: [claim("unverifiable", "C")] }),
      sentence({ seq: 3, skipReason: "not_a_claim" }),
      sentence({ seq: 4, skipReason: "not_covered" }),
      sentence({ seq: 5 }),
    ]);
    expect(highlights.map((highlight) => highlight.seq)).toEqual([0, 1]);
  });

  test("carries page, occurrence, and the anchoring text through", () => {
    const highlights = toHighlightSentences([
      sentence({
        seq: 7,
        page: 3,
        occurrence: 2,
        text: "Le chômage a baissé.",
        claims: [claim("credible", "Le chômage a baissé")],
      }),
    ]);
    expect(highlights).toEqual([
      {
        seq: 7,
        page: 3,
        text: "Le chômage a baissé.",
        occurrence: 2,
        verdict: "credible",
        snippet: "Le chômage a baissé",
      },
    ]);
  });

  test("a disputed claim colors the sentence over a credible one", () => {
    const highlights = toHighlightSentences([
      sentence({
        claims: [claim("credible", "vrai"), claim("disputed", "faux")],
      }),
    ]);
    expect(highlights[0].verdict).toBe("disputed");
    expect(highlights[0].snippet).toBe("faux");
  });

  test("uses the first credible claim's text as the snippet", () => {
    const highlights = toHighlightSentences([
      sentence({
        claims: [
          claim("unverifiable", "flou"),
          claim("credible", "établi"),
        ],
      }),
    ]);
    expect(highlights[0].verdict).toBe("credible");
    expect(highlights[0].snippet).toBe("établi");
  });
});

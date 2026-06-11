import { describe, expect, test } from "vitest";
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveStatement } from "./statements";
import { deriveFactChecks } from "./fact-checks";

const checked = (
  id: string,
  start: number,
  text: string,
  overrides: Partial<Extract<LiveStatement, { status: "checked" }>> = {},
): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text,
  status: "checked",
  matches: [],
  ...overrides,
});

const analysing = (id: string, start: number, text: string): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text,
  status: "analysing",
});

describe("deriveFactChecks", () => {
  test("emits one entry per match on a checked statement, carrying the origin", () => {
    const entries = deriveFactChecks([
      checked("s1", 4, "the earth is round", {
        matches: [
          {
            kind: "claim",
            claim: "Earth is an oblate spheroid",
            verdict: "corroborates",
            sources: [{ title: "NASA", url: "https://nasa.gov" }],
            similarity: 0.92,
          },
          {
            kind: "evidence",
            excerpt: "Earth is the third planet",
            article: { title: "Earth", url: "https://en.wikipedia.org/wiki/Earth" },
            similarity: 0.81,
          },
        ],
      }),
    ]);

    expect(entries).toHaveLength(2);
    expect(entries[0]).toMatchObject({
      statementId: "s1",
      start: 4,
      snippet: "the earth is round",
      match: { kind: "claim", verdict: "corroborates" },
    });
    expect(entries[1].match.kind).toBe("evidence");
    // Keys are unique so React can list them without index collisions.
    expect(new Set(entries.map((e) => e.key)).size).toBe(2);
  });

  test("ignores analysing, errored, skipped, and no-match statements", () => {
    const verdict: SegmentMatch = {
      kind: "claim",
      claim: "leaked verdict",
      verdict: "corroborates",
      sources: [],
      similarity: 0.9,
    };
    const entries = deriveFactChecks([
      analysing("a1", 0, "still being checked"),
      // Errored and skipped statements are excluded even when they carry
      // matches, so a row marked "Not checked" can never also show a verdict.
      checked("e1", 2, "broke", {
        error: "analysis failed",
        matches: [verdict],
      }),
      checked("k1", 4, "small talk", {
        skipReason: "not_a_claim",
        matches: [verdict],
      }),
      checked("n1", 6, "no hit", { matches: [] }),
    ]);

    expect(entries).toEqual([]);
  });

  test("preserves the incoming statement order across entries", () => {
    const claim = (claim: string): SegmentMatch => ({
      kind: "claim",
      claim,
      verdict: "unclear",
      sources: [],
      similarity: 0.5,
    });
    const entries = deriveFactChecks([
      checked("s1", 0, "first", { matches: [claim("a")] }),
      checked("s2", 10, "second", { matches: [claim("b"), claim("c")] }),
    ]);

    expect(entries.map((e) => e.statementId)).toEqual(["s1", "s2", "s2"]);
  });
});

import { describe, expect, test } from "vitest";
import type { SegmentMatch, Verdict } from "@/lib/fact-check/api";
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
      evidence: 1,
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
      evidence: 0,
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

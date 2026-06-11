import { describe, expect, test } from "vitest";

import type { DebugResultsFrame } from "./frames";
import { applyResults, initialDebugSearchView } from "./search-state";

function frame(seq: number, titles: string[], error?: string): DebugResultsFrame {
  return {
    type: "results",
    seq,
    hits: titles.map((title) => ({
      title,
      url: `https://example.test/${title}`,
      snippet: title,
      similarity: 0.5,
    })),
    ...(error ? { error } : {}),
  };
}

describe("applyResults", () => {
  test("applies a newer frame and records its seq", () => {
    const next = applyResults(initialDebugSearchView, frame(1, ["a"]));
    expect(next.renderedSeq).toBe(1);
    expect(next.hits.map((h) => h.title)).toEqual(["a"]);
    expect(next.error).toBeNull();
  });

  test("ignores a frame older than the last rendered one", () => {
    const after2 = applyResults(initialDebugSearchView, frame(2, ["fresh"]));
    const stale = applyResults(after2, frame(1, ["old"]));
    expect(stale).toBe(after2);
    expect(stale.hits.map((h) => h.title)).toEqual(["fresh"]);
  });

  test("an equal seq replaces (idempotent re-application)", () => {
    const after3 = applyResults(initialDebugSearchView, frame(3, ["a"]));
    const again = applyResults(after3, frame(3, ["b"]));
    expect(again.hits.map((h) => h.title)).toEqual(["b"]);
  });

  test("a newer empty frame clears the list", () => {
    const after1 = applyResults(initialDebugSearchView, frame(1, ["a", "b"]));
    const cleared = applyResults(after1, frame(2, []));
    expect(cleared.hits).toEqual([]);
    expect(cleared.renderedSeq).toBe(2);
  });

  test("surfaces an error from a newer frame", () => {
    const errored = applyResults(initialDebugSearchView, frame(1, [], "search failed"));
    expect(errored.error).toBe("search failed");
  });
});

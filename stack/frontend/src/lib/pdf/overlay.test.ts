import { describe, expect, test } from "vitest";
import type { AnchorRange } from "./anchor";
import {
  mergeLineRects,
  resolveHighlightBoxes,
  type AnchoredSentence,
  type Rect,
} from "./overlay";

describe("mergeLineRects", () => {
  test("merges same-line fragments into one box per line", () => {
    // Two fragments on line one (same top), one fragment on line two.
    const merged = mergeLineRects([
      { left: 10, top: 0, width: 30, height: 12 },
      { left: 40, top: 0, width: 20, height: 12 },
      { left: 10, top: 16, width: 25, height: 12 },
    ]);
    expect(merged).toEqual([
      { left: 10, top: 0, width: 50, height: 12 },
      { left: 10, top: 16, width: 25, height: 12 },
    ]);
  });

  test("treats sub-pixel vertical jitter on one line as the same line", () => {
    const merged = mergeLineRects([
      { left: 10, top: 0, width: 30, height: 12 },
      { left: 41, top: 0.4, width: 20, height: 12 },
    ]);
    expect(merged).toHaveLength(1);
    expect(merged[0]).toEqual({ left: 10, top: 0, width: 51, height: 12.4 });
  });

  test("drops zero-area rects", () => {
    const merged = mergeLineRects([
      { left: 0, top: 0, width: 0, height: 12 },
      { left: 0, top: 0, width: 10, height: 0 },
    ]);
    expect(merged).toEqual([]);
  });
});

// A fake text layer: three items, and a measurement seam that returns a rect per
// item in the range so tests exercise anchoring, measurement, and merging end to
// end without a browser layout engine.
const ITEMS = ["Le chômage a", "nettement baissé.", "La dette monte."];

function fakeMeasure(range: AnchorRange): Rect[] {
  const rects: Rect[] = [];
  for (let item = range.startItem; item <= range.endItem; item += 1) {
    rects.push({ left: 0, top: item * 20, width: 100, height: 12 });
  }
  return rects;
}

function credible(over: Partial<AnchoredSentence> = {}): AnchoredSentence {
  return {
    seq: 0,
    text: "Le chômage a nettement baissé.",
    occurrence: 1,
    verdict: "credible",
    snippet: "Le chômage a baissé",
    ...over,
  };
}

describe("resolveHighlightBoxes", () => {
  test("anchors, measures, and merges a multi-item sentence", () => {
    const boxes = resolveHighlightBoxes({
      items: ITEMS,
      sentences: [credible()],
      measure: fakeMeasure,
    });
    expect(boxes).toHaveLength(1);
    expect(boxes[0].seq).toBe(0);
    expect(boxes[0].verdict).toBe("credible");
    // The sentence spans items 0 and 1: two lines, so two boxes.
    expect(boxes[0].rects).toEqual([
      { left: 0, top: 0, width: 100, height: 12 },
      { left: 0, top: 20, width: 100, height: 12 },
    ]);
  });

  test("skips a sentence that cannot be anchored, keeping the rest", () => {
    const boxes = resolveHighlightBoxes({
      items: ITEMS,
      sentences: [
        credible({ seq: 1, text: "Phrase absente du document.", snippet: "x" }),
        credible({ seq: 2, verdict: "disputed" }),
      ],
      measure: fakeMeasure,
    });
    expect(boxes.map((box) => box.seq)).toEqual([2]);
    expect(boxes[0].verdict).toBe("disputed");
  });

  test("skips a sentence whose measurement yields no rects", () => {
    const boxes = resolveHighlightBoxes({
      items: ITEMS,
      sentences: [credible()],
      measure: () => [],
    });
    expect(boxes).toEqual([]);
  });

  test("passes the measured range that matches the anchored items", () => {
    const seen: AnchorRange[] = [];
    resolveHighlightBoxes({
      items: ITEMS,
      sentences: [credible({ text: "La dette monte.", occurrence: 1 })],
      measure: (range) => {
        seen.push(range);
        return fakeMeasure(range);
      },
    });
    expect(seen).toHaveLength(1);
    expect(seen[0].startItem).toBe(2);
    expect(seen[0].endItem).toBe(2);
  });
});

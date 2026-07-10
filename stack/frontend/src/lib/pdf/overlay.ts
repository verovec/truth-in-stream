import { anchorSentence, buildPageAnchorIndex, type AnchorRange } from "./anchor";

// Rect is a highlight box in page-container coordinates (CSS pixels from the
// page's top-left) - the shape the overlay renders and the measurement seam
// returns.
export type Rect = {
  left: number;
  top: number;
  width: number;
  height: number;
};

// HighlightVerdict is the subset of verdicts that light up inside the PDF: only
// what clearly holds or clearly does not. Unverifiable and skipped sentences stay
// panel-only by design, so they never reach the overlay.
export type HighlightVerdict = "credible" | "disputed";

// AnchoredSentence is one credible/disputed sentence to draw on a page: the panel
// sequence number tying it to the side panel, the verdict that colors it, the
// claim snippet the tooltip shows, and the identity (text + occurrence) used to
// anchor it.
export type AnchoredSentence = {
  seq: number;
  text: string;
  occurrence: number;
  verdict: HighlightVerdict;
  snippet: string;
};

// HighlightBox is a resolved, drawable highlight: one sentence's merged per-line
// boxes plus the metadata the interactive overlay needs.
export type HighlightBox = {
  seq: number;
  verdict: HighlightVerdict;
  snippet: string;
  rects: Rect[];
};

// MeasureRange turns an item range into page-relative rects. In the browser this
// wraps a DOM Range over the text-layer nodes and reads getClientRects; tests
// inject a deterministic stand-in.
export type MeasureRange = (range: AnchorRange) => Rect[];

// resolveHighlightBoxes is the pure heart of the overlay: it builds the page
// anchor index once, anchors each sentence, measures its rects through the seam,
// and merges contiguous same-line rects into one box per visual line. A sentence
// that fails to anchor or measures no rects is dropped from the drawing but never
// throws - its verdict still lives in the side panel.
export function resolveHighlightBoxes(params: {
  items: string[];
  sentences: readonly AnchoredSentence[];
  measure: MeasureRange;
}): HighlightBox[] {
  const index = buildPageAnchorIndex(params.items);
  const boxes: HighlightBox[] = [];
  for (const sentence of params.sentences) {
    const range = anchorSentence(index, sentence.text, sentence.occurrence);
    if (range === null) {
      continue;
    }
    const rects = mergeLineRects(params.measure(range));
    if (rects.length === 0) {
      continue;
    }
    boxes.push({
      seq: sentence.seq,
      verdict: sentence.verdict,
      snippet: sentence.snippet,
      rects,
    });
  }
  return boxes;
}

// mergeLineRects collapses the raw client rects of one sentence - one per span
// fragment - into a single box per visual line. Rects whose vertical centers sit
// within half the smaller line height of each other belong to the same line; each
// line becomes the bounding box of its fragments, so a sentence wrapping three
// lines draws three clean boxes instead of a ragged pile of per-span slivers.
export function mergeLineRects(rects: readonly Rect[]): Rect[] {
  const sorted = rects
    .filter((rect) => rect.width > 0 && rect.height > 0)
    .sort((a, b) => a.top - b.top || a.left - b.left);

  const lines: Rect[][] = [];
  for (const rect of sorted) {
    const line = lines[lines.length - 1];
    if (line && sameLine(line[line.length - 1], rect)) {
      line.push(rect);
    } else {
      lines.push([rect]);
    }
  }
  return lines.map(boundingBox);
}

function verticalCenter(rect: Rect): number {
  return rect.top + rect.height / 2;
}

function sameLine(reference: Rect, rect: Rect): boolean {
  const tolerance = Math.min(reference.height, rect.height) * 0.5;
  return Math.abs(verticalCenter(reference) - verticalCenter(rect)) <= tolerance;
}

function boundingBox(rects: Rect[]): Rect {
  let left = Infinity;
  let top = Infinity;
  let right = -Infinity;
  let bottom = -Infinity;
  for (const rect of rects) {
    left = Math.min(left, rect.left);
    top = Math.min(top, rect.top);
    right = Math.max(right, rect.left + rect.width);
    bottom = Math.max(bottom, rect.top + rect.height);
  }
  return { left, top, width: right - left, height: bottom - top };
}

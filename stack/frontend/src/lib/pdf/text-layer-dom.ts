import "client-only";
import type { AnchorRange } from "./anchor";
import type { MeasureRange, Rect } from "./overlay";

// react-pdf renders the text layer into this container, one span per pdf.js text
// item in document order. Reading the items straight from the DOM this way keeps
// the strings the overlay anchors against identical to the spans it measures, so
// item indices always line up with what is on screen.
const TEXT_LAYER_SELECTOR = ".react-pdf__Page__textContent";

// readItemNodes returns the text nodes of the page's text-item spans in document
// order. An item span is one whose first child is the item's text node; react-pdf
// wraps structured PDFs in marked-content spans (whose children are other spans)
// and appends an endOfContent marker, both of which are skipped so the list lines
// up with the pdf.js items extraction saw.
function readItemNodes(pageEl: HTMLElement): Text[] {
  const layer = pageEl.querySelector(TEXT_LAYER_SELECTOR);
  if (!layer) {
    return [];
  }
  const nodes: Text[] = [];
  layer.querySelectorAll("span").forEach((span) => {
    if (span.classList.contains("endOfContent")) {
      return;
    }
    const first = span.firstChild;
    if (first && first.nodeType === Node.TEXT_NODE) {
      nodes.push(first as Text);
    }
  });
  return nodes;
}

// buildPageMeasurement reads a rendered page's text items and returns them with a
// measurement function that resolves an item range to page-relative rects. The
// measurer builds a DOM Range across the item text nodes and reads its client
// rects (one per wrapped line), translated into the page container's coordinates
// so the overlay can position boxes with plain absolute offsets. Any out-of-range
// or invalid range measures to nothing rather than throwing, so a stray anchor can
// never break the page.
export function buildPageMeasurement(pageEl: HTMLElement): {
  items: string[];
  measure: MeasureRange;
} {
  const nodes = readItemNodes(pageEl);
  const items = nodes.map((node) => node.textContent ?? "");
  const origin = pageEl.getBoundingClientRect();

  const measure: MeasureRange = (range: AnchorRange): Rect[] => {
    const startNode = nodes[range.startItem];
    const endNode = nodes[range.endItem];
    if (!startNode || !endNode) {
      return [];
    }
    const domRange = document.createRange();
    try {
      domRange.setStart(startNode, clampOffset(range.startOffset, startNode));
      domRange.setEnd(endNode, clampOffset(range.endOffset, endNode));
    } catch {
      return [];
    }
    const rects: Rect[] = [];
    const measured = domRange.getClientRects();
    for (let i = 0; i < measured.length; i += 1) {
      const rect = measured[i];
      rects.push({
        left: rect.left - origin.left,
        top: rect.top - origin.top,
        width: rect.width,
        height: rect.height,
      });
    }
    return rects;
  };

  return { items, measure };
}

function clampOffset(offset: number, node: Text): number {
  const max = node.textContent?.length ?? 0;
  if (offset < 0) {
    return 0;
  }
  return offset > max ? max : offset;
}

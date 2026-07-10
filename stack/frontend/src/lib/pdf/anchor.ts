import { normalizeWithMap } from "./normalize";

// AnchorRange addresses a matched sentence inside a page's rendered text layer as
// a half-open span over text items: from character `startOffset` of item
// `startItem` up to (but not including) character `endOffset` of item `endItem`.
// Offsets index the raw item strings - the text nodes react-pdf renders - so the
// overlay can build a DOM Range directly and read its client rects.
export type AnchorRange = {
  startItem: number;
  startOffset: number;
  endItem: number;
  endOffset: number;
};

// PageAnchorIndex is the once-per-page concatenation of the text items with the
// provenance needed to resolve any substring match back to items. `text` is
// exactly the page text extraction stored (items joined with a single space, then
// normalized), so a stored sentence substring-matches it; `itemAt`/`offsetAt`
// carry, per normalized character, the source item and raw offset within it.
export type PageAnchorIndex = {
  text: string;
  itemAt: number[];
  offsetAt: number[];
};

// ITEM_SEPARATOR is the single space extraction joins text items with
// (readPdfPages does `items.join(" ")`); anchoring must join identically or the
// normalized page text would differ and sentences would not match.
const ITEM_SEPARATOR = " ";

// buildPageAnchorIndex concatenates a page's text items exactly as extraction did,
// normalizes with the shared mapped normalizer, and threads every normalized
// character back to its source item and offset. Characters coming from a
// separator inserted between items carry item -1, so a match resolving onto one
// is treated as a miss rather than pointing at a phantom item.
export function buildPageAnchorIndex(items: string[]): PageAnchorIndex {
  let joined = "";
  const ownerItem: number[] = [];
  const ownerOffset: number[] = [];
  items.forEach((item, itemIndex) => {
    if (itemIndex > 0) {
      joined += ITEM_SEPARATOR;
      ownerItem.push(-1);
      ownerOffset.push(-1);
    }
    for (let offset = 0; offset < item.length; offset += 1) {
      joined += item[offset];
      ownerItem.push(itemIndex);
      ownerOffset.push(offset);
    }
  });

  const { text, sourceIndex } = normalizeWithMap(joined);
  const itemAt: number[] = new Array(text.length);
  const offsetAt: number[] = new Array(text.length);
  for (let i = 0; i < text.length; i += 1) {
    const source = sourceIndex[i];
    itemAt[i] = ownerItem[source];
    offsetAt[i] = ownerOffset[source];
  }
  return { text, itemAt, offsetAt };
}

// anchorSentence locates the `occurrence`-th (1-based) copy of `sentence` in the
// page text and resolves it to an item range. It returns null - a graceful miss
// that leaves the sentence in the side panel and never breaks the page - when the
// sentence is empty, absent, the requested occurrence does not exist, or the match
// starts or ends on a synthetic separator rather than a real item.
export function anchorSentence(
  index: PageAnchorIndex,
  sentence: string,
  occurrence: number,
): AnchorRange | null {
  if (sentence === "") {
    return null;
  }
  const start = nthIndexOf(index.text, sentence, occurrence);
  if (start < 0) {
    return null;
  }
  const end = start + sentence.length - 1;
  const startItem = index.itemAt[start];
  const endItem = index.itemAt[end];
  if (startItem < 0 || endItem < 0) {
    return null;
  }
  return {
    startItem,
    startOffset: index.offsetAt[start],
    endItem,
    endOffset: index.offsetAt[end] + 1,
  };
}

// nthIndexOf returns the start index of the `occurrence`-th (1-based) non-
// overlapping substring match of `needle` in `haystack`, or -1. After each hit the
// search resumes past the whole match. This is the design's substring anchoring:
// it matches the extractor's per-page `occurrence` for distinct sentences and for
// verbatim full-sentence repeats (the realistic duplicate cases); the only skew is
// the rare case where a sentence's text also appears embedded inside a longer one,
// where a mis-anchor stays graceful (the verdict still shows in the side panel).
function nthIndexOf(
  haystack: string,
  needle: string,
  occurrence: number,
): number {
  if (occurrence < 1) {
    return -1;
  }
  let from = 0;
  for (let count = 1; count <= occurrence; count += 1) {
    const at = haystack.indexOf(needle, from);
    if (at < 0) {
      return -1;
    }
    if (count === occurrence) {
      return at;
    }
    from = at + needle.length;
  }
  return -1;
}

// Highlighting the exact transcript words each atomic claim was checked
// against. The backend anchors every claim's verbatim quote onto its unit's
// segments as [start, end) code-point spans (segment_id = the subtitle id a
// statement row keys on); this module joins those spans with the claim's live
// lifecycle and slices a statement's text into renderable parts. Pure functions
// so both live drivers (video and TV channel) and the tests share one
// implementation.
import type { ClaimsState } from "./claims";
import type { ClaimStatus, ClaimVerdict } from "./frames";

// ClaimHighlight is one claim's anchored range inside one statement's text,
// joined with the claim's current lifecycle so the mark can tint by verdict as
// results arrive. Offsets count Unicode code points (the backend counts runes).
// quote is the claim's verbatim source words; the renderer checks the sliced
// text against it, so an offset that no longer matches the words it was
// computed for (a segment id reused across backend sessions on the channel
// path, a stale snapshot) renders plain instead of marking arbitrary words.
export type ClaimHighlight = {
  unitId: string;
  claimId: string;
  start: number;
  end: number;
  status: ClaimStatus;
  verdict?: ClaimVerdict;
  quote?: string;
};

// NO_HIGHLIGHTS is the shared stable empty result for a segment with no
// anchored claims, so per-row lookups return one identity and never re-render
// a memoized list with a fresh empty array.
export const NO_HIGHLIGHTS: readonly ClaimHighlight[] = [];

// claimHighlights indexes every anchored claim span by the segment id it points
// at, so a statement row looks up its own highlights in one map read. Claims
// without spans (an unanchorable quote, a legacy snapshot) simply contribute
// nothing. Ordering is segmentTextParts' concern - it sorts what it renders -
// so the index preserves plain claim order.
export function claimHighlights(
  state: ClaimsState,
): ReadonlyMap<string, readonly ClaimHighlight[]> {
  const bySegment = new Map<string, ClaimHighlight[]>();
  for (const [unitId, claims] of state.byUnit) {
    for (const claim of claims.values()) {
      for (const span of claim.spans ?? []) {
        const list = bySegment.get(span.segmentId) ?? [];
        list.push({
          unitId,
          claimId: claim.claimId,
          start: span.start,
          end: span.end,
          status: claim.status,
          verdict: claim.verdict,
          quote: claim.quote,
        });
        bySegment.set(span.segmentId, list);
      }
    }
  }
  return bySegment;
}

// TextPart is one slice of a statement's text: highlighted (with the claim it
// belongs to) or plain. Concatenating every part's text reproduces the
// statement exactly.
export type TextPart = {
  text: string;
  highlight?: ClaimHighlight;
};

// segmentTextParts slices text into plain and highlighted parts from the given
// (start-sorted or not) highlights. Offsets are code points, so the text is
// walked by code point and each part is sliced on code-point boundaries -
// never through a surrogate pair. Ranges are clamped to the text; an empty or
// inverted range is dropped; an overlapping range is trimmed to start where the
// previous highlight ended (first-come wins) and dropped when nothing remains.
// A range whose sliced words are not part of the claim's quote is dropped too:
// the offsets were computed for different text (a reused segment id across
// backend sessions, a stale snapshot), and no mark beats marking wrong words.
export function segmentTextParts(
  text: string,
  highlights: readonly ClaimHighlight[],
): TextPart[] {
  if (highlights.length === 0) {
    return [{ text }];
  }
  const codePoints = Array.from(text);
  const parts: TextPart[] = [];
  let cursor = 0;
  const ordered = [...highlights].sort(
    (a, b) => a.start - b.start || a.end - b.end,
  );
  for (const highlight of ordered) {
    const start = Math.max(highlight.start, cursor);
    const end = Math.min(highlight.end, codePoints.length);
    if (start >= end) {
      continue;
    }
    const slice = codePoints.slice(start, end).join("");
    if (!sliceMatchesQuote(slice, highlight.quote)) {
      continue;
    }
    if (start > cursor) {
      parts.push({ text: codePoints.slice(cursor, start).join("") });
    }
    parts.push({ text: slice, highlight });
    cursor = end;
  }
  if (cursor < codePoints.length) {
    parts.push({ text: codePoints.slice(cursor).join("") });
  }
  return parts;
}

// sliceMatchesQuote reports whether the sliced statement words belong to the
// claim's verbatim quote, under the same tolerance the backend anchored with
// (case fold, whitespace collapse). A slice is a fragment of the quote when the
// span crossed a segment boundary, so substring - not equality - is the test.
// A highlight without a quote cannot be checked and is trusted as-is.
function sliceMatchesQuote(slice: string, quote: string | undefined): boolean {
  if (quote === undefined) {
    return true;
  }
  const normalizedSlice = normalizeForMatch(slice);
  if (normalizedSlice.length === 0) {
    return false;
  }
  return normalizeForMatch(quote).includes(normalizedSlice);
}

// normalizeForMatch mirrors the backend quote locator's tolerance: lowercase
// and collapse every whitespace run to one space.
function normalizeForMatch(text: string): string {
  return text.toLowerCase().replace(/\s+/g, " ").trim();
}

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
export type ClaimHighlight = {
  unitId: string;
  claimId: string;
  start: number;
  end: number;
  status: ClaimStatus;
  verdict?: ClaimVerdict;
};

// claimHighlights indexes every anchored claim span by the segment id it points
// at, so a statement row looks up its own highlights in one map read. Claims
// without spans (an unanchorable quote, a legacy snapshot) simply contribute
// nothing. Within a segment, highlights sort by start so the renderer can walk
// them left to right.
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
        });
        bySegment.set(span.segmentId, list);
      }
    }
  }
  for (const list of bySegment.values()) {
    list.sort((a, b) => a.start - b.start || a.end - b.end);
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
    if (start > cursor) {
      parts.push({ text: codePoints.slice(cursor, start).join("") });
    }
    parts.push({
      text: codePoints.slice(start, end).join(""),
      highlight,
    });
    cursor = end;
  }
  if (cursor < codePoints.length) {
    parts.push({ text: codePoints.slice(cursor).join("") });
  }
  return parts;
}

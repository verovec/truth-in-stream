// Pure stale-response logic for the developer wiki-search bar. Responses arrive
// out of order relative to keystrokes (each query embeds and searches at its own
// latency), so the view tracks the highest sequence number it has rendered and
// ignores any frame older than that. Keeping this in one pure function lets the
// hook stay thin and the rule be tested without a socket.
import type { DebugHit, DebugResultsFrame } from "./frames";

export type DebugSearchView = {
  hits: DebugHit[];
  error: string | null;
  // renderedSeq is the sequence number of the most recent frame applied; a frame
  // with a lower seq is a superseded response and is discarded.
  renderedSeq: number;
};

export const initialDebugSearchView: DebugSearchView = {
  hits: [],
  error: null,
  renderedSeq: 0,
};

/**
 * Folds a results frame into the view, dropping it when its sequence number is
 * older than the last rendered one. An equal or newer seq replaces the hits and
 * error wholesale, so a later query's empty result correctly clears the list.
 */
export function applyResults(
  view: DebugSearchView,
  frame: DebugResultsFrame,
): DebugSearchView {
  if (frame.seq < view.renderedSeq) {
    return view;
  }
  return {
    hits: frame.hits,
    error: frame.error ?? null,
    renderedSeq: frame.seq,
  };
}

// A tiny external store that publishes the live-analysis session's output to any
// component in the watch screen, mirroring the playback store pattern. One
// driver owns the live hook and publishes here; the summary strip and the
// fact-check panel subscribe with selectors and read one consistent snapshot,
// so they share a single WebSocket and can never disagree.
import type { LiveAnalysis } from "@/hooks/use-live-analysis";

// LiveAnalysisSnapshot is the live session's output, or null when no video is
// being analysed (idle, or between switching videos).
export type LiveAnalysisSnapshot = LiveAnalysis | null;

export type LiveAnalysisStore = {
  getSnapshot: () => LiveAnalysisSnapshot;
  subscribe: (listener: () => void) => () => void;
  publish: (snapshot: LiveAnalysisSnapshot) => void;
};

export function createLiveAnalysisStore(): LiveAnalysisStore {
  let snapshot: LiveAnalysisSnapshot = null;
  const listeners = new Set<() => void>();

  return {
    getSnapshot: () => snapshot,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    publish: (next) => {
      if (Object.is(snapshot, next)) {
        return;
      }
      snapshot = next;
      for (const listener of listeners) {
        listener();
      }
    },
  };
}

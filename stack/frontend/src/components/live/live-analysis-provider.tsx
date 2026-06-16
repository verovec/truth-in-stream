"use client";

import {
  createContext,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useLiveAnalysis } from "@/hooks/use-live-analysis";
import {
  createLiveAnalysisStore,
  type LiveAnalysisSnapshot,
  type LiveAnalysisStore,
} from "@/lib/live/live-analysis-store";

const LiveAnalysisContext = createContext<LiveAnalysisStore | null>(null);

// LiveAnalysisProvider hosts a single live-analysis session and shares it with
// the whole watch screen. The summary strip at the top of the page and the
// fact-check panel inside the grid both read it through useLiveAnalysisSelector,
// so they sit in different places in the tree yet share one source and one
// WebSocket. The store lives in stable state and the page is passed through as
// children, so a live update re-renders only the selector subscribers, never
// the library or the player.
export function LiveAnalysisProvider({
  videoId,
  children,
}: {
  videoId: string | null;
  children: ReactNode;
}) {
  const [store] = useState(createLiveAnalysisStore);
  return (
    <LiveAnalysisContext.Provider value={store}>
      {videoId !== null && (
        <LiveAnalysisDriver key={videoId} videoId={videoId} store={store} />
      )}
      {children}
    </LiveAnalysisContext.Provider>
  );
}

// LiveAnalysisDriver owns the one live-analysis hook for the active video and
// publishes its output into the shared store. It is keyed by video id by the
// provider, so switching videos tears the session down and starts a fresh one.
// It renders nothing, so it never wraps the page: publishing a frame notifies
// only the store's subscribers.
function LiveAnalysisDriver({
  videoId,
  store,
}: {
  videoId: string;
  store: LiveAnalysisStore;
}) {
  const { statements, caption, status, summary, claimsFor } =
    useLiveAnalysis(videoId);
  useEffect(() => {
    store.publish({ statements, caption, status, summary, claimsFor });
  }, [store, statements, caption, status, summary, claimsFor]);
  // When this session ends (the video is deselected or switched), clear the
  // snapshot so the strip falls back to idle instead of showing stale counts.
  useEffect(() => () => store.publish(null), [store]);
  return null;
}

/**
 * Reads a slice of the shared live-analysis snapshot, re-rendering only when
 * that slice changes. Select a stable value (a memoized object or a primitive):
 * the summary and the ordered statements keep a stable identity across
 * interim-only updates, so a strip that selects the summary does not re-render
 * on every spoken word. The snapshot is null when no video is being analysed.
 */
export function useLiveAnalysisSelector<T>(
  selector: (snapshot: LiveAnalysisSnapshot) => T,
): T {
  const store = useContext(LiveAnalysisContext);
  if (!store) {
    throw new Error(
      "useLiveAnalysisSelector requires a LiveAnalysisProvider ancestor",
    );
  }
  const getSelection = () => selector(store.getSnapshot());
  return useSyncExternalStore(store.subscribe, getSelection, getSelection);
}

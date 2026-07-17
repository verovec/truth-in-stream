"use client";

import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useLiveAnalysis } from "@/hooks/use-live-analysis";
import type { LiveFrame } from "@/lib/live/frames";
import { hydrateAnalysis } from "@/lib/live/hydrate";
import {
  createLiveAnalysisStore,
  type LiveAnalysisSnapshot,
  type LiveAnalysisStore,
} from "@/lib/live/live-analysis-store";
import type { AudioCaptureFactory, LiveSocketFactory } from "@/lib/live/ports";

// Exported so an alternate driver (the TV channel viewer, ChannelLiveProvider)
// can publish into the same context the four live components read through
// useLiveAnalysisSelector, letting them render a channel stream unchanged. The
// video path continues to provide it through LiveAnalysisProvider below.
export const LiveAnalysisContext = createContext<LiveAnalysisStore | null>(null);

// LiveAnalysisProvider hosts a single analysis session and shares it with the
// whole watch screen. The summary strip at the top of the page and the
// fact-check panel inside the grid both read it through useLiveAnalysisSelector,
// so they sit in different places in the tree yet share one source and one
// WebSocket. The store lives in stable state and the page is passed through as
// children, so a live update re-renders only the selector subscribers, never
// the library or the player.
//
// analysed switches the active video onto the stored pre-analysis path: the
// live driver (socket + audio capture) never mounts for it, and once
// analysedFrames arrive they hydrate the store through the same reducers the
// socket feeds. The two flags are separate because hydration is asynchronous:
// while the stored result is still loading (frames null) an analysed video must
// already suppress the live session, or a quick play would open the socket the
// stored analysis makes redundant. socketFactory and captureFactory are
// injection seams for tests, forwarded to the live driver's hook.
export function LiveAnalysisProvider({
  videoId,
  analysed = false,
  analysedFrames = null,
  socketFactory,
  captureFactory,
  children,
}: {
  videoId: string | null;
  analysed?: boolean;
  analysedFrames?: LiveFrame[] | null;
  socketFactory?: LiveSocketFactory;
  captureFactory?: AudioCaptureFactory;
  children: ReactNode;
}) {
  const [store] = useState(createLiveAnalysisStore);
  return (
    <LiveAnalysisContext.Provider value={store}>
      {videoId !== null &&
        (analysed ? (
          analysedFrames !== null && (
            <AnalysedAnalysisDriver
              key={videoId}
              frames={analysedFrames}
              store={store}
            />
          )
        ) : (
          <LiveAnalysisDriver
            key={videoId}
            videoId={videoId}
            store={store}
            socketFactory={socketFactory}
            captureFactory={captureFactory}
          />
        ))}
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
  socketFactory,
  captureFactory,
}: {
  videoId: string;
  store: LiveAnalysisStore;
  socketFactory?: LiveSocketFactory;
  captureFactory?: AudioCaptureFactory;
}) {
  const {
    statements,
    caption,
    status,
    summary,
    claimsFor,
    highlightsFor,
    speakers,
  } = useLiveAnalysis(videoId, {
    ...(socketFactory ? { socketFactory } : {}),
    ...(captureFactory ? { captureFactory } : {}),
  });
  useEffect(() => {
    store.publish({
      statements,
      caption,
      status,
      summary,
      claimsFor,
      highlightsFor,
      speakers,
    });
  }, [
    store,
    statements,
    caption,
    status,
    summary,
    claimsFor,
    highlightsFor,
    speakers,
  ]);
  // When this session ends (the video is deselected or switched), clear the
  // snapshot so the strip falls back to idle instead of showing stale counts.
  useEffect(() => () => store.publish(null), [store]);
  return null;
}

// AnalysedAnalysisDriver publishes a stored pre-analysis into the shared store:
// the frames run through the same reducers the live socket feeds, with their
// absolute video-time timestamps intact (base time 0), so the transcript,
// claims, speakers, and summary render exactly as a completed live session
// would - present in full before playback starts, with no socket and no audio
// capture. Keyed by video id by the provider, so switching videos swaps the
// snapshot cleanly.
function AnalysedAnalysisDriver({
  frames,
  store,
}: {
  frames: LiveFrame[];
  store: LiveAnalysisStore;
}) {
  const snapshot = useMemo(() => hydrateAnalysis(frames), [frames]);
  useEffect(() => {
    store.publish(snapshot);
  }, [store, snapshot]);
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

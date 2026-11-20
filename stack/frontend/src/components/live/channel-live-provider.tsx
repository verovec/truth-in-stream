"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useChannelLive } from "@/hooks/use-channel-live";
import {
  createLiveAnalysisStore,
  type LiveAnalysisStore,
} from "@/lib/live/live-analysis-store";
import type { LiveSocketFactory } from "@/lib/live/ports";
import { LiveAnalysisContext } from "./live-analysis-provider";

// ChannelLiveProvider is the TV-channel counterpart of LiveAnalysisProvider: it
// hosts one read-only viewer session for a channel and publishes it into the
// same store the four live components read through useLiveAnalysisSelector, so
// the summary strip, speaker credibility, and fact-check panel render a channel
// stream with no changes. The store lives in stable state and the view is passed
// as children, so a live frame re-renders only the selector subscribers, never
// the embed or the recordings strip. socketFactory is an injection seam for
// tests. When channelId is null (no channel selected) no session is driven and
// subscribers read the idle snapshot.
export function ChannelLiveProvider({
  channelId,
  socketFactory,
  children,
}: {
  channelId: string | null;
  socketFactory?: LiveSocketFactory;
  children: ReactNode;
}) {
  const [store] = useState(createLiveAnalysisStore);
  return (
    <LiveAnalysisContext.Provider value={store}>
      {channelId !== null && (
        <ChannelLiveDriver
          key={channelId}
          channelId={channelId}
          store={store}
          socketFactory={socketFactory}
        />
      )}
      {children}
    </LiveAnalysisContext.Provider>
  );
}

// ChannelLiveDriver owns the one viewer hook for the active channel and
// publishes its output into the shared store. It is keyed by channel id by the
// provider, so switching channels tears the session down and starts a fresh one.
// It renders nothing, so publishing a frame notifies only the store's
// subscribers.
function ChannelLiveDriver({
  channelId,
  store,
  socketFactory,
}: {
  channelId: string;
  store: LiveAnalysisStore;
  socketFactory?: LiveSocketFactory;
}) {
  const {
    statements,
    caption,
    status,
    summary,
    claimsFor,
    highlightsFor,
    speakers,
  } = useChannelLive(channelId, socketFactory ? { socketFactory } : {});
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
  useEffect(() => () => store.publish(null), [store]);
  return null;
}

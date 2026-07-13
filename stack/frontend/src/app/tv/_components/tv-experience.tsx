"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import type { Role } from "@/lib/auth/token";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveSocketFactory } from "@/lib/live/ports";
import { type Channel, listChannels, type Recording } from "@/lib/tv/api";
import type { PlayableVideo } from "@/lib/video/api";
import { ChannelGrid } from "./channel-grid";
import { ChannelView } from "./channel-view";

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

// TvExperience owns the /tv surface: it loads the channel registry, shows the
// role-filtered grid, and opens a selected channel in the channel view with a
// way back to the grid. It is a pure consumption surface - no management
// controls (those are the backoffice). The channels are fetched on the client
// like the rest of this app's data so the fetch rides the same-origin proxy that
// makes the backend session cookie first-party. The loadChannels/socketFactory/
// loadRecordings/resolveRecording seams are for tests.
export function TvExperience({
  role = "guest",
  loadChannels = listChannels,
  socketFactory,
  loadRecordings,
  resolveRecording,
}: {
  role?: Role;
  loadChannels?: (signal?: AbortSignal) => Promise<Channel[]>;
  socketFactory?: LiveSocketFactory;
  loadRecordings?: (
    channelId: string,
    signal?: AbortSignal,
  ) => Promise<Recording[]>;
  resolveRecording?: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<PlayableVideo>;
}) {
  const { t } = useAppI18n();
  const [channels, setChannels] = useState<Channel[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const loadRef = useRef(loadChannels);
  useEffect(() => {
    loadRef.current = loadChannels;
  });

  useEffect(() => {
    const controller = new AbortController();
    loadRef
      .current(controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setChannels(loaded);
        setListState({ status: "loaded" });
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setListState({
          status: "error",
          message: err instanceof Error ? err.message : null,
        });
      });
    return () => controller.abort();
  }, [reloadToken]);

  const retry = useCallback(() => {
    setListState({ status: "loading" });
    setReloadToken((token) => token + 1);
  }, []);

  // The selected channel is resolved from the freshly-listed set, so a re-list
  // that flips a channel's live/enabled flags keeps the open view current. It
  // falls back to grid when the id is gone (deleted in the backoffice).
  const selectedChannel =
    channels.find((channel) => channel.id === selectedId) ?? null;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        {selectedChannel ? (
          <button
            type="button"
            onClick={() => setSelectedId(null)}
            className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
          >
            {t.tv.back}
          </button>
        ) : null}
        <h1 className="text-lg font-semibold tracking-tight text-ink dark:text-paper">
          {selectedChannel ? selectedChannel.name : t.tv.heading}
        </h1>
      </div>
      {selectedChannel ? (
        <ChannelView
          channel={selectedChannel}
          socketFactory={socketFactory}
          loadRecordings={loadRecordings}
          resolveRecording={resolveRecording}
        />
      ) : (
        <GridSection
          listState={listState}
          channels={channels}
          role={role}
          onSelect={(channel) => setSelectedId(channel.id)}
          onRetry={retry}
        />
      )}
    </div>
  );
}

function GridSection({
  listState,
  channels,
  role,
  onSelect,
  onRetry,
}: {
  listState: ListState;
  channels: Channel[];
  role: Role;
  onSelect: (channel: Channel) => void;
  onRetry: () => void;
}) {
  const { t } = useAppI18n();
  if (listState.status === "loading") {
    return (
      <p role="status" className="text-sm text-ink/60 dark:text-paper/60">
        {t.tv.loadingAria}
      </p>
    );
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {listState.message === null
            ? t.tv.loadErrorFallback
            : formatTemplate(t.tv.loadError, { message: listState.message })}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.tv.retry}
        </button>
      </div>
    );
  }
  return <ChannelGrid channels={channels} role={role} onSelect={onSelect} />;
}

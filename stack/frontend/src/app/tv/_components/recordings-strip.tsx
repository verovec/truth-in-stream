"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { VideoPlayer } from "@/components/playback/video-player";
import { formatTemplate } from "@/lib/i18n/text";
import { listChannelRecordings, type Recording } from "@/lib/tv/api";
import { getVideo, type PlayableVideo } from "@/lib/video/api";

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

type Resolved =
  | { forId: string; status: "ready"; playable: PlayableVideo }
  | { forId: string; status: "error"; message: string | null };

// formatRecordedAt renders an ISO timestamp in the active locale, falling back
// to the raw string if it cannot be parsed so a malformed value still shows.
function formatRecordedAt(recordedAt: string, locale: string): string {
  const date = new Date(recordedAt);
  if (Number.isNaN(date.getTime())) {
    return recordedAt;
  }
  return date.toLocaleString(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

// RecordingsStrip lists a channel's archived recordings, newest first (the
// backend orders them), and opens the selected one in a self-contained player.
// The list and player are consumption-only; management lives in the backoffice.
// loadRecordings and resolveRecording are injection seams for tests.
export function RecordingsStrip({
  channelId,
  loadRecordings = listChannelRecordings,
  resolveRecording = getVideo,
}: {
  channelId: string;
  loadRecordings?: (
    channelId: string,
    signal?: AbortSignal,
  ) => Promise<Recording[]>;
  resolveRecording?: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<PlayableVideo>;
}) {
  const { t, locale } = useAppI18n();
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [resolved, setResolved] = useState<Resolved | null>(null);

  const loadRef = useRef(loadRecordings);
  const resolveRef = useRef(resolveRecording);
  useEffect(() => {
    loadRef.current = loadRecordings;
    resolveRef.current = resolveRecording;
  });

  // Switching channels closes any open player during render (React's sanctioned
  // adjust-state-on-prop-change pattern) rather than in the load effect, so the
  // previous channel's recording never lingers over the new list.
  const [prevChannelId, setPrevChannelId] = useState(channelId);
  if (channelId !== prevChannelId) {
    setPrevChannelId(channelId);
    setSelectedId(null);
    setResolved(null);
  }

  useEffect(() => {
    const controller = new AbortController();
    loadRef
      .current(channelId, controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setRecordings(loaded);
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
  }, [channelId, reloadToken]);

  useEffect(() => {
    if (selectedId === null) {
      return;
    }
    const controller = new AbortController();
    resolveRef
      .current(selectedId, controller.signal)
      .then((playable) => {
        if (controller.signal.aborted) {
          return;
        }
        setResolved({ forId: selectedId, status: "ready", playable });
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setResolved({
          forId: selectedId,
          status: "error",
          message: err instanceof Error ? err.message : null,
        });
      });
    return () => controller.abort();
  }, [selectedId]);

  const retry = useCallback(() => {
    setListState({ status: "loading" });
    setReloadToken((token) => token + 1);
  }, []);

  return (
    <section aria-label={t.tv.recordings.heading} className="flex flex-col gap-3">
      <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
        {t.tv.recordings.heading}
      </h2>
      {selectedId !== null ? (
        <RecordingPlayer
          resolved={resolved}
          selectedId={selectedId}
          onClose={() => setSelectedId(null)}
        />
      ) : null}
      <RecordingsList
        listState={listState}
        recordings={recordings}
        selectedId={selectedId}
        locale={locale}
        onSelect={setSelectedId}
        onRetry={retry}
      />
    </section>
  );
}

function RecordingsList({
  listState,
  recordings,
  selectedId,
  locale,
  onSelect,
  onRetry,
}: {
  listState: ListState;
  recordings: Recording[];
  selectedId: string | null;
  locale: string;
  onSelect: (id: string) => void;
  onRetry: () => void;
}) {
  const { t } = useAppI18n();
  if (listState.status === "loading") {
    return (
      <p role="status" className="text-sm text-ink/60 dark:text-paper/60">
        {t.tv.recordings.loadingAria}
      </p>
    );
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {listState.message === null
            ? t.tv.recordings.loadErrorFallback
            : formatTemplate(t.tv.recordings.loadError, {
                message: listState.message,
              })}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.tv.recordings.retry}
        </button>
      </div>
    );
  }
  if (recordings.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.tv.recordings.empty}
      </p>
    );
  }
  return (
    <ul className="flex flex-col gap-2">
      {recordings.map((recording) => {
        const active = recording.id === selectedId;
        return (
          <li key={recording.id}>
            <button
              type="button"
              aria-current={active ? "true" : undefined}
              onClick={() => onSelect(recording.id)}
              className={`flex w-full items-center justify-between gap-3 rounded-xl border px-4 py-3 text-left transition focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60 ${
                active
                  ? "border-bleu/40 bg-bleu/10 dark:border-sky-400/40 dark:bg-sky-400/15"
                  : "border-black/10 bg-white hover:bg-black/5 dark:border-white/10 dark:bg-white/5 dark:hover:bg-white/10"
              }`}
            >
              <span className="min-w-0 truncate text-sm font-medium text-ink dark:text-paper">
                {recording.title}
              </span>
              <span className="shrink-0 text-xs text-ink/50 dark:text-paper/50">
                {formatRecordedAt(recording.recordedAt, locale)}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function RecordingPlayer({
  resolved,
  selectedId,
  onClose,
}: {
  resolved: Resolved | null;
  selectedId: string;
  onClose: () => void;
}) {
  const { t } = useAppI18n();
  const ready = resolved && resolved.forId === selectedId ? resolved : null;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-end">
        <button
          type="button"
          onClick={onClose}
          className="rounded-md border border-black/10 bg-white px-3 py-1 text-xs font-medium text-ink/70 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/70 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.tv.recordings.close}
        </button>
      </div>
      {ready === null ? (
        <div
          aria-label={t.tv.recordings.loadingPlayer}
          className="aspect-video w-full animate-pulse rounded-2xl border border-black/10 bg-black/10 dark:border-white/10 dark:bg-white/10"
        />
      ) : ready.status === "ready" ? (
        <PlaybackProvider>
          <VideoPlayer src={ready.playable.playback.url} title={ready.playable.title} />
        </PlaybackProvider>
      ) : (
        <div
          role="alert"
          className="flex aspect-video w-full flex-col items-center justify-center gap-1 rounded-2xl border border-rouge/25 bg-rouge/5 p-4 text-center text-sm text-rouge dark:border-rouge/40 dark:bg-rouge/15 dark:text-rose-300"
        >
          <p className="font-medium">{t.tv.recordings.playError}</p>
          {ready.message !== null ? (
            <p className="text-xs">{ready.message}</p>
          ) : null}
        </div>
      )}
    </div>
  );
}

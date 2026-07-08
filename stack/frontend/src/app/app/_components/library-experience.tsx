"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { LiveAnalysisProvider } from "@/components/live/live-analysis-provider";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { VideoPlayer } from "@/components/playback/video-player";
import { useVideoUploads } from "@/hooks/use-video-uploads";
import {
  getVideo,
  type LibraryVideo,
  listVideos,
  type PlayableVideo,
  submitYoutubeUrl,
} from "@/lib/video/api";
import type { PutUploader } from "@/lib/video/upload";
import { ExportControls } from "@/components/export/export-controls";
import type { Role } from "@/lib/auth/token";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { LiveFactCheckPanel } from "./live-fact-check-panel";
import { LiveSpeakerCredibility } from "./live-speaker-credibility";
import { LiveSummaryStrip } from "./live-summary-strip";
import { GALLERY_GRID_CLASS, VideoGallery } from "./video-gallery";
import { VideoUploader } from "./video-uploader";
import { YoutubeUrlForm } from "./youtube-url-form";

// DEFAULT_YOUTUBE_POLL_MS is how often the library re-checks the backend while a
// YouTube row is still downloading. The transition is server-driven with no
// client step, so it can only be observed by polling.
const DEFAULT_YOUTUBE_POLL_MS = 2500;

// mergePendingYoutube advances pending YouTube rows to whatever the freshly
// listed catalog reports, in place, leaving every other row (including in-flight
// uploads) untouched. It returns the previous array unchanged when nothing moved
// so a poll that observes no transition does not trigger a re-render.
function mergePendingYoutube(
  prev: LibraryVideo[],
  listed: LibraryVideo[],
): LibraryVideo[] {
  const byId = new Map(listed.map((video) => [video.id, video]));
  let changed = false;
  const next = prev.map((video) => {
    if (video.kind !== "youtube" || video.status !== "pending") {
      return video;
    }
    const fresh = byId.get(video.id);
    if (fresh && fresh.status !== video.status) {
      changed = true;
      return fresh;
    }
    return video;
  });
  return changed ? next : prev;
}

// ActiveState resolves the selected video into something playable. The presigned
// playback URL is short-lived and per-video, so it is fetched at selection time
// rather than embedded once.
type ActiveState =
  | { status: "idle" }
  | { status: "loading"; video: LibraryVideo }
  | { status: "ready"; playable: PlayableVideo }
  | { status: "error"; video: LibraryVideo; message: string | null };

// Resolved is the fetched outcome for one video id. Loading and idle are derived
// from it during render rather than written into state, so the fetch effect only
// ever sets state asynchronously. Error messages stay raw (or null when the
// failure carried none) and are localized at render time, so a locale switch
// re-labels an already-failed row.
type Resolved =
  | { forId: string; status: "ready"; playable: PlayableVideo }
  | { forId: string; status: "error"; message: string | null };

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

function resolveActive(
  selectedVideo: LibraryVideo | null,
  resolved: Resolved | null,
): ActiveState {
  if (!selectedVideo) {
    return { status: "idle" };
  }
  if (resolved && resolved.forId === selectedVideo.id) {
    return resolved.status === "ready"
      ? { status: "ready", playable: resolved.playable }
      : { status: "error", video: selectedVideo, message: resolved.message };
  }
  return { status: "loading", video: selectedVideo };
}

function firstReadyId(videos: LibraryVideo[]): string | null {
  return videos.find((video) => video.status === "ready")?.id ?? null;
}

// LibraryExperience owns the watch screen: it loads the video library, lets the
// operator upload and pick videos, plays the selected one, and shows fact checks
// for it. The library is fetched on the client (like the rest of this app's
// data) so it rides the same-origin proxy that makes the backend session cookie
// first-party; loadVideos and uploader are injection seams for tests.
export function LibraryExperience({
  role = "guest",
  loadVideos = listVideos,
  pollVideos = listVideos,
  uploader,
  submitYoutube = submitYoutubeUrl,
  pollIntervalMs = DEFAULT_YOUTUBE_POLL_MS,
}: {
  role?: Role;
  loadVideos?: (signal?: AbortSignal) => Promise<LibraryVideo[]>;
  pollVideos?: (signal?: AbortSignal) => Promise<LibraryVideo[]>;
  uploader?: PutUploader;
  submitYoutube?: (url: string, signal?: AbortSignal) => Promise<LibraryVideo>;
  pollIntervalMs?: number;
}) {
  const [videos, setVideos] = useState<LibraryVideo[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [resolved, setResolved] = useState<Resolved | null>(null);

  // loadVideos is an injection seam fixed at mount; reading it from a ref keeps
  // it out of the load effect's deps so an inline caller cannot trigger a loop.
  const loadVideosRef = useRef(loadVideos);
  useEffect(() => {
    loadVideosRef.current = loadVideos;
  });

  // pollVideos is the same seam for the YouTube ready-poll effect.
  const pollVideosRef = useRef(pollVideos);
  useEffect(() => {
    pollVideosRef.current = pollVideos;
  });

  // videosRef mirrors the current list so the add-by-link handler can decide
  // dedup at call time without re-creating itself on every render.
  const videosRef = useRef(videos);
  useEffect(() => {
    videosRef.current = videos;
  });

  const { jobs, startUploads, dismiss } = useVideoUploads({
    uploader,
    onUploaded: (video) => {
      setVideos((prev) => [video, ...prev]);
      setSelectedId((prev) => prev ?? video.id);
    },
  });

  // A YouTube link the backend accepted: insert the returned record and select
  // it like the upload path. Because the backend deduplicates, the record may
  // already be listed; if so, do not add a second tile, just select the existing
  // one.
  const handleYoutubeAdded = useCallback((video: LibraryVideo) => {
    const alreadyListed = videosRef.current.some((v) => v.id === video.id);
    if (!alreadyListed) {
      setVideos((prev) => [video, ...prev]);
    }
    setSelectedId((prev) => (alreadyListed ? video.id : (prev ?? video.id)));
  }, []);

  // The library loads on the client (like the rest of this app's data) and
  // reloads when reloadToken changes. The fetch is aborted on unmount/reload so
  // a stale response cannot land; every state write happens asynchronously,
  // never synchronously in the effect body.
  useEffect(() => {
    const controller = new AbortController();
    loadVideosRef
      .current(controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setVideos(loaded);
        setSelectedId((prev) => prev ?? firstReadyId(loaded));
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

  const retryLibrary = useCallback(() => {
    setListState({ status: "loading" });
    setReloadToken((token) => token + 1);
  }, []);

  const selectedVideo = videos.find((video) => video.id === selectedId) ?? null;
  const selectedStatus = selectedVideo?.status ?? null;

  // Keyed on selectedId (a stable string), not the selectedVideo object, so
  // rebuilding the videos array on an upload does not re-fetch and flicker the
  // currently-playing video. Also keyed on the selected video's status so a
  // pending YouTube row that finishes downloading re-resolves and plays the
  // moment polling flips it to ready, without a manual re-click.
  useEffect(() => {
    if (selectedId === null) {
      return;
    }
    const controller = new AbortController();
    getVideo(selectedId, controller.signal)
      .then((playable) => {
        // Guard the success path too: a status re-key re-resolves the same id, so
        // without this a late, stale request could overwrite the fresh one (both
        // carry the same forId, so resolveActive cannot tell them apart).
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
  }, [selectedId, selectedStatus]);

  // While any YouTube row is still downloading, re-list the catalog on an
  // interval and advance those rows in place; stop once none remain pending. The
  // controller aborts the in-flight request on unmount or when the last pending
  // row resolves.
  const hasPendingYoutube = videos.some(
    (video) => video.kind === "youtube" && video.status === "pending",
  );
  useEffect(() => {
    if (!hasPendingYoutube) {
      return;
    }
    const controller = new AbortController();
    const tick = () => {
      pollVideosRef
        .current(controller.signal)
        .then((listed) => {
          if (controller.signal.aborted) {
            return;
          }
          setVideos((prev) => mergePendingYoutube(prev, listed));
        })
        .catch(() => {
          // A transient poll failure is ignored; the next tick retries while a
          // row is still pending.
        });
    };
    const handle = setInterval(tick, pollIntervalMs);
    return () => {
      controller.abort();
      clearInterval(handle);
    };
  }, [hasPendingYoutube, pollIntervalMs]);

  const { t } = useAppI18n();
  const active = resolveActive(selectedVideo, resolved);
  const activeVideoId = active.status === "ready" ? active.playable.id : null;
  // The title of the video on the player, surfaced above the library so the
  // operator always sees what is playing. Resolved from the ready playback shape
  // when available, else the selected record (still loading or errored); absent
  // only when nothing is selected.
  const nowPlayingTitle =
    active.status === "ready"
      ? active.playable.title
      : active.status === "idle"
        ? null
        : active.video.title;

  return (
    <PlaybackProvider>
      {/* One live session feeds both the top-of-page summary strip and the
          in-grid fact-check panel: the provider owns the single WebSocket and
          publishes to a store the two read independently, so neither the player
          nor the library re-renders as findings stream in. */}
      <LiveAnalysisProvider videoId={activeVideoId}>
        <div className="flex flex-col gap-4">
          <LiveSummaryStrip />
          <LiveSpeakerCredibility />
          <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <div className="flex flex-col gap-4">
              <PlayerStage active={active} />
              <ExportControls role={role} videoId={activeVideoId} />
              <section className="flex flex-col gap-3">
                {nowPlayingTitle ? (
                  <h2 className="font-display text-xl font-semibold tracking-tight text-ink dark:text-paper">
                    {nowPlayingTitle}
                  </h2>
                ) : null}
                <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
                  {t.library.heading}
                </h2>
                <VideoUploader onFiles={startUploads} />
                <YoutubeUrlForm
                  onAdded={handleYoutubeAdded}
                  submit={submitYoutube}
                />
                <LibrarySection
                  listState={listState}
                  onRetry={retryLibrary}
                  videos={videos}
                  jobs={jobs}
                  selectedId={selectedId}
                  onSelect={(video) => setSelectedId(video.id)}
                  onDismiss={dismiss}
                />
              </section>
            </div>
            {active.status === "ready" ? (
              <LiveFactCheckPanel key={active.playable.id} />
            ) : (
              <FactCheckPlaceholder />
            )}
          </div>
        </div>
      </LiveAnalysisProvider>
    </PlaybackProvider>
  );
}

type LibrarySectionProps = {
  listState: ListState;
  onRetry: () => void;
  videos: LibraryVideo[];
  jobs: ReturnType<typeof useVideoUploads>["jobs"];
  selectedId: string | null;
  onSelect: (video: LibraryVideo) => void;
  onDismiss: (jobId: string) => void;
};

function LibrarySection({
  listState,
  onRetry,
  videos,
  jobs,
  selectedId,
  onSelect,
  onDismiss,
}: LibrarySectionProps) {
  const { t } = useAppI18n();
  if (listState.status === "loading") {
    return <LibrarySkeleton />;
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p role="alert" className="text-sm text-rouge dark:text-rose-300">
          {listState.message === null
            ? t.library.loadErrorFallback
            : formatTemplate(t.library.loadError, {
                message: listState.message,
              })}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-black/10 bg-white px-3 py-1.5 text-sm font-medium text-ink/80 hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-bleu-flag dark:border-white/15 dark:bg-white/5 dark:text-paper/80 dark:hover:bg-white/10 dark:focus-visible:outline-paper/60"
        >
          {t.library.retry}
        </button>
      </div>
    );
  }
  return (
    <VideoGallery
      videos={videos}
      jobs={jobs}
      selectedId={selectedId}
      onSelect={onSelect}
      onDismiss={onDismiss}
    />
  );
}

// LIBRARY_SKELETON_TILES is the placeholder count while the catalog loads: enough
// to fill the first couple of grid rows without implying an exact library size.
const LIBRARY_SKELETON_TILES = 6;

// LibrarySkeleton mirrors the gallery grid with shimmer tiles so the library
// reserves its space and reads as loading rather than empty, with no layout shift
// when the real tiles replace it. It is a status region so assistive tech hears
// "Loading library" instead of nothing.
function LibrarySkeleton() {
  const { t } = useAppI18n();
  return (
    <ul
      role="status"
      aria-label={t.library.loadingAria}
      className={GALLERY_GRID_CLASS}
    >
      {Array.from({ length: LIBRARY_SKELETON_TILES }, (_, index) => (
        <li
          key={index}
          aria-hidden
          className="overflow-hidden rounded-xl border border-black/10 dark:border-white/10"
        >
          <div className="aspect-video w-full animate-pulse bg-black/10 dark:bg-white/10" />
          <div className="px-3 py-2">
            <div className="h-4 w-3/4 animate-pulse rounded bg-black/10 dark:bg-white/10" />
          </div>
        </li>
      ))}
    </ul>
  );
}

function PlayerStage({ active }: { active: ActiveState }) {
  const { t } = useAppI18n();
  switch (active.status) {
    case "ready":
      return (
        <VideoPlayer
          src={active.playable.playback.url}
          title={active.playable.title}
        />
      );
    case "loading":
      return (
        <div
          aria-label={formatTemplate(t.player.loadingAria, {
            title: active.video.title,
          })}
          className="aspect-video w-full animate-pulse rounded-2xl border border-black/10 bg-black/10 dark:border-white/10 dark:bg-white/10"
        />
      );
    case "error":
      return (
        <div
          role="alert"
          className="flex aspect-video w-full flex-col items-center justify-center gap-1 rounded-2xl border border-rouge/25 bg-rouge/5 p-4 text-center text-sm text-rouge dark:border-rouge/40 dark:bg-rouge/15 dark:text-rose-300"
        >
          <p className="font-medium">{t.player.loadError}</p>
          {active.message !== null ? (
            <p className="text-xs">{active.message}</p>
          ) : null}
        </div>
      );
    case "idle":
      return (
        <div className="flex aspect-video w-full items-center justify-center rounded-2xl border border-dashed border-black/15 px-4 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
          {t.player.idle}
        </div>
      );
  }
}

// FactCheckPlaceholder keeps the panel landmark and layout while a selected
// upload has no batch fact-check source. Live verdicts arrive with the streaming
// analysis path.
function FactCheckPlaceholder() {
  const { t } = useAppI18n();
  return (
    <aside
      aria-label={t.panel.factChecks}
      className="flex h-full flex-col gap-2 rounded-2xl border border-black/10 bg-white p-4 dark:border-white/10 dark:bg-white/5"
    >
      <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
        {t.panel.factChecks}
      </h2>
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.factChecks.placeholderHint}
      </p>
    </aside>
  );
}

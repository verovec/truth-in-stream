"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { LiveAnalysisProvider } from "@/components/live/live-analysis-provider";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { VideoPlayer } from "@/components/playback/video-player";
import { useVideoAnalysis } from "@/hooks/use-video-analysis";
import { getVideo, type LibraryVideo, type PlayableVideo } from "@/lib/video/api";
import {
  type AnalysedLibraryVideo,
  getVideoAnalysis,
  listVideosWithAnalysis,
  startVideoAnalysis,
  type VideoAnalysis,
  type VideoAnalysisStatus,
} from "@/lib/video/analysis";
import { ExportControls } from "@/components/export/export-controls";
import type { Role } from "@/lib/auth/token";
import type { AudioCaptureFactory, LiveSocketFactory } from "@/lib/live/ports";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { AnalysisControl } from "./analysis-control";
import { LiveFactCheckPanel } from "./live-fact-check-panel";
import { LiveSpeakerCredibility } from "./live-speaker-credibility";
import { LiveSummaryStrip } from "./live-summary-strip";
import { GALLERY_GRID_CLASS, VideoGallery } from "./video-gallery";

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
// viewer pick videos, plays the selected one, and shows fact checks for it. It is
// a pure consumption surface - ingestion (upload, YouTube import, delete) lives in
// the admin backoffice, not here. The library is fetched on the client (like the
// rest of this app's data) so it rides the same-origin proxy that makes the
// backend session cookie first-party; loadVideos, loadAnalysis, startAnalysis,
// pollIntervalMs, socketFactory, and captureFactory are injection seams for
// tests.
export function LibraryExperience({
  role = "guest",
  loadVideos = listVideosWithAnalysis,
  loadAnalysis = getVideoAnalysis,
  startAnalysis = startVideoAnalysis,
  pollIntervalMs,
  socketFactory,
  captureFactory,
}: {
  role?: Role;
  loadVideos?: (signal?: AbortSignal) => Promise<AnalysedLibraryVideo[]>;
  loadAnalysis?: (id: string, signal?: AbortSignal) => Promise<VideoAnalysis>;
  startAnalysis?: (id: string, signal?: AbortSignal) => Promise<void>;
  pollIntervalMs?: number;
  socketFactory?: LiveSocketFactory;
  captureFactory?: AudioCaptureFactory;
}) {
  const [videos, setVideos] = useState<AnalysedLibraryVideo[]>([]);
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

  // The library list is the single source of each video's analysis status: the
  // tracker reports observed transitions (a run started, completed, or failed)
  // back here so the tile badge, the control, and the playback gate all read
  // one truth. analyzedAt updates only when the transition carried it.
  const handleAnalysisStatusChange = useCallback(
    (
      videoId: string,
      status: VideoAnalysisStatus,
      analyzedAt?: string | null,
    ) => {
      setVideos((prev) =>
        prev.map((video) =>
          video.id === videoId
            ? {
                ...video,
                analysisStatus: status,
                analyzedAt:
                  analyzedAt === undefined ? video.analyzedAt : analyzedAt,
              }
            : video,
        ),
      );
    },
    [],
  );

  // Tracks the selected video's pre-analysis: fetches the stored frames of a
  // complete video for hydration, polls while a run is analysing, and exposes
  // the admin trigger.
  const track = useVideoAnalysis({
    video: selectedVideo,
    onStatusChange: handleAnalysisStatusChange,
    loadAnalysis,
    startAnalysis,
    pollIntervalMs,
  });

  // A video is on the analysed playback path when its stored frames are
  // hydrated - kept through a re-analysis so the current result never vanishes
  // mid-run - or the list says a stored result exists (frames still being
  // fetched: the live socket must already stay closed while they load).
  const analysed =
    track.frames !== null || selectedVideo?.analysisStatus === "complete";

  // Keyed on selectedId (a stable string), not the selectedVideo object, so
  // rebuilding the videos array does not re-fetch and flicker the currently-
  // playing video. Also keyed on the selected video's status so a row that
  // becomes ready re-resolves and plays without a manual re-click.
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

  const { t } = useAppI18n();
  const active = resolveActive(selectedVideo, resolved);
  const activeVideoId = active.status === "ready" ? active.playable.id : null;
  // The title of the video on the player, surfaced above the library so the
  // viewer always sees what is playing. Resolved from the ready playback shape
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
      {/* One analysis session feeds both the top-of-page summary strip and the
          in-grid fact-check panel: the provider owns the single WebSocket (or
          the hydrated stored analysis for a pre-analysed video) and publishes
          to a store the two read independently, so neither the player nor the
          library re-renders as findings stream in. */}
      <LiveAnalysisProvider
        videoId={activeVideoId}
        analysed={analysed}
        analysedFrames={track.frames}
        socketFactory={socketFactory}
        captureFactory={captureFactory}
      >
        <div className="flex flex-col gap-4">
          <LiveSummaryStrip />
          <LiveSpeakerCredibility />
          <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <div className="flex flex-col gap-4">
              <PlayerStage active={active} />
              <AnalysisControl role={role} video={selectedVideo} track={track} />
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
                <LibrarySection
                  listState={listState}
                  onRetry={retryLibrary}
                  videos={videos}
                  selectedId={selectedId}
                  onSelect={(video) => setSelectedId(video.id)}
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
  videos: AnalysedLibraryVideo[];
  selectedId: string | null;
  onSelect: (video: AnalysedLibraryVideo) => void;
};

function LibrarySection({
  listState,
  onRetry,
  videos,
  selectedId,
  onSelect,
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
      selectedId={selectedId}
      onSelect={onSelect}
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

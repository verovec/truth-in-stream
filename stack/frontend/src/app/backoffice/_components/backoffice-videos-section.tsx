"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useVideoUploads } from "@/hooks/use-video-uploads";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import {
  deleteVideo,
  type LibraryVideo,
  submitYoutubeUrl,
} from "@/lib/video/api";
import type { PutUploader } from "@/lib/video/upload";
import { GALLERY_GRID_CLASS } from "@/app/app/_components/video-gallery";
import { UploadTile } from "@/app/app/_components/upload-tile";
import { VideoUploader } from "@/app/app/_components/video-uploader";
import { YoutubeUrlForm } from "@/app/app/_components/youtube-url-form";
import {
  getVideoAnalysis,
  listBackofficeVideos,
  startVideoAnalysis,
  type BackofficeVideo,
  type VideoAnalysisDetail,
} from "./analysis-api";
import { BackofficeVideoList } from "./backoffice-video-list";

// DEFAULT_POLL_MS is how often the section re-checks the backend while a
// server-driven transition is pending: a YouTube row still downloading, or a
// pre-analysis run in flight. Neither has a client step, so both can only be
// observed by polling.
const DEFAULT_POLL_MS = 2500;

// mergePolled advances rows to whatever the freshly listed catalog reports
// where a server-driven transition can occur - pending YouTube downloads and
// the analysis lifecycle - leaving every other row untouched. It returns the
// previous array unchanged when nothing moved so a poll that observes no
// transition does not trigger a re-render.
//
// Two analysis reports are rejected as stale reads from a response that raced
// the analyse trigger: an analysing row never regresses to "none" (the
// backend only moves analysing to complete or failed), and a "complete"
// carrying the analyzedAt the row already had is the pre-trigger stored
// result, not a new completion (a genuine one always re-dates it). Taking
// either would stop the polling loop with a live run invisible.
function mergePolled(
  prev: BackofficeVideo[],
  listed: BackofficeVideo[],
): BackofficeVideo[] {
  const byId = new Map(listed.map((video) => [video.id, video]));
  let changed = false;
  const next = prev.map((video) => {
    const fresh = byId.get(video.id);
    if (!fresh) {
      return video;
    }
    const youtubeAdvanced =
      video.kind === "youtube" &&
      video.status === "pending" &&
      fresh.status !== video.status;
    const staleAnalysis =
      video.analysisStatus === "analysing" &&
      (fresh.analysisStatus === "none" ||
        (fresh.analysisStatus === "complete" &&
          fresh.analyzedAt === video.analyzedAt));
    const analysisMoved =
      !staleAnalysis &&
      (fresh.analysisStatus !== video.analysisStatus ||
        fresh.analyzedAt !== video.analyzedAt);
    if (youtubeAdvanced || analysisMoved) {
      changed = true;
      return fresh;
    }
    return video;
  });
  return changed ? next : prev;
}

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string | null };

// BackofficeVideosSection is the admin-only ingestion surface: the file uploader,
// the YouTube-link form, in-flight upload tiles, and a management list of every
// video with its analysis state and controls plus a delete control. The
// consumption app (/app) only lists and plays; all ingestion lives here. The
// catalog is re-listed from the backend (the source of truth) after each
// mutation - confirm, ingest, and delete all persist before their callback
// fires, so a re-list reflects them without optimistic merging. The one
// exception is the analyse trigger: its 202 races the next list, so the row is
// flipped to analysing locally, which is what the backend just recorded.
// loadVideos, pollVideos, remove, submitYoutube, startAnalysis, loadAnalysis,
// uploader, and pollIntervalMs are injection seams so tests can drive each
// path deterministically.
export function BackofficeVideosSection({
  loadVideos = listBackofficeVideos,
  pollVideos = listBackofficeVideos,
  remove = deleteVideo,
  submitYoutube = submitYoutubeUrl,
  startAnalysis = startVideoAnalysis,
  loadAnalysis = getVideoAnalysis,
  uploader,
  pollIntervalMs = DEFAULT_POLL_MS,
}: {
  loadVideos?: (signal?: AbortSignal) => Promise<BackofficeVideo[]>;
  pollVideos?: (signal?: AbortSignal) => Promise<BackofficeVideo[]>;
  remove?: (id: string, signal?: AbortSignal) => Promise<void>;
  submitYoutube?: (url: string, signal?: AbortSignal) => Promise<LibraryVideo>;
  startAnalysis?: (id: string, signal?: AbortSignal) => Promise<void>;
  loadAnalysis?: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<VideoAnalysisDetail>;
  uploader?: PutUploader;
  pollIntervalMs?: number;
} = {}) {
  const [videos, setVideos] = useState<BackofficeVideo[]>([]);
  const [listState, setListState] = useState<ListState>({ status: "loading" });
  const [reloadToken, setReloadToken] = useState(0);

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

  // refresh re-lists the catalog without flashing the skeleton (the current list
  // stays on screen until the fresh one lands), used after an upload confirms, a
  // YouTube link is ingested, or a video is deleted.
  const refresh = useCallback(() => {
    setReloadToken((token) => token + 1);
  }, []);

  const { jobs, startUploads, dismiss } = useVideoUploads({
    uploader,
    onUploaded: () => refresh(),
  });

  // The catalog loads on the client (like the rest of this app's data, riding the
  // same-origin proxy) and reloads when reloadToken changes. The fetch is aborted
  // on unmount/reload so a stale response cannot land; every state write happens
  // asynchronously, never synchronously in the effect body.
  useEffect(() => {
    const controller = new AbortController();
    loadVideosRef
      .current(controller.signal)
      .then((loaded) => {
        if (controller.signal.aborted) {
          return;
        }
        setVideos(loaded);
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

  // While any YouTube row is still downloading or any analysis run is in
  // flight, re-list the catalog on an interval and advance those rows in
  // place; stop once nothing is pending. The controller aborts the in-flight
  // request on unmount or when the last pending row resolves.
  const hasPendingTransition = videos.some(
    (video) =>
      (video.kind === "youtube" && video.status === "pending") ||
      video.analysisStatus === "analysing",
  );
  useEffect(() => {
    if (!hasPendingTransition) {
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
          setVideos((prev) => mergePolled(prev, listed));
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
  }, [hasPendingTransition, pollIntervalMs]);

  // markAnalysing reflects an accepted analyse trigger (or a 409 that proved a
  // run is already live) without waiting for the next list: the flip is what
  // the backend recorded before answering, and it is what arms the polling
  // that will observe the run's real progress and completion.
  const markAnalysing = useCallback((id: string) => {
    setVideos((prev) => {
      const next = prev.map((video) =>
        video.id === id && video.analysisStatus !== "analysing"
          ? { ...video, analysisStatus: "analysing" as const }
          : video,
      );
      return next.some((video, i) => video !== prev[i]) ? next : prev;
    });
  }, []);

  const { t } = useAppI18n();
  const inFlight = jobs.filter((job) => job.state.status !== "ready");

  return (
    <div className="flex flex-col gap-4">
      <VideoUploader onFiles={startUploads} />
      <YoutubeUrlForm onAdded={() => refresh()} submit={submitYoutube} />
      {inFlight.length > 0 ? (
        <ul className={GALLERY_GRID_CLASS}>
          {inFlight.map((job) => (
            <li key={job.id}>
              <UploadTile job={job} onDismiss={() => dismiss(job.id)} />
            </li>
          ))}
        </ul>
      ) : null}
      <div className="flex flex-col gap-2">
        <h4 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {t.backoffice.videos.list.heading}
        </h4>
        <ManagementList
          listState={listState}
          onRetry={retry}
          videos={videos}
          remove={remove}
          onDeleted={refresh}
          startAnalysis={startAnalysis}
          onAnalysisStarted={markAnalysing}
          loadAnalysis={loadAnalysis}
          pollIntervalMs={pollIntervalMs}
        />
      </div>
    </div>
  );
}

type ManagementListProps = {
  listState: ListState;
  onRetry: () => void;
  videos: BackofficeVideo[];
  remove: (id: string, signal?: AbortSignal) => Promise<void>;
  onDeleted: () => void;
  startAnalysis: (id: string, signal?: AbortSignal) => Promise<void>;
  onAnalysisStarted: (id: string) => void;
  loadAnalysis: (
    id: string,
    signal?: AbortSignal,
  ) => Promise<VideoAnalysisDetail>;
  pollIntervalMs: number;
};

function ManagementList({
  listState,
  onRetry,
  videos,
  remove,
  onDeleted,
  startAnalysis,
  onAnalysisStarted,
  loadAnalysis,
  pollIntervalMs,
}: ManagementListProps) {
  const { t } = useAppI18n();
  if (listState.status === "loading") {
    return <ManagementSkeleton />;
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
    <BackofficeVideoList
      videos={videos}
      remove={remove}
      onDeleted={onDeleted}
      startAnalysis={startAnalysis}
      onAnalysisStarted={onAnalysisStarted}
      loadAnalysis={loadAnalysis}
      pollIntervalMs={pollIntervalMs}
    />
  );
}

// MANAGEMENT_SKELETON_ROWS is the placeholder row count while the catalog loads:
// enough to reserve the list's space so it reads as loading rather than empty.
const MANAGEMENT_SKELETON_ROWS = 4;

function ManagementSkeleton() {
  const { t } = useAppI18n();
  return (
    <ul
      role="status"
      aria-label={t.library.loadingAria}
      className="flex flex-col gap-2"
    >
      {Array.from({ length: MANAGEMENT_SKELETON_ROWS }, (_, index) => (
        <li
          key={index}
          aria-hidden
          className="h-11 animate-pulse rounded-lg border border-black/10 bg-black/5 dark:border-white/10 dark:bg-white/10"
        />
      ))}
    </ul>
  );
}

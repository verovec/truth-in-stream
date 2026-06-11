"use client";

import { useCallback, useEffect, useRef, useState } from "react";
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
import { factCheckSourceFor } from "@/lib/video/fact-check-source";
import type { PutUploader } from "@/lib/video/upload";
import { FactCheckPanel } from "./fact-check-panel";
import { VideoGallery } from "./video-gallery";
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
  | { status: "error"; video: LibraryVideo; message: string };

// Resolved is the fetched outcome for one video id. Loading and idle are derived
// from it during render rather than written into state, so the fetch effect only
// ever sets state asynchronously.
type Resolved =
  | { forId: string; status: "ready"; playable: PlayableVideo }
  | { forId: string; status: "error"; message: string };

type ListState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string };

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
  loadVideos = listVideos,
  pollVideos = listVideos,
  uploader,
  submitYoutube = submitYoutubeUrl,
  pollIntervalMs = DEFAULT_YOUTUBE_POLL_MS,
}: {
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
          message:
            err instanceof Error ? err.message : "Could not load the library.",
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
          message: err instanceof Error ? err.message : "Could not load video.",
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

  const active = resolveActive(selectedVideo, resolved);

  const factCheckSource = selectedVideo
    ? factCheckSourceFor(selectedVideo)
    : null;

  return (
    <PlaybackProvider>
      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex flex-col gap-4">
          <PlayerStage active={active} />
          <section className="flex flex-col gap-3">
            <h2 className="text-sm font-semibold uppercase tracking-wide text-zinc-700 dark:text-zinc-300">
              Library
            </h2>
            <VideoUploader onFiles={startUploads} />
            <YoutubeUrlForm onAdded={handleYoutubeAdded} submit={submitYoutube} />
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
        {factCheckSource ? (
          <FactCheckPanel key={factCheckSource} source={factCheckSource} />
        ) : (
          <FactCheckPlaceholder />
        )}
      </div>
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
  if (listState.status === "loading") {
    return (
      <p className="text-sm text-zinc-500 dark:text-zinc-400">
        Loading library…
      </p>
    );
  }
  if (listState.status === "error") {
    return (
      <div className="flex flex-col items-start gap-2">
        <p
          role="alert"
          className="text-sm text-rose-700 dark:text-rose-300"
        >
          The library could not load: {listState.message}
        </p>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm font-medium text-zinc-800 hover:bg-zinc-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800"
        >
          Try again
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

function PlayerStage({ active }: { active: ActiveState }) {
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
          aria-label={`Loading ${active.video.title}`}
          className="aspect-video w-full animate-pulse rounded-xl bg-zinc-200 dark:bg-zinc-800"
        />
      );
    case "error":
      return (
        <div
          role="alert"
          className="flex aspect-video w-full flex-col items-center justify-center gap-1 rounded-xl border border-rose-200 bg-rose-50 p-4 text-center text-sm text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
        >
          <p className="font-medium">This video could not be loaded.</p>
          <p className="text-xs">{active.message}</p>
        </div>
      );
    case "idle":
      return (
        <div className="flex aspect-video w-full items-center justify-center rounded-xl border border-dashed border-zinc-300 px-4 text-center text-sm text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
          Select a video from the library to play it.
        </div>
      );
  }
}

// FactCheckPlaceholder keeps the panel landmark and layout while a selected
// upload has no batch fact-check source. Live verdicts arrive with the streaming
// analysis path.
function FactCheckPlaceholder() {
  return (
    <aside
      aria-label="Fact checks"
      className="flex h-full flex-col gap-2 rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <h2 className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100">
        Fact checks
      </h2>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">
        Fact checks stream here while the video plays.
      </p>
    </aside>
  );
}

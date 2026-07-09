"use client";

import type { LibraryVideo } from "@/lib/video/api";
import type { UploadJob } from "@/hooks/use-video-uploads";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { UploadTile } from "./upload-tile";
import { VideoTile } from "./video-tile";

type VideoGalleryProps = {
  videos: LibraryVideo[];
  jobs: UploadJob[];
  selectedId: string | null;
  onSelect: (video: LibraryVideo) => void;
  onDismiss: (jobId: string) => void;
};

// GALLERY_GRID_CLASS is the library grid layout, shared so the loading skeleton
// reserves exactly the gallery's columns and gaps and cannot drift from it.
export const GALLERY_GRID_CLASS = "grid grid-cols-2 gap-3 sm:grid-cols-3";

// VideoGallery is the library grid: in-flight uploads first, then every video.
// A ready job is represented by its confirmed library row, so it is filtered out
// here to avoid showing the same video twice.
export function VideoGallery({
  videos,
  jobs,
  selectedId,
  onSelect,
  onDismiss,
}: VideoGalleryProps) {
  const { t } = useAppI18n();
  const inFlight = jobs.filter((job) => job.state.status !== "ready");

  if (videos.length === 0 && inFlight.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-black/15 px-4 py-8 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
        {t.library.empty}
      </p>
    );
  }

  return (
    <ul className={GALLERY_GRID_CLASS}>
      {inFlight.map((job) => (
        <li key={job.id}>
          <UploadTile job={job} onDismiss={() => onDismiss(job.id)} />
        </li>
      ))}
      {videos.map((video) => (
        <li key={video.id}>
          <VideoTile
            video={video}
            selected={video.id === selectedId}
            onSelect={() => onSelect(video)}
          />
        </li>
      ))}
    </ul>
  );
}

"use client";

import type { LibraryVideo } from "@/lib/video/api";
import type { UploadJob } from "@/hooks/use-video-uploads";
import { UploadTile } from "./upload-tile";
import { VideoTile } from "./video-tile";

type VideoGalleryProps = {
  videos: LibraryVideo[];
  jobs: UploadJob[];
  selectedId: string | null;
  onSelect: (video: LibraryVideo) => void;
  onDismiss: (jobId: string) => void;
};

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
  const inFlight = jobs.filter((job) => job.state.status !== "ready");

  if (videos.length === 0 && inFlight.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-zinc-300 px-4 py-8 text-center text-sm text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
        No videos yet. Upload one to get started.
      </p>
    );
  }

  return (
    <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3">
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

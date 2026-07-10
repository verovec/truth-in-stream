"use client";

import type { LibraryVideo } from "@/lib/video/api";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { VideoTile } from "./video-tile";

type VideoGalleryProps = {
  videos: LibraryVideo[];
  selectedId: string | null;
  onSelect: (video: LibraryVideo) => void;
};

// GALLERY_GRID_CLASS is the library grid layout, shared so the loading skeleton
// reserves exactly the gallery's columns and gaps and cannot drift from it.
export const GALLERY_GRID_CLASS = "grid grid-cols-2 gap-3 sm:grid-cols-3";

// VideoGallery is the /app library grid: a pure consumption surface listing every
// video for playback. Ingestion (uploads, YouTube imports) lives in the admin
// backoffice, so no in-flight upload tiles are shown here.
export function VideoGallery({
  videos,
  selectedId,
  onSelect,
}: VideoGalleryProps) {
  const { t } = useAppI18n();

  if (videos.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-black/15 px-4 py-8 text-center text-sm text-ink/50 dark:border-white/15 dark:text-paper/50">
        {t.library.empty}
      </p>
    );
  }

  return (
    <ul className={GALLERY_GRID_CLASS}>
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
